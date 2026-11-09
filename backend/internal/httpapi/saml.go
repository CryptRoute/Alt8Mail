package httpapi

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
)

const (
	// samlDiscoveryTimeout bounds a single IdP metadata load. The login page
	// never waits on it: only the metadata, start and ACS handlers do.
	samlDiscoveryTimeout = 10 * time.Second
	// samlDiscoveryRetryBackoff keeps a temporarily unreachable IdP from being
	// hammered on every sign-in attempt while still letting the next attempt
	// succeed after the provider recovers.
	samlDiscoveryRetryBackoff = 30 * time.Second
	// samlTrackedRequestMaxAge bounds how long a user may spend at the IdP.
	samlTrackedRequestMaxAge = 10 * time.Minute
	// samlMaxMetadataSize caps the metadata document read from a file or URL.
	samlMaxMetadataSize = 2 << 20 // 2 MiB

	samlMetadataPath = "/api/auth/saml/metadata"
	samlACSPath      = "/api/auth/saml/acs"
)

var errSAMLNotConfigured = errors.New("SAML SSO is not configured")

// defaultSAMLMetadataHTTPClient fetches IdP metadata. Every redirect is
// re-validated so an HTTPS or loopback URL cannot bounce the client to a
// cleartext or non-loopback location, and an HTTPS URL can never be
// downgraded to cleartext.
var defaultSAMLMetadataHTTPClient = &http.Client{
	Timeout: samlDiscoveryTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many SAML IdP metadata redirects")
		}
		if len(via) > 0 && strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return errors.New("SAML IdP metadata redirect must not downgrade from HTTPS")
		}
		if !samlMetadataURLAllowed(req.URL) {
			return errors.New("SAML IdP metadata redirect must use HTTPS (HTTP is allowed only for loopback)")
		}
		return nil
	},
}

// samlConfigured reports whether every required SAML setting is present and
// the verified license includes the paid saml entitlement. An incomplete
// configuration or a missing/expired entitlement disables SAML entirely,
// leaving OIDC, password and passkey sign-in untouched. It never contacts the
// identity provider, so the public login page can call it on every request.
func (s *Server) samlConfigured() bool {
	cfg := s.cfg
	if !s.entitled("saml") {
		return false
	}
	if strings.TrimSpace(cfg.SAMLSPCertFile) == "" || strings.TrimSpace(cfg.SAMLSPKeyFile) == "" {
		return false
	}
	if strings.TrimSpace(cfg.SAMLIDPMetadataFile) == "" {
		raw := strings.TrimSpace(cfg.SAMLIDPMetadataURL)
		if raw == "" {
			return false
		}
		endpoint, err := url.Parse(raw)
		if err != nil || !samlMetadataURLAllowed(endpoint) {
			return false
		}
	}
	if _, err := s.samlPublicURL(); err != nil {
		return false
	}
	return true
}

// samlDisplayName is the label the login page shows on the SAML button.
func (s *Server) samlDisplayName() string {
	return strings.TrimSpace(s.cfg.SAMLDisplayName)
}

// samlPublicURL returns the public origin used for SP metadata and ACS URLs.
// SAML assertions and AuthnRequests carry session material, so a cleartext
// origin is accepted only for loopback development and tests.
func (s *Server) samlPublicURL() (*url.URL, error) {
	raw := strings.TrimSpace(s.cfg.PublicURL)
	if raw == "" {
		return nil, errors.New("ALT8MAIL_PUBLIC_URL is required for SAML SSO")
	}
	publicURL, err := url.Parse(raw)
	if err != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" {
		return nil, errors.New("ALT8MAIL_PUBLIC_URL must be an absolute HTTP(S) URL for SAML SSO")
	}
	if publicURL.Scheme == "http" && !samlLoopbackHost(publicURL.Hostname()) {
		return nil, errors.New("ALT8MAIL_PUBLIC_URL must use HTTPS for SAML SSO (HTTP is allowed only for loopback)")
	}
	return publicURL, nil
}

// samlEntityID is the configured SP entity ID, or the SP metadata URL when the
// operator does not override it.
func (s *Server) samlEntityID(publicURL *url.URL) string {
	if entityID := strings.TrimSpace(s.cfg.SAMLSPEntityID); entityID != "" {
		return entityID
	}
	return publicURL.ResolveReference(&url.URL{Path: samlMetadataPath}).String()
}

