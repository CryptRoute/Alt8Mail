package control

import (
	"strings"
	"testing"
)

func TestOutboundCapDecisionPerMailbox(t *testing.T) {
	const domain = "a8m.test"

	if reason, allowed := outboundCapDecision(false, 0, 0, 0, 0, 150, 0, 0, 0, 149, domain); !allowed {
		t.Fatalf("the 150th recipient should be allowed, got %q", reason)
	}
	reason, allowed := outboundCapDecision(false, 0, 0, 0, 0, 150, 0, 0, 0, 150, domain)
	if allowed || !strings.Contains(reason, "150 recipients/day") {
		t.Fatalf("the 151st recipient should be deferred by the mailbox cap, got %q", reason)
	}

	// Zero means unlimited, even after a very large number of accepted
	// recipients and with domain rolling caps disabled.
	if reason, allowed := outboundCapDecision(false, 0, 0, 0, 0, 0, 0, 0, 0, 1_000_000, domain); !allowed {
		t.Fatalf("zero mailbox cap must be unlimited, got %q", reason)
	}
}

func TestOutboundCapDecisionDomainCapsStillApply(t *testing.T) {
	const domain = "a8m.test"

	reason, allowed := outboundCapDecision(true, 0, 100, 0, 0, 0, 0, 100, 0, 0, domain)
	if allowed || !strings.Contains(reason, "daily cap reached for a8m.test") {
		t.Fatalf("domain daily cap should defer, got %q", reason)
	}

	reason, allowed = outboundCapDecision(true, 10, 0, 0, 5, 0, 15, 0, 0, 0, domain)
	if allowed || !strings.Contains(reason, "hourly cap reached") {
		t.Fatalf("hourly cap plus burst should defer, got %q", reason)
	}

	if reason, allowed := outboundCapDecision(true, 10, 0, 0, 5, 0, 14, 0, 0, 0, domain); !allowed {
		t.Fatalf("usage within hourly cap and burst should be allowed, got %q", reason)
	}
}
