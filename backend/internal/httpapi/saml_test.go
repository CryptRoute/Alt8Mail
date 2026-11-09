package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

// samlTestMaterial is a throwaway RSA key pair used only by these tests.
type samlTestMaterial struct {
	certPEM []byte
	keyPEM  []byte
	certDER []byte
}

func newSAMLTestMaterial(t *testing.T) samlTestMaterial {
	t.Helper()
	return newSAMLTestMaterialFor(t, 2048, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
}

func newSAMLTestMaterialFor(t *testing.T, bits int, notBefore, notAfter time.Time) samlTestMaterial {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "Alt8Mail SAML Test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create self-signed certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return samlTestMaterial{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		certDER: der,
	}
}

// writeSAMLTestPair writes a generated key pair and returns its file paths.
func writeSAMLTestPair(t *testing.T, material samlTestMaterial, prefix string) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile = filepath.Join(dir, prefix+".crt")
	keyFile = filepath.Join(dir, prefix+".key")
	if err := os.WriteFile(certFile, material.certPEM, 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, material.keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// samlTestMetadata is a minimal, local IdP metadata document. It never points
// at a live identity provider.
func samlTestMetadata(entityID string, certDER []byte) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="%s">
  <md:IDPSSODescriptor WantAuthnRequestsSigned="true" protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <md:KeyDescriptor use="signing">
      <ds:KeyInfo xmlns:ds="http://www.w3.org/2000/09/xmldsig#">
        <ds:X509Data>
          <ds:X509Certificate>%s</ds:X509Certificate>
        </ds:X509Data>
      </ds:KeyInfo>
    </md:KeyDescriptor>
    <md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.test/sso"/>
    <md:SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://idp.example.test/sso"/>
  </md:IDPSSODescriptor>
</md:EntityDescriptor>`, entityID, base64.StdEncoding.EncodeToString(certDER)))
}

const samlTestIDPEntityID = "https://idp.example.test/metadata"

// samlTestEnv is a complete file-backed SAML configuration plus the files it
// references. Tests mutate cfg to exercise invalid configurations.
type samlTestEnv struct {
	cfg          config.Config
	material     samlTestMaterial
	metadata     []byte
	metadataFile string
	certFile     string
	keyFile      string
}

func newSAMLTestEnv(t *testing.T) *samlTestEnv {
	t.Helper()
	dir := t.TempDir()
	material := newSAMLTestMaterial(t)
	env := &samlTestEnv{
		material:     material,
		metadata:     samlTestMetadata(samlTestIDPEntityID, material.certDER),
		metadataFile: filepath.Join(dir, "idp-metadata.xml"),
		certFile:     filepath.Join(dir, "sp.crt"),
		keyFile:      filepath.Join(dir, "sp.key"),
	}
	if err := os.WriteFile(env.metadataFile, env.metadata, 0o600); err != nil {
		t.Fatalf("write IdP metadata: %v", err)
	}
	if err := os.WriteFile(env.certFile, material.certPEM, 0o600); err != nil {
		t.Fatalf("write SP certificate: %v", err)
	}
	if err := os.WriteFile(env.keyFile, material.keyPEM, 0o600); err != nil {
		t.Fatalf("write SP key: %v", err)
	}
	env.cfg = config.Config{
		PublicURL:           "https://mail.example.test",
		SAMLIDPMetadataFile: env.metadataFile,
		SAMLSPCertFile:      env.certFile,
		SAMLSPKeyFile:       env.keyFile,
		SAMLDisplayName:     "Example SAML",
		SAMLEmailDomains:    []string{"example.test"},
		SessionSecret:       "test-session-secret",
		CSRFSecret:          "test-csrf-secret",
	}
	return env
}

func installSAMLLicense(t *testing.T, server *Server) *mutableClock {
	t.Helper()
	clock := newMutableClock(time.Now())
	server.license = &licensing.License{
		Version: licensing.Version, ID: "lic_saml_test", Edition: "enterprise", Seats: 10,
		Features: []string{"saml"}, IssuedAt: clock.Now(), ExpiresAt: clock.Now().Add(24 * time.Hour),
		GraceDays: 7,
	}
	server.licenseNow = clock.Now
	return clock
}

func newSAMLTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	server := New(Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	installSAMLLicense(t, server)
	return server
}

func TestSAMLConfigParsing(t *testing.T) {
	stabilizeConfigEnv(t)
	t.Setenv("ALT8MAIL_SAML_IDP_METADATA_URL", " https://idp.example.test/metadata ")
	t.Setenv("ALT8MAIL_SAML_IDP_METADATA_FILE", " /etc/a8m/saml/idp-metadata.xml ")
	t.Setenv("ALT8MAIL_SAML_SP_ENTITY_ID", " https://mail.example.test/saml/sp ")
	t.Setenv("ALT8MAIL_SAML_SP_CERT_FILE", " /etc/a8m/saml/sp.crt ")
	t.Setenv("ALT8MAIL_SAML_SP_KEY_FILE", " /etc/a8m/saml/sp.key ")
	t.Setenv("ALT8MAIL_SAML_DISPLAY_NAME", " Example SAML ")
	t.Setenv("ALT8MAIL_SAML_EMAIL_DOMAINS", " Example.com, @Example.org ,, example.com, sub.example.test ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.SAMLIDPMetadataURL != "https://idp.example.test/metadata" {
		t.Errorf("SAMLIDPMetadataURL = %q", cfg.SAMLIDPMetadataURL)
	}
	if cfg.SAMLIDPMetadataFile != "/etc/a8m/saml/idp-metadata.xml" {
		t.Errorf("SAMLIDPMetadataFile = %q", cfg.SAMLIDPMetadataFile)
	}
	if cfg.SAMLSPEntityID != "https://mail.example.test/saml/sp" {
		t.Errorf("SAMLSPEntityID = %q", cfg.SAMLSPEntityID)
	}
	if cfg.SAMLSPCertFile != "/etc/a8m/saml/sp.crt" {
		t.Errorf("SAMLSPCertFile = %q", cfg.SAMLSPCertFile)
	}
	if cfg.SAMLSPKeyFile != "/etc/a8m/saml/sp.key" {
		t.Errorf("SAMLSPKeyFile = %q", cfg.SAMLSPKeyFile)
	}
	if cfg.SAMLDisplayName != "Example SAML" {
		t.Errorf("SAMLDisplayName = %q", cfg.SAMLDisplayName)
	}
	wantDomains := []string{"example.com", "example.org", "sub.example.test"}
	if len(cfg.SAMLEmailDomains) != len(wantDomains) {
		t.Fatalf("SAMLEmailDomains = %v, want %v", cfg.SAMLEmailDomains, wantDomains)
	}
	for i, want := range wantDomains {
		if cfg.SAMLEmailDomains[i] != want {
			t.Fatalf("SAMLEmailDomains = %v, want %v", cfg.SAMLEmailDomains, wantDomains)
		}
	}
}

func TestSAMLConfigured(t *testing.T) {
	env := newSAMLTestEnv(t)
	base := env.cfg

	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   bool
	}{
		{name: "complete file metadata", mutate: func(*config.Config) {}, want: true},
		{
			name: "complete metadata URL",
			mutate: func(c *config.Config) {
				c.SAMLIDPMetadataFile = ""
				c.SAMLIDPMetadataURL = "https://idp.example.test/metadata"
			},
			want: true,
		},
		{
			name: "loopback HTTP metadata URL",
			mutate: func(c *config.Config) {
				c.SAMLIDPMetadataFile = ""
				c.SAMLIDPMetadataURL = "http://127.0.0.1:9090/metadata"
			},
			want: true,
		},
		{
			name: "non-loopback HTTP metadata URL",
			mutate: func(c *config.Config) {
				c.SAMLIDPMetadataFile = ""
				c.SAMLIDPMetadataURL = "http://idp.example.test/metadata"
			},
			want: false,
		},
		{
			name: "non-HTTP metadata URL",
			mutate: func(c *config.Config) {
				c.SAMLIDPMetadataFile = ""
				c.SAMLIDPMetadataURL = "ftp://idp.example.test/metadata"
			},
			want: false,
		},
		{
			name: "metadata source missing",
			mutate: func(c *config.Config) {
				c.SAMLIDPMetadataFile = ""
				c.SAMLIDPMetadataURL = ""
			},
			want: false,
		},
		{name: "SP certificate missing", mutate: func(c *config.Config) { c.SAMLSPCertFile = "" }, want: false},
		{name: "SP key missing", mutate: func(c *config.Config) { c.SAMLSPKeyFile = "" }, want: false},
		{
			name:   "public URL missing",
			mutate: func(c *config.Config) { c.PublicURL = "" },
			want:   false,
		},
		{
			name:   "public URL relative",
			mutate: func(c *config.Config) { c.PublicURL = "mail.example.test" },
			want:   false,
		},
		{
			name:   "cleartext non-loopback public URL",
			mutate: func(c *config.Config) { c.PublicURL = "http://mail.example.com" },
			want:   false,
		},
		{
			name:   "cleartext localhost public URL",
			mutate: func(c *config.Config) { c.PublicURL = "http://localhost:8001" },
			want:   true,
		},
		{
			name:   "cleartext loopback IP public URL",
			mutate: func(c *config.Config) { c.PublicURL = "http://127.0.0.1:8001" },
			want:   true,
		},
		{
			name:   "HTTPS public URL",
			mutate: func(c *config.Config) { c.PublicURL = "https://mail.example.com" },
			want:   true,
		},
		{name: "display name optional", mutate: func(c *config.Config) { c.SAMLDisplayName = "" }, want: true},
		{name: "entity ID optional", mutate: func(c *config.Config) { c.SAMLSPEntityID = "" }, want: true},
		{name: "email domains optional", mutate: func(c *config.Config) { c.SAMLEmailDomains = nil }, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			server := &Server{cfg: cfg}
			installSAMLLicense(t, server)
			if got := server.samlConfigured(); got != tc.want {
				t.Fatalf("samlConfigured() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("unlicensed", func(t *testing.T) {
		server := &Server{cfg: base}
		if server.samlConfigured() {
			t.Fatal("samlConfigured() = true without a license")
		}
	})
	t.Run("expired license", func(t *testing.T) {
		server := &Server{cfg: base}
		clock := installSAMLLicense(t, server)
		clock.Set(server.license.ExpiresAt.Add(time.Duration(server.license.GraceDays+1) * 24 * time.Hour))
		if server.samlConfigured() {
			t.Fatal("samlConfigured() = true with an expired license")
		}
	})
	t.Run("license without saml feature", func(t *testing.T) {
		server := &Server{cfg: base}
		installSAMLLicense(t, server)
		server.license.Features = []string{"sso", "scim"}
		if server.samlConfigured() {
			t.Fatal("samlConfigured() = true without the saml feature")
		}
	})
	t.Run("grace license keeps saml enabled", func(t *testing.T) {
		server := &Server{cfg: base}
		clock := installSAMLLicense(t, server)
		clock.Set(server.license.ExpiresAt.Add(24 * time.Hour))
		if !server.samlConfigured() {
			t.Fatal("samlConfigured() = false during the grace period")
		}
	})
}

func TestSAMLConfigEndpoint(t *testing.T) {
	env := newSAMLTestEnv(t)
	cases := []struct {
		name        string
		cfg         config.Config
		withLicense bool
		wantEnabled bool
		wantName    string
	}{
		{
			name:        "disabled without configuration",
			cfg:         config.Config{},
			wantEnabled: false,
			wantName:    "",
		},
		{
			name:        "disabled with configuration but no license",
			cfg:         env.cfg,
			wantEnabled: false,
			wantName:    "Example SAML",
		},
		{
			name:        "enabled with configuration and saml license",
			cfg:         env.cfg,
			withLicense: true,
			wantEnabled: true,
			wantName:    "Example SAML",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := New(Options{
				Config: tc.cfg,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if tc.withLicense {
				installSAMLLicense(t, server)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/auth/saml/config", nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			var payload struct {
				Enabled     bool   `json:"enabled"`
				DisplayName string `json:"displayName"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", payload.Enabled, tc.wantEnabled)
			}
			if payload.DisplayName != tc.wantName {
				t.Fatalf("displayName = %q, want %q", payload.DisplayName, tc.wantName)
			}
		})
	}
}

