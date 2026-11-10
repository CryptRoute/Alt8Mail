package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

func TestPaidFeatureGates(t *testing.T) {
	server := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := featureGate(server, FeatureFollowUps, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/api/followups", nil))
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "LICENSE_FEATURE") {
		t.Fatalf("unlicensed gate = %d %s", recorder.Code, recorder.Body.String())
	}
	server.license = &licensing.License{
		Version: licensing.Version, ID: "specific", Edition: "enterprise", Seats: 1,
		Features: []string{FeatureAITriage, FeatureBYOK}, ExpiresAt: time.Now().Add(time.Hour),
	}
	recorder = httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/api/followups", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("specific license unexpectedly granted follow-ups: %d", recorder.Code)
	}
	if !server.entitled(FeatureAITriage) || !server.entitled(FeatureBYOK) || server.entitled(FeatureFollowUps) {
		t.Fatal("specific feature grant is wrong")
	}
	server.license = &licensing.License{
		Version: licensing.Version, ID: "all", Edition: "enterprise", Seats: 1,
		Features: []string{"all"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	recorder = httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/api/followups", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("wildcard license gate = %d %s", recorder.Code, recorder.Body.String())
	}
	if missing := server.missingPaidFeatures(); len(missing) != 0 {
		t.Fatalf("wildcard license missing features = %#v", missing)
	}
}

func TestExpiredLicenseBlocksAllPaidFeatures(t *testing.T) {
	server := &Server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		license: &licensing.License{
			Version: licensing.Version, ID: "expired", Edition: "enterprise", Seats: 1,
			Features: []string{"all"}, ExpiresAt: time.Now().Add(-48 * time.Hour),
		},
	}
	if server.entitled(FeatureFollowUps) {
		t.Fatal("expired license granted a paid feature")
	}
	if missing := server.missingPaidFeatures(); len(missing) != len(paidFeatureKeys) {
		t.Fatalf("expired missing features = %d", len(missing))
	}
}
