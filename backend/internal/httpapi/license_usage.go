package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

// licenseUsageWorker sends the optional privacy-bounded heartbeat. It never
// includes mail, contacts, subjects or message content.
func (s *Server) licenseUsageWorker(ctx context.Context) {
	if !s.licenseUsageConfigured() {
		return
	}
	s.reportLicenseUsage(ctx)
	interval := s.cfg.LicenseUsageInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reportLicenseUsage(ctx)
		}
	}
}

func (s *Server) licenseUsageConfigured() bool {
	return strings.TrimSpace(s.cfg.LicenseUsageURL) != "" && strings.TrimSpace(s.cfg.LicenseUsageToken) != ""
}

func (s *Server) reportLicenseUsage(ctx context.Context) {
	if !s.licenseUsageConfigured() || s.license == nil {
		return
	}
	deploymentID := strings.TrimSpace(s.license.DeploymentID)
	if deploymentID == "" {
		if s.logger != nil {
			s.logger.Debug("license usage heartbeat skipped: license has no deploymentId")
		}
		return
	}
	status := s.license.Status(s.licenseTime())
	payload := map[string]any{
		"deploymentId":  deploymentID,
		"licenseId":     s.license.ID,
		"version":       s.version,
		"activeSeats":   s.licenseSeatUsageCount(ctx),
		"licensedSeats": status.SeatLimit,
		"featureCounts": licenseFeatureCounts(status.Features),
		"reportedAt":    time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.LicenseUsageURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.cfg.LicenseUsageToken)
	request.Header.Set("User-Agent", "Alt8Mail-Usage/1.0")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("license usage heartbeat failed", "err", err)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if s.logger != nil {
			s.logger.Warn("license usage heartbeat rejected", "status", response.StatusCode)
		}
		return
	}
	if s.logger != nil {
		s.logger.Debug("license usage heartbeat sent", "deploymentId", deploymentID, "activeSeats", payload["activeSeats"])
	}
}

func licenseFeatureCounts(features []string) map[string]int {
	counts := map[string]int{}
	add := func(key string) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || len(counts) >= 100 {
			return
		}
		counts[key] = 1
	}
	for _, feature := range features {
		if strings.EqualFold(strings.TrimSpace(feature), "all") || strings.TrimSpace(feature) == "*" {
			for _, key := range licensing.PaidFeatureKeys {
				add(key)
			}
			continue
		}
		add(feature)
	}
	return counts
}
