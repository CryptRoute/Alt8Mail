package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

// loadLicense reads and verifies the optional offline license file. A missing
// file is a normal OSS deployment; an unreadable or invalid file is reported
// to the caller so startup can continue with paid features disabled.
func loadLicense(cfg config.Config, logger *slog.Logger) (*licensing.License, error) {
	if strings.TrimSpace(cfg.LicenseFile) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(cfg.LicenseFile)
	if err != nil {
		return nil, fmt.Errorf("read license file: %w", err)
	}
	publicKey, err := licensing.ParsePublicKey(cfg.LicensePublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse license public key: %w", err)
	}
	license, err := (licensing.Verifier{PublicKey: publicKey}).Verify(string(raw))
	if err != nil {
		return nil, fmt.Errorf("verify license: %w", err)
	}
	if logger != nil {
		status := license.Status(time.Now())
		logger.Info("license loaded", "edition", license.Edition, "state", status.State,
			"seats", status.SeatLimit, "daysRemaining", status.DaysRemaining)
	}
	return &license, nil
}

// licenseTime is the clock used to evaluate the license lifecycle. Production
// uses time.Now; tests can inject a clock to move through valid, grace and
// expired without waiting.
func (s *Server) licenseTime() time.Time {
	if s.licenseNow != nil {
		return s.licenseNow()
	}
	return time.Now()
}

// licenseSnapshot returns the non-secret entitlement view exposed to the
// webmail and Control Plane. Raw tokens, signatures and private material never
// leave this process. The lifecycle is evaluated against the current clock on
// every call, so a long-running server transitions through valid, grace and
// expired without restarting.
func (s *Server) licenseSnapshot() map[string]any {
	if s.license == nil {
		state := "unlicensed"
		if s.licenseConfigured {
			state = "invalid"
		}
		return map[string]any{
			"edition": "oss", "state": state, "inGrace": false, "expired": false,
			"seats": 0, "features": []string{}, "daysRemaining": 0, "expiresAt": "",
		}
	}
	status := s.license.Status(s.licenseTime())
	features := append([]string(nil), status.Features...)
	if status.Expired || features == nil {
		// An expired license must not advertise usable paid features.
		features = []string{}
	}
	return map[string]any{
		"edition": s.license.Edition, "state": status.State,
		"inGrace": status.InGrace, "expired": status.Expired,
		"seats": status.SeatLimit, "features": features,
		"daysRemaining": status.DaysRemaining,
		"expiresAt":     status.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// entitled reports whether a verified, non-expired license includes a paid
// feature. Grace-period licenses remain usable; expired licenses do not.
func (s *Server) entitled(feature string) bool {
	if s.license == nil {
		return false
	}
	if s.license.Status(s.licenseTime()).Expired {
		return false
	}
	return s.license.HasFeature(feature)
}

func (s *Server) getEntitlements(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.licenseSnapshot())
}
