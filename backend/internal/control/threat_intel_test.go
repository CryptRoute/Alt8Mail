package control

import (
	"context"
	"reflect"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/threatintel"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeThreatSource struct {
	reports []threatintel.ReportIndicators
}

func (f fakeThreatSource) ConfirmedIndicators(context.Context) ([]threatintel.ReportIndicators, error) {
	return f.reports, nil
}

func TestSyncThreatIndicatorsRebuildsFromConfirmedReports(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client.Close()
	ctx := context.Background()
	reports := []threatintel.ReportIndicators{
		{Sender: "alice@example.com", Domain: "example.com", URLHosts: []string{"shared.example", "one.example"}},
		{Sender: "bob@example.net", Domain: "example.net", URLHosts: []string{"shared.example"}},
	}
	if err := syncThreatIndicators(ctx, fakeThreatSource{reports: reports}, true, client); err != nil {
		t.Fatalf("enable sync: %v", err)
	}
	assertThreatHash(t, client, threatintel.URLMap, map[string]string{
		"one.example": "1", "shared.example": "1",
	})

	// Dismissing one confirmation must rebuild, not delete a shared host that
	// the second confirmed report still contributes.
	if err := syncThreatIndicators(ctx, fakeThreatSource{reports: reports[1:]}, true, client); err != nil {
		t.Fatalf("dismissal sync: %v", err)
	}
	assertThreatHash(t, client, threatintel.SenderMap, map[string]string{"bob@example.net": "1"})
	assertThreatHash(t, client, threatintel.URLMap, map[string]string{"shared.example": "1"})

	// Disabled sharing performs no Redis writes as part of a normal sync.
	if err := syncThreatIndicators(ctx, fakeThreatSource{reports: reports}, false, client); err != nil {
		t.Fatalf("disabled sync: %v", err)
	}
	assertThreatHash(t, client, threatintel.SenderMap, map[string]string{"bob@example.net": "1"})
}

func TestSanitizeURLHosts(t *testing.T) {
	got := sanitizeURLHosts([]string{" Example.COM. ", "example.com", "", "other.example"})
	want := []string{"example.com", "other.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeURLHosts = %#v, want %#v", got, want)
	}
}

func assertThreatHash(t *testing.T, client *redis.Client, key string, want map[string]string) {
	t.Helper()
	got, err := client.HGetAll(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