func TestSAMLMetadataEndpoint(t *testing.T) {
	env := newSAMLTestEnv(t)

	t.Run("configured entity ID", func(t *testing.T) {
		cfg := env.cfg
		cfg.SAMLSPEntityID = "https://mail.example.test/saml/sp"
		server := newSAMLTestServer(t, cfg)

		request := httptest.NewRequest(http.MethodGet, samlMetadataPath, nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		contentType := recorder.Header().Get("Content-Type")
		if contentType != "application/samlmetadata+xml" {
			t.Fatalf("Content-Type = %q, want application/samlmetadata+xml", contentType)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `entityID="https://mail.example.test/saml/sp"`) {
			t.Fatalf("metadata does not contain the configured entity ID:\n%s", body)
		}
		if !strings.Contains(body, `Location="https://mail.example.test/api/auth/saml/acs"`) {
			t.Fatalf("metadata does not contain the public ACS URL:\n%s", body)
		}
		if !strings.Contains(body, "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST") {
			t.Fatalf("metadata does not advertise the HTTP-POST ACS binding:\n%s", body)
		}
		if strings.Contains(body, "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Artifact") {
			t.Fatalf("metadata advertises an unsupported artifact ACS binding:\n%s", body)
		}
		if !strings.Contains(body, `AuthnRequestsSigned="true"`) {
			t.Fatalf("metadata does not require signed AuthnRequests:\n%s", body)
		}
		if !strings.Contains(body, `WantAssertionsSigned="true"`) {
			t.Fatalf("metadata does not require signed assertions:\n%s", body)
		}
		if !strings.Contains(body, `use="signing"`) {
			t.Fatalf("metadata has no signing KeyDescriptor:\n%s", body)
		}
		if !strings.Contains(body, base64.StdEncoding.EncodeToString(env.material.certDER)) {
			t.Fatalf("signing KeyDescriptor does not contain the SP certificate:\n%s", body)
		}
	})

	t.Run("default entity ID is the metadata URL", func(t *testing.T) {
		server := newSAMLTestServer(t, env.cfg)
		request := httptest.NewRequest(http.MethodGet, samlMetadataPath, nil)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `entityID="https://mail.example.test/api/auth/saml/metadata"`) {
			t.Fatalf("metadata does not use the metadata URL as the default entity ID:\n%s", recorder.Body.String())
		}
	})
}

