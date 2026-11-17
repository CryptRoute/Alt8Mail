package threatintel

import (
	"context"
	"reflect"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestPublishAndRebuildConfirmedSet(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client.Close()
	ctx := context.Background()

	reports := []ReportIndicators{
		{Sender: "Alice@Example.com", Domain: "example.com", URLHosts: []string{"shared.example", "one.example"}},
		{Sender: "bob@example.net", Domain: "example.net", URLHosts: []string{"shared.example"}},
	}
	if err := Publish(ctx, client, ActiveSet(reports)); err != nil {
		t.Fatalf("publish confirmed set: %v", err)
	}
	assertHash(t, client, SenderMap, map[string]string{
		"alice@example.com": "1", "bob@example.net": "1",
	})
	assertHash(t, client, DomainMap, map[string]string{
		"example.com": "1", "example.net": "1",
	})
	assertHash(t, client, URLMap, map[string]string{
		"one.example": "1", "shared.example": "1",
	})

	// Dismiss Alice's report. The shared host must remain because Bob's
	// confirmed report still contributes it; Alice's exclusive indicators go.
	if err := Publish(ctx, client, ActiveSet(reports[1:])); err != nil {
		t.Fatalf("publish after dismissal: %v", err)
	}
	assertHash(t, client, SenderMap, map[string]string{"bob@example.net": "1"})
	assertHash(t, client, DomainMap, map[string]string{"example.net": "1"})
	assertHash(t, client, URLMap, map[string]string{"shared.example": "1"})

	// Undo the last confirmation: all three maps become empty.
	if err := Publish(ctx, client, ActiveSet(nil)); err != nil {
		t.Fatalf("publish empty set: %v", err)
	}
	for _, key := range []string{SenderMap, DomainMap, URLMap} {
		if length, err := client.HLen(ctx, key).Result(); err != nil || length != 0 {
			t.Fatalf("%s after undo: length=%d err=%v", key, length, err)
		}
	}
}

func TestClearRemovesPublishedMaps(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client.Close()
	ctx := context.Background()
	set := ActiveSet([]ReportIndicators{{Sender: "a@example.com", Domain: "example.com", URLHosts: []string{"example.com"}}})
	if err := Publish(ctx, client, set); err != nil {
		t.Fatal(err)
	}
	if err := Clear(ctx, client); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{SenderMap, DomainMap, URLMap} {
		if exists, err := client.Exists(ctx, key).Result(); err != nil || exists != 0 {
			t.Fatalf("%s after clear: exists=%d err=%v", key, exists, err)
		}
	}
}

func assertHash(t *testing.T, client *redis.Client, key string, want map[string]string) {
	t.Helper()
	got, err := client.HGetAll(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
