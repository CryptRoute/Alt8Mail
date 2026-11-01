package httpapi

import (
	"regexp"
	"testing"
)

func TestOpaqueThreadIDFormat(t *testing.T) {
	opaque := opaqueThreadID("<root@example.com>")
	if len(opaque) != 22 {
		t.Fatalf("opaque thread id length = %d, want 22 (%q)", len(opaque), opaque)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`).MatchString(opaque) {
		t.Fatalf("opaque thread id is not URL-safe: %q", opaque)
	}
	if opaqueThreadID("<root@example.com>") != opaque {
		t.Fatal("opaque thread id is not deterministic")
	}
	if opaqueThreadID("<other@example.com>") == opaque {
		t.Fatal("different roots produced the same thread id")
	}
	if got := opaqueThreadID("t_imap_ff7b66b977772e84"); got != "bIvYJV36zA28KuObHxmSGw" {
		t.Fatalf("legacy mapping changed: %q", got)
	}
}

func TestNewThreadIDFormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	for index := 0; index < 100; index++ {
		id := newThreadID()
		if !pattern.MatchString(id) {
			t.Fatalf("new thread id is not opaque URL-safe text: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate random thread id: %q", id)
		}
		seen[id] = true
	}
}

func TestNormalizeLegacyThreadID(t *testing.T) {
	legacy := "t_imap_ff7b66b977772e84"
	opaque := "bIvYJV36zA28KuObHxmSGw"
	if got := normalizeThreadID(legacy); got != opaque {
		t.Fatalf("legacy normalization = %q, want %q", got, opaque)
	}
	if got := normalizeThreadID("  " + legacy + " "); got != opaque {
		t.Fatalf("trimmed legacy normalization = %q, want %q", got, opaque)
	}
	if got := normalizeThreadID(opaque); got != opaque {
		t.Fatalf("opaque ID changed: %q", got)
	}
	if got := normalizeThreadID(""); got != "" {
		t.Fatalf("empty ID changed: %q", got)
	}
}

func TestLegacyThreadIDRecognition(t *testing.T) {
	valid := []string{
		"t_imap_ff7b66b977772e84",
		"t_AbCdEf0123456789",
	}
	for _, value := range valid {
		if !isLegacyThreadID(value) {
			t.Fatalf("expected legacy thread id: %q", value)
		}
	}
	invalid := []string{
		"t_IMAP_ff7b66b977772e84",
		"t_short",
		"t_imap_nothex",
		"t_imap_ff7b66b977772e840",
		"bIvYJV36zA28KuObHxmSGw",
		"t_AbCdEf01234567890",
	}
	for _, value := range invalid {
		if isLegacyThreadID(value) {
			t.Fatalf("unexpected legacy match: %q", value)
		}
	}
}
