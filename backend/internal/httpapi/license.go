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
func loadLicense(cfg config.Config, logger *slog.Logger) (*licensing.License, licensing.Status, error) {
	if strings.TrimSpace(cfg.LicenseFile) == "" {
		return nil, licensing.Status{}, nil
	}
	raw, err := os.ReadFile(cfg.LicenseFile)
	if err != nil {
		return nil, licensing.Status{}, fmt.Errorf("read license file: %w", err)
	}
	publicKey, err := licensing.ParsePublicKey(cfg.LicensePublicKey)
	if err != nil {
		return nil, licensing.Status{}, fmt.Errorf("parse license public key: %w", err)
	}
	license, err := (licensing.Verifier{PublicKey: publicKey}).Verify(string(raw))
	if err != nil {
		return nil, licensing.Status{}, fmt.Errorf("verify license: %w", err)
	}
	status := license.Status(time.Now())
	if logger != nil {
		logger.Info("license loaded", "edition", license.Edition, "state", status.State,
			"seats", status.SeatLimit, "daysRemaining", status.DaysRemaining)
	}
	return &license, status, nil
}

// licenseSnapshot returns the non-secret entitlement view exposed to the
// webmail and Control Plane. Raw tokens, signatures and private material never
// leave this process.
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
	features := append([]string(nil), s.licenseStatus.Features...)
	if features == nil {
		features = []string{}
	}
	return map[string]any{
		"edition": s.license.Edition, "state": s.licenseStatus.State,
		"inGrace": s.licenseStatus.InGrace, "expired": s.licenseStatus.Expired,
		"seats": s.licenseStatus.SeatLimit, "features": features,
		"daysRemaining": s.licenseStatus.DaysRemaining,
		"expiresAt":     s.licenseStatus.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// entitled reports whether a verified, non-expired license includes a paid
// feature. Grace-period licenses remain usable; expired licenses do not.
func (s *Server) entitled(feature string) bool {
	if s.license == nil || s.licenseStatus.Expired {
		return false
	}
	return s.license.HasFeature(feature)
}

func (s *Server) getEntitlements(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.licenseSnapshot())
}