func TestSAMLMetadataEndpointRequiresEntitlement(t *testing.T) {
	env := newSAMLTestEnv(t)
	cases := []struct {
		name      string
		configure func(t *testing.T, server *Server)
	}{
		{
			name:      "unlicensed",
			configure: func(*testing.T, *Server) {},
		},
		{
			name: "expired license",
			configure: func(t *testing.T, server *Server) {
				clock := installSAMLLicense(t, server)
				clock.Set(server.license.ExpiresAt.Add(time.Duration(server.license.GraceDays+1) * 24 * time.Hour))
			},
		},
		{
			name: "license without saml feature",
			configure: func(t *testing.T, server *Server) {
				installSAMLLicense(t, server)
				server.license.Features = []string{"sso"}
			},
		},
		{
			name:      "not configured",
			configure: func(*testing.T, *Server) {},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.cfg
			if tc.name == "not configured" {
				cfg = config.Config{}
			}
			server := New(Options{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			tc.configure(t, server)
			request := httptest.NewRequest(http.MethodGet, samlMetadataPath, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
			}
			if server.samlCached != nil {
				t.Fatal("metadata endpoint built the SAML middleware while disabled")
			}
		})
	}
}

func TestSAMLMetadataURLRejected(t *testing.T) {
	env := newSAMLTestEnv(t)
	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = ""
	cfg.SAMLIDPMetadataURL = "http://idp.example.test/metadata"
	server := &Server{cfg: cfg}
	installSAMLLicense(t, server)

	if server.samlConfigured() {
		t.Fatal("samlConfigured() accepted a non-loopback HTTP metadata URL")
	}
	if _, err := server.samlMiddleware(context.Background()); err == nil {
		t.Fatal("samlMiddleware() accepted a non-loopback HTTP metadata URL")
	}
}

func TestSAMLLoopbackMetadataURLs(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "https://idp.example.test/metadata", want: true},
		{raw: "http://127.0.0.1:8080/metadata", want: true},
		{raw: "http://127.10.20.30/metadata", want: true},
		{raw: "http://localhost:8080/metadata", want: true},
		{raw: "http://[::1]:8080/metadata", want: true},
		{raw: "http://localhost.evil.example/metadata", want: false},
		{raw: "http://idp.example.test/metadata", want: false},
		{raw: "ftp://127.0.0.1/metadata", want: false},
		{raw: "https:///metadata", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			endpoint, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if got := samlMetadataURLAllowed(endpoint); got != tc.want {
				t.Fatalf("samlMetadataURLAllowed(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
	if samlMetadataURLAllowed(nil) {
		t.Fatal("samlMetadataURLAllowed(nil) = true")
	}
}

func TestSAMLMetadataRedirectPolicy(t *testing.T) {
	cases := []struct {
		name     string
		original string
		target   string
		wantErr  bool
	}{
		{name: "HTTPS to HTTPS", original: "https://idp.example.test/a", target: "https://idp.example.test/b"},
		{name: "HTTPS downgrade to loopback HTTP", original: "https://idp.example.test/a", target: "http://127.0.0.1/metadata", wantErr: true},
		{name: "HTTPS downgrade to public HTTP", original: "https://idp.example.test/a", target: "http://idp.example.test/b", wantErr: true},
		{name: "loopback HTTP to loopback HTTP", original: "http://127.0.0.1/a", target: "http://localhost/b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := httptest.NewRequest(http.MethodGet, tc.original, nil)
			target := httptest.NewRequest(http.MethodGet, tc.target, nil)
			err := defaultSAMLMetadataHTTPClient.CheckRedirect(target, []*http.Request{original})
			if tc.wantErr != (err != nil) {
				t.Fatalf("CheckRedirect(%q -> %q) error = %v, wantErr %v", tc.original, tc.target, err, tc.wantErr)
			}
		})
	}
}

func TestSAMLOnErrorSanitized(t *testing.T) {
	env := newSAMLTestEnv(t)
	var logs bytes.Buffer
	server := New(Options{
		Config: env.cfg,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	installSAMLLicense(t, server)
	middleware, err := server.samlMiddleware(context.Background())
	if err != nil {
		t.Fatalf("samlMiddleware() error = %v", err)
	}

	const marker = "SECRET-SAML-ASSERTION-MARKER"
	request := httptest.NewRequest(http.MethodPost, samlACSPath, strings.NewReader(marker))
	recorder := httptest.NewRecorder()
	middleware.OnError(recorder, request, fmt.Errorf("parse SAMLResponse: %s", marker))

	// Task 2 makes the sanitized handler redirect to the login page instead of
	// answering with a bare 400.
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	if location := recorder.Header().Get("Location"); location != "/?saml_error=1" {
		t.Fatalf("error redirect = %q, want /?saml_error=1", location)
	}
	if strings.Contains(recorder.Body.String(), marker) {
		t.Fatalf("error response leaked the error text: %s", recorder.Body.String())
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatalf("logs leaked the error text: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "SAML request failed") {
		t.Fatalf("logs do not contain the fixed SAML failure message: %s", logs.String())
	}
	if !strings.Contains(logs.String(), samlACSPath) {
		t.Fatalf("logs do not contain the request path: %s", logs.String())
	}
}

func TestSAMLMetadataFilePrecedenceWarning(t *testing.T) {
	env := newSAMLTestEnv(t)
	cfg := env.cfg
	cfg.SAMLIDPMetadataURL = "https://idp.example.test/metadata"
	var logs bytes.Buffer
	server := New(Options{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	installSAMLLicense(t, server)
	if _, err := server.samlMiddleware(context.Background()); err != nil {
		t.Fatalf("samlMiddleware() error = %v", err)
	}
	if !strings.Contains(logs.String(), "takes precedence") {
		t.Fatalf("logs do not warn that the metadata file takes precedence: %s", logs.String())
	}
}

func TestSAMLMetadataURLLoading(t *testing.T) {
	env := newSAMLTestEnv(t)
	metadata := env.metadata
	var hits int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(metadata)
	}))
	defer idp.Close()

	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = ""
	cfg.SAMLIDPMetadataURL = idp.URL + "/metadata"
	server := newSAMLTestServer(t, cfg)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("server construction fetched metadata %d times, want 0", got)
	}

	// The public config endpoint must report the capability without contacting
	// the identity provider.
	request := httptest.NewRequest(http.MethodGet, "/api/auth/saml/config", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("config endpoint fetched metadata %d times, want 0", got)
	}

	for attempt := 0; attempt < 2; attempt++ {
		middleware, err := server.samlMiddleware(context.Background())
		if err != nil {
			t.Fatalf("samlMiddleware() error = %v", err)
		}
		if middleware == nil {
			t.Fatal("samlMiddleware() returned nil middleware")
		}
		if middleware.ServiceProvider.EntityID != "https://mail.example.test/api/auth/saml/metadata" {
			t.Fatalf("EntityID = %q", middleware.ServiceProvider.EntityID)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("metadata was fetched %d times, want 1 (cached)", got)
	}

	// Serving SP metadata uses the cached middleware and must not refetch.
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, samlMetadataPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("metadata endpoint refetched IdP metadata: %d hits", got)
	}
}

func TestSAMLMetadataHTTPSLoading(t *testing.T) {
	env := newSAMLTestEnv(t)
	metadata := env.metadata
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(metadata)
	}))
	defer idp.Close()

	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = ""
	cfg.SAMLIDPMetadataURL = idp.URL + "/metadata"
	server := newSAMLTestServer(t, cfg)
	server.samlMetadataClient = idp.Client()

	middleware, err := server.samlMiddleware(context.Background())
	if err != nil {
		t.Fatalf("samlMiddleware() error = %v", err)
	}
	if middleware == nil {
		t.Fatal("samlMiddleware() returned nil middleware")
	}
}

