package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

// officeRuntime is the server-side Office state. Secret is never serialized.
type officeRuntime struct {
	Enabled          bool
	Ready            bool
	SecretConfigured bool
	PublicURL        string
	Status           string
	Version          string
	LastHealthAt     *time.Time
	Secret           string
}

func (s *Server) officeRuntime(ctx context.Context) (officeRuntime, error) {
	runtime := officeRuntime{Status: "not_installed"}
	err := s.store.DB.QueryRow(ctx, `
		SELECT enabled, public_url, status, version, last_health_at
		FROM public.office_settings WHERE id = 'default'
	`).Scan(&runtime.Enabled, &runtime.PublicURL, &runtime.Status, &runtime.Version, &runtime.LastHealthAt)
	if err != nil {
		return runtime, err
	}
	if path := strings.TrimSpace(s.cfg.OfficeJWTSecretFile); path != "" {
		if data, readErr := os.ReadFile(path); readErr == nil {
			secret := strings.TrimSpace(string(data))
			if len(secret) >= 32 {
				runtime.Secret = secret
				runtime.SecretConfigured = true
			}
		}
	}
	runtime.PublicURL = strings.TrimRight(strings.TrimSpace(runtime.PublicURL), "/")
	healthy := runtime.LastHealthAt != nil && time.Since(*runtime.LastHealthAt) < 2*time.Minute
	runtime.Ready = runtime.Enabled &&
		runtime.Status == "running" &&
		healthy &&
		runtime.SecretConfigured &&
		validOfficeBaseURL(s.cfg.PublicURL) &&
		validOfficeServerURL(runtime.PublicURL)
	return runtime, nil
}

func (s *Server) officeSigningSecret() string {
	path := strings.TrimSpace(s.cfg.OfficeJWTSecretFile)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	secret := strings.TrimSpace(string(data))
	if len(secret) < 32 {
		return ""
	}
	return secret
}

func (s *Server) officeReady(w http.ResponseWriter, r *http.Request, user userRecord) (officeRuntime, bool) {
	if _, allowed := s.officeUserAllowed(r.Context(), user); !allowed {
		writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Office is not enabled for your account by organization policy")
		return officeRuntime{}, false
	}
	runtime, err := s.officeRuntime(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not load the Office feature state")
		return runtime, false
	}
	if !runtime.Ready {
		writeError(w, http.StatusConflict, "OFFICE_NOT_READY", "Office is not enabled, healthy and reachable yet")
		return runtime, false
	}
	return runtime, true
}

func (s *Server) officePublicBase() string {
	return strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/")
}

// officeConfig exposes only the browser-safe Office feature state. The JWT
// secret and the Office agent credentials never leave the server.
func (s *Server) officeConfig(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	runtime, err := s.officeRuntime(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "ready": false, "publicUrl": "", "status": "not_installed",
			"version": "", "secretConfigured": false, "mode": "editor",
			"formats": office.SupportedPayload(),
		})
		return
	}
	policy, allowed := s.officeUserAllowed(r.Context(), sess.User)
	enabled := runtime.Enabled && allowed
	ready := runtime.Ready && allowed
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "ready": ready, "publicUrl": runtime.PublicURL,
		"status": runtime.Status, "version": runtime.Version,
		"secretConfigured": runtime.SecretConfigured, "mode": "editor",
		"formats": office.SupportedPayload(),
		"policy": map[string]any{
			"internalSharing": policy.InternalSharing, "ai": policy.AI,
			"dlpOnSave": policy.DLPOnSave, "versionRetention": policy.VersionRetention,
		},
	})
}

func validOfficeBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == ""
}

func validOfficeServerURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return false
	}
	return strings.Contains(parsed.Hostname(), ".")
}