// samlMiddleware returns the lazily constructed SAML SP middleware. IdP
// metadata and the SP key pair are loaded on demand so an unreachable identity
// provider or a broken key file can never block server startup or the public
// login page. Failures are cached briefly and then retried.
func (s *Server) samlMiddleware(ctx context.Context) (*samlsp.Middleware, error) {
	if !s.samlConfigured() {
		return nil, errSAMLNotConfigured
	}
	s.samlMu.Lock()
	defer s.samlMu.Unlock()
	if s.samlCached != nil {
		return s.samlCached, nil
	}
	if !s.samlLastTry.IsZero() && time.Since(s.samlLastTry) < samlDiscoveryRetryBackoff {
		if s.samlLastErr != nil {
			return nil, fmt.Errorf("SAML metadata load failed recently: %w", s.samlLastErr)
		}
		return nil, errors.New("SAML metadata load is cooling down")
	}
	s.samlLastTry = time.Now()
	// Detach the load from the caller's request cancellation. A browser
	// abandoning the request must not cache a failure that locks every SAML
	// user out for the retry backoff.
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), samlDiscoveryTimeout)
	defer cancel()
	middleware, err := s.buildSAMLSP(loadCtx)
	if err != nil {
		s.samlLastErr = err
		if s.logger != nil {
			s.logger.Warn("SAML SP initialization failed", "err", err)
		}
		return nil, err
	}
	s.samlLastErr = nil
	s.samlCached = middleware
	return middleware, nil
}

// buildSAMLSP loads the key pair and IdP metadata and constructs the
// crewjam/saml service provider middleware.
func (s *Server) buildSAMLSP(ctx context.Context) (*samlsp.Middleware, error) {
	publicURL, err := s.samlPublicURL()
	if err != nil {
		return nil, err
	}
	key, certificate, err := loadSAMLSPKeyPair(s.cfg.SAMLSPCertFile, s.cfg.SAMLSPKeyFile)
	if err != nil {
		return nil, err
	}
	metadata, err := s.loadSAMLIDPMetadata(ctx)
	if err != nil {
		return nil, err
	}
	middleware, err := samlsp.New(samlsp.Options{
		EntityID:    s.samlEntityID(publicURL),
		URL:         *publicURL,
		Key:         key,
		Certificate: certificate,
		IDPMetadata: metadata,
		// AuthnRequests must be signed, and assertions must be signed by the
		// IdP: no unsigned fallback is offered. Only the HTTP-POST ACS binding
		// is advertised, matching the handler implemented in Task 2.
		SignRequest:                      true,
		AllowIDPInitiated:                false,
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		MetadataPath:                     samlMetadataPath,
		AcsPath:                          samlACSPath,
		TrackedRequestMaxAge:             samlTrackedRequestMaxAge,
	})
	if err != nil {
		return nil, err
	}
	// Install the sanitized handler here, at construction time, so the cached
	// middleware Task 2 serves never uses samlsp.DefaultOnError. That default
	// logs the raw SAMLResponse, including NameID and attributes.
	middleware.OnError = s.samlOnError
	return middleware, nil
}

// samlOnError is installed on the cached SAML middleware when it is built. It
// never logs or echoes the error value: crewjam errors can embed the raw
// SAMLResponse or assertion XML.
func (s *Server) samlOnError(w http.ResponseWriter, r *http.Request, _ error) {
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	if s.logger != nil {
		s.logger.Warn("SAML request failed", "path", path)
	}
	http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
}

// loadSAMLIDPMetadata reads the configured IdP metadata file, or fetches the
// configured metadata URL. A file takes precedence over a URL because it was
// provisioned deliberately and is not exposed to network tampering.
func (s *Server) loadSAMLIDPMetadata(ctx context.Context) (*saml.EntityDescriptor, error) {
	if file := strings.TrimSpace(s.cfg.SAMLIDPMetadataFile); file != "" {
		if strings.TrimSpace(s.cfg.SAMLIDPMetadataURL) != "" && s.logger != nil {
			s.logger.Warn("SAML IdP metadata file takes precedence over the configured metadata URL")
		}
		data, err := readSAMLMetadataFile(file)
		if err != nil {
			return nil, err
		}
		return parseSAMLMetadata(data)
	}
	raw := strings.TrimSpace(s.cfg.SAMLIDPMetadataURL)
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse SAML IdP metadata URL: %w", err)
	}
	if !samlMetadataURLAllowed(endpoint) {
		return nil, errors.New("SAML IdP metadata URL must use HTTPS (HTTP is allowed only for loopback)")
	}
	data, err := fetchSAMLMetadata(ctx, s.samlHTTPClient(), endpoint)
	if err != nil {
		return nil, err
	}
	return parseSAMLMetadata(data)
}