func TestSAMLMetadataRetryBackoff(t *testing.T) {
	env := newSAMLTestEnv(t)
	metadata := env.metadata
	var hits int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			http.Error(w, "temporarily unavailable", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(metadata)
	}))
	defer idp.Close()

	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = ""
	cfg.SAMLIDPMetadataURL = idp.URL + "/metadata"
	server := newSAMLTestServer(t, cfg)

	if _, err := server.samlMiddleware(context.Background()); err == nil {
		t.Fatal("samlMiddleware() succeeded on a failing metadata response")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("metadata fetch count = %d, want 1", got)
	}
	// A second attempt inside the backoff window must not hit the IdP again.
	if _, err := server.samlMiddleware(context.Background()); err == nil {
		t.Fatal("samlMiddleware() succeeded during the retry backoff")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("metadata fetch count during backoff = %d, want 1", got)
	}

	// Rewind the last-attempt clock past the backoff and retry.
	server.samlLastTry = time.Now().Add(-samlDiscoveryRetryBackoff - time.Second)
	middleware, err := server.samlMiddleware(context.Background())
	if err != nil {
		t.Fatalf("samlMiddleware() after backoff error = %v", err)
	}
	if middleware == nil {
		t.Fatal("samlMiddleware() after backoff returned nil middleware")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("metadata fetch count after backoff = %d, want 2", got)
	}
	// The successful result is cached.
	if _, err := server.samlMiddleware(context.Background()); err != nil {
		t.Fatalf("cached samlMiddleware() error = %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("metadata fetch count after success = %d, want 2", got)
	}
}

