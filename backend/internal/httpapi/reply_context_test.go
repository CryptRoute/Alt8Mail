package httpapi

import "testing"

func TestNormalizedReplySubject(t *testing.T) {
	base := normalizedReplySubject("Project update")
	for _, value := range []string{"Re: Project update", "RE: Project update", "Fwd: Re: Project update", "  fw: project update  "} {
		if got := normalizedReplySubject(value); got != base {
			t.Fatalf("normalizedReplySubject(%q) = %q, want %q", value, got, base)
		}
	}
	if normalizedReplySubject("Project update v2") == base {
		t.Fatal("different subjects must not match")
	}
}
