package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

func TestReportLicenseUsage(t *testing.T) {
	var calls int32
	var got map[string]any
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		authorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	now := time.Now()
	s := &Server{
		license: &licensing.License{
			Version: licensing.Version, ID: "lic_usage", DeploymentID: "dep_usage",
			Edition: "enterprise", Plan: "enterprise", Seats: 10,
			Features: []string{"all"}, IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
		},
		version: "3.9.1",
		cfg: config.Config{
			LicenseUsageURL: server.URL, LicenseUsageToken: "usage-token",
		},
	}
	s.reportLicenseUsage(context.Background())
	if calls != 1 {
		t.Fatalf("usage calls = %d", calls)
	}
	if authorization != "Bearer usage-token" {
		t.Fatalf("authorization = %q", authorization)
	}
	if got["deploymentId"] != "dep_usage" || got["licenseId"] != "lic_usage" || got["version"] != "3.9.1" {
		t.Fatalf("payload = %#v", got)
	}
	if got["licensedSeats"] != float64(10) || got["activeSeats"] != float64(0) {
		t.Fatalf("seat payload = %#v", got)
	}
	counts, ok := got["featureCounts"].(map[string]any)
	if !ok || counts["sso"] != float64(1) {
		t.Fatalf("feature counts = %#v", got["featureCounts"])
	}

	s.license.DeploymentID = ""
	s.reportLicenseUsage(context.Background())
	if calls != 1 {
		t.Fatalf("usage without deploymentId made a request: calls=%d", calls)
	}
}