func TestSAMLMetadataParseFailure(t *testing.T) {
	env := newSAMLTestEnv(t)
	var hits int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("<not-saml-metadata>"))
	}))
	defer idp.Close()

	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = ""
	cfg.SAMLIDPMetadataURL = idp.URL + "/metadata"
	server := newSAMLTestServer(t, cfg)

	if _, err := server.samlMiddleware(context.Background()); err == nil {
		t.Fatal("samlMiddleware() accepted malformed IdP metadata")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("metadata fetch count = %d, want 1", got)
	}
	request := httptest.NewRequest(http.MethodGet, samlMetadataPath, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("metadata status = %d, want %d; body: %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestSAMLMetadataRequiresIDPDescriptor(t *testing.T) {
	env := newSAMLTestEnv(t)
	dir := t.TempDir()
	notAnIDP := filepath.Join(dir, "sp-metadata.xml")
	document := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://sp.example.test/metadata">
  <md:SPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"/>
</md:EntityDescriptor>`)
	if err := os.WriteFile(notAnIDP, document, 0o600); err != nil {
		t.Fatalf("write SP metadata: %v", err)
	}
	cfg := env.cfg
	cfg.SAMLIDPMetadataFile = notAnIDP
	server := newSAMLTestServer(t, cfg)

	if _, err := server.samlMiddleware(context.Background()); err == nil {
		t.Fatal("samlMiddleware() accepted metadata without an IDPSSODescriptor")
	}
}

func TestSAMLSPKeyPairValidation(t *testing.T) {
	env := newSAMLTestEnv(t)
	dir := t.TempDir()

	if _, _, err := loadSAMLSPKeyPair("", ""); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted empty paths")
	}

	badPEM := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badPEM, []byte("not a pem block"), 0o600); err != nil {
		t.Fatalf("write bad PEM: %v", err)
	}
	if _, _, err := loadSAMLSPKeyPair(badPEM, badPEM); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted non-PEM files")
	}

	other := newSAMLTestMaterial(t)
	mismatchCert := filepath.Join(dir, "mismatch.crt")
	if err := os.WriteFile(mismatchCert, other.certPEM, 0o600); err != nil {
		t.Fatalf("write mismatched certificate: %v", err)
	}
	if _, _, err := loadSAMLSPKeyPair(mismatchCert, env.keyFile); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted a certificate that does not match the key")
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("marshal EC key: %v", err)
	}
	ecFile := filepath.Join(dir, "sp-ec.key")
	if err := os.WriteFile(ecFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}), 0o600); err != nil {
		t.Fatalf("write EC key: %v", err)
	}
	if _, _, err := loadSAMLSPKeyPair(env.certFile, ecFile); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted a non-RSA key")
	}

	weakCert, weakKey := writeSAMLTestPair(t,
		newSAMLTestMaterialFor(t, 1024, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)), "weak")
	if _, _, err := loadSAMLSPKeyPair(weakCert, weakKey); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted a 1024-bit RSA key")
	}

	expiredCert, expiredKey := writeSAMLTestPair(t,
		newSAMLTestMaterialFor(t, 2048, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)), "expired")
	if _, _, err := loadSAMLSPKeyPair(expiredCert, expiredKey); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted an expired certificate")
	}

	futureCert, futureKey := writeSAMLTestPair(t,
		newSAMLTestMaterialFor(t, 2048, time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour)), "future")
	if _, _, err := loadSAMLSPKeyPair(futureCert, futureKey); err == nil {
		t.Fatal("loadSAMLSPKeyPair() accepted a not-yet-valid certificate")
	}

	key, cert, err := loadSAMLSPKeyPair(env.certFile, env.keyFile)
	if err != nil {
		t.Fatalf("loadSAMLSPKeyPair() error = %v", err)
	}
	if key == nil || cert == nil {
		t.Fatal("loadSAMLSPKeyPair() returned nil key or certificate")
	}
}

func TestSAMLConfigParsingEmailAttributeAndMFAContexts(t *testing.T) {
	stabilizeConfigEnv(t)
	t.Setenv("ALT8MAIL_SAML_EMAIL_ATTRIBUTE", "  urn:oid:0.9.2342.19200300.100.1.3  ")
	t.Setenv("ALT8MAIL_SAML_MFA_AUTHN_CONTEXT_CLASSES", " urn:example:acme:mfa ,, urn:Example:CaseSensitive , urn:example:acme:mfa , urn:example:acme:second ")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.SAMLEmailAttribute != "urn:oid:0.9.2342.19200300.100.1.3" {
		t.Fatalf("SAMLEmailAttribute = %q, want trimmed attribute name", cfg.SAMLEmailAttribute)
	}
	wantContexts := []string{"urn:example:acme:mfa", "urn:Example:CaseSensitive", "urn:example:acme:second"}
	if len(cfg.SAMLMFAContexts) != len(wantContexts) {
		t.Fatalf("SAMLMFAContexts = %v, want %v", cfg.SAMLMFAContexts, wantContexts)
	}
	for i, want := range wantContexts {
		if cfg.SAMLMFAContexts[i] != want {
			t.Fatalf("SAMLMFAContexts = %v, want %v (case must be preserved)", cfg.SAMLMFAContexts, wantContexts)
		}
	}

	t.Run("unset is empty", func(t *testing.T) {
		stabilizeConfigEnv(t)
		t.Setenv("ALT8MAIL_SAML_EMAIL_ATTRIBUTE", "")
		t.Setenv("ALT8MAIL_SAML_MFA_AUTHN_CONTEXT_CLASSES", "")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load() error = %v", err)
		}
		if cfg.SAMLEmailAttribute != "" {
			t.Fatalf("SAMLEmailAttribute = %q, want empty", cfg.SAMLEmailAttribute)
		}
		if len(cfg.SAMLMFAContexts) != 0 {
			t.Fatalf("SAMLMFAContexts = %v, want empty", cfg.SAMLMFAContexts)
		}
	})
}