func parseSAMLMetadata(data []byte) (*saml.EntityDescriptor, error) {
	entity, err := samlsp.ParseMetadata(data)
	if err != nil {
		return nil, fmt.Errorf("parse SAML IdP metadata: %w", err)
	}
	if entity == nil {
		return nil, errors.New("SAML IdP metadata is empty")
	}
	if len(entity.IDPSSODescriptors) == 0 {
		return nil, errors.New("SAML IdP metadata has no IDPSSODescriptor")
	}
	return entity, nil
}

func readSAMLMetadataFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open SAML IdP metadata file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, samlMaxMetadataSize+1))
	if err != nil {
		return nil, fmt.Errorf("read SAML IdP metadata file: %w", err)
	}
	if len(data) > samlMaxMetadataSize {
		return nil, fmt.Errorf("SAML IdP metadata file exceeds %d bytes", samlMaxMetadataSize)
	}
	return data, nil
}

func fetchSAMLMetadata(ctx context.Context, client *http.Client, endpoint *url.URL) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build SAML IdP metadata request: %w", err)
	}
	request.Header.Set("Accept", "application/samlmetadata+xml, application/xml;q=0.9, text/xml;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch SAML IdP metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("fetch SAML IdP metadata: unexpected status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, samlMaxMetadataSize+1))
	if err != nil {
		return nil, fmt.Errorf("read SAML IdP metadata response: %w", err)
	}
	if len(data) > samlMaxMetadataSize {
		return nil, fmt.Errorf("SAML IdP metadata response exceeds %d bytes", samlMaxMetadataSize)
	}
	return data, nil
}

func (s *Server) samlHTTPClient() *http.Client {
	if s.samlMetadataClient != nil {
		return s.samlMetadataClient
	}
	return defaultSAMLMetadataHTTPClient
}

// samlMetadataURLAllowed accepts HTTPS metadata URLs and cleartext HTTP only
// for loopback test endpoints. Loopback is never accepted over a non-HTTP
// scheme.
func samlMetadataURLAllowed(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.Host == "" {
		return false
	}
	switch strings.ToLower(endpoint.Scheme) {
	case "https":
		return true
	case "http":
		return samlLoopbackHost(endpoint.Hostname())
	default:
		return false
	}
}

func samlLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loadSAMLSPKeyPair loads the RSA private key and certificate that sign
// AuthnRequests and identify the SP. It never logs or returns the key material.
func loadSAMLSPKeyPair(certFile, keyFile string) (*rsa.PrivateKey, *x509.Certificate, error) {
	keyPEM, err := os.ReadFile(strings.TrimSpace(keyFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read SAML SP key file: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("SAML SP key file is not PEM encoded")
	}
	key, err := parseSAMLSPPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if key.N.BitLen() < 2048 {
		return nil, nil, errors.New("SAML SP private key must be at least 2048 bits")
	}

	certPEM, err := os.ReadFile(strings.TrimSpace(certFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read SAML SP certificate file: %w", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("SAML SP certificate file is not PEM encoded")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse SAML SP certificate: %w", err)
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
		return nil, nil, errors.New("SAML SP certificate is not currently valid")
	}
	if certificate.KeyUsage != 0 && certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return nil, nil, errors.New("SAML SP certificate key usage must include digital signature")
	}
	certPublicKey, ok := certificate.PublicKey.(*rsa.PublicKey)
	if !ok || !certPublicKey.Equal(&key.PublicKey) {
		return nil, nil, errors.New("SAML SP certificate does not match the private key")
	}
	return key, certificate, nil
}

func parseSAMLSPPrivateKey(der []byte) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse SAML SP private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("SAML SP private key must be RSA")
	}
	return key, nil
}

// samlConfig is the public, unauthenticated capability probe used by the login
// page. It reports configuration only and never contacts the identity provider.
func (s *Server) samlConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.samlConfigured(),
		"displayName": s.samlDisplayName(),
	})
}

// samlMetadata serves the SP metadata consumed by the IdP. SAML is
// license-gated: an unlicensed or unconfigured deployment does not advertise
// the endpoint at all.
func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.samlConfigured() {
		http.NotFound(w, r)
		return
	}
	middleware, err := s.samlMiddleware(r.Context())
	if err != nil {
		// The error describes configuration or fetch failures, never metadata
		// contents, assertions or key material.
		if s.logger != nil {
			s.logger.Warn("SAML SP metadata unavailable", "err", err)
		}
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	middleware.ServeMetadata(w, r)
}
