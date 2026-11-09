package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	// oidcDiscoveryTimeout bounds a single provider discovery request. The
	// login page never waits on it: only the Task 2 start/callback handlers do.
	oidcDiscoveryTimeout = 10 * time.Second
	// oidcDiscoveryRetryBackoff keeps a temporarily unreachable identity
	// provider from being hammered on every sign-in attempt while still letting
	// the next attempt succeed after the provider recovers.
	oidcDiscoveryRetryBackoff = 30 * time.Second
)

var errOIDCNotConfigured = errors.New("OIDC SSO is not configured")

// oidcClient bundles lazily discovered provider metadata with the OAuth2 client
// used by the Task 2 start and callback handlers. Discovery is performed on the
// first login-flow request, never during server construction.
type oidcClient struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	config   *oauth2.Config
}

// oidcConfigured reports whether every required OIDC setting is present. An
// incomplete configuration disables SSO entirely, leaving password and passkey
// sign-in untouched.
func (s *Server) oidcConfigured() bool {
	cfg := s.cfg
	return strings.TrimSpace(cfg.OIDCIssuer) != "" &&
		strings.TrimSpace(cfg.OIDCClientID) != "" &&
		strings.TrimSpace(cfg.OIDCClientSecret) != "" &&
		strings.TrimSpace(cfg.OIDCRedirectURL) != ""
}

// oidcDisplayName is the label the login page shows on the SSO button.
func (s *Server) oidcDisplayName() string {
	return strings.TrimSpace(s.cfg.OIDCDisplayName)
}

// oidcClient returns the lazily discovered OIDC provider, ID-token verifier and
// OAuth2 client configuration. Discovery runs on demand so an unreachable
// identity provider can never block server startup or the public login page.
// A failed discovery is cached briefly and then retried.
func (s *Server) oidcClient(ctx context.Context) (*oidcClient, error) {
	if !s.oidcConfigured() {
		return nil, errOIDCNotConfigured
	}
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidcCached != nil {
		return s.oidcCached, nil
	}
	if !s.oidcLastTry.IsZero() && time.Since(s.oidcLastTry) < oidcDiscoveryRetryBackoff {
		if s.oidcLastErr != nil {
			return nil, fmt.Errorf("OIDC discovery failed recently: %w", s.oidcLastErr)
		}
		return nil, errors.New("OIDC discovery is cooling down")
	}
	s.oidcLastTry = time.Now()
	issuer := strings.TrimRight(strings.TrimSpace(s.cfg.OIDCIssuer), "/")
	discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		s.oidcLastErr = err
		if s.logger != nil {
			s.logger.Warn("OIDC provider discovery failed", "issuer", issuer, "err", err)
		}
		return nil, fmt.Errorf("discover OIDC provider %q: %w", issuer, err)
	}
	s.oidcLastErr = nil
	s.oidcCached = &oidcClient{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: s.cfg.OIDCClientID}),
		config: &oauth2.Config{
			ClientID:     s.cfg.OIDCClientID,
			ClientSecret: s.cfg.OIDCClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  s.cfg.OIDCRedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
	}
	return s.oidcCached, nil
}

// oidcConfig is the public, unauthenticated capability probe used by the login
// page. It reports configuration only and never contacts the identity provider.
func (s *Server) oidcConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.oidcConfigured(),
		"displayName": s.oidcDisplayName(),
	})
}
