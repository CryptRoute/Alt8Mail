package httpapi

import (
	"testing"
	"time"
)

func TestLoginLimiterDefaultEntryCap(t *testing.T) {
	limiter := newLoginLimiter(5, 10*time.Minute)
	if limiter.maxEntries != loginLimiterMaxEntries {
		t.Fatalf("maxEntries = %d, want %d", limiter.maxEntries, loginLimiterMaxEntries)
	}
	if limiter.sweepEvery != loginLimiterMaxSweepInterval {
		t.Fatalf("sweepEvery = %v, want %v", limiter.sweepEvery, loginLimiterMaxSweepInterval)
	}
	short := newLoginLimiter(5, 10*time.Second)
	if short.sweepEvery != 10*time.Second {
		t.Fatalf("short-window sweepEvery = %v, want 10s", short.sweepEvery)
	}
}

func TestLoginLimiterEntryCapEvictsOldest(t *testing.T) {
	limiter := newLoginLimiter(5, 10*time.Minute)
	limiter.maxEntries = 3
	// Pre-populate with explicit, staggered reset times so "oldest" does not
	// depend on the resolution of the system clock.
	now := time.Now()
	limiter.attempts["a"] = limiterEntry{Count: 1, ResetAt: now.Add(time.Minute)}
	limiter.attempts["b"] = limiterEntry{Count: 1, ResetAt: now.Add(2 * time.Minute)}
	limiter.attempts["c"] = limiterEntry{Count: 1, ResetAt: now.Add(3 * time.Minute)}
	limiter.lastSweep = now // keep the periodic sweep out of the way

	// An existing counter still increments at capacity and must not evict
	// another address.
	limiter.Fail("b")
	if len(limiter.attempts) != 3 || limiter.attempts["b"].Count != 2 {
		t.Fatalf("existing counter was not preserved: %#v", limiter.attempts)
	}
	for _, ip := range []string{"a", "b", "c"} {
		if _, exists := limiter.attempts[ip]; !exists {
			t.Fatalf("incrementing an existing entry evicted %q: %#v", ip, limiter.attempts)
		}
	}

	// A new address evicts the entry with the earliest reset, not whichever
	// entry the map happened to iterate first.
	limiter.Fail("d")
	if len(limiter.attempts) != 3 {
		t.Fatalf("entry count after cap = %d, want 3", len(limiter.attempts))
	}
	if _, exists := limiter.attempts["a"]; exists {
		t.Fatalf("oldest entry was not evicted: %#v", limiter.attempts)
	}
	for _, ip := range []string{"b", "c", "d"} {
		if _, exists := limiter.attempts[ip]; !exists {
			t.Fatalf("active entry %q was evicted: %#v", ip, limiter.attempts)
		}
	}
	if limiter.attempts["b"].Count != 2 {
		t.Fatalf("new address eviction changed an existing counter: %#v", limiter.attempts)
	}
}

func TestLoginLimiterCapEvictsExpiredBeforeActive(t *testing.T) {
	limiter := newLoginLimiter(5, time.Minute)
	limiter.maxEntries = 3
	now := time.Now()
	limiter.attempts["expired"] = limiterEntry{Count: 9, ResetAt: now.Add(-time.Second)}
	limiter.attempts["a"] = limiterEntry{Count: 1, ResetAt: now.Add(time.Minute)}
	limiter.attempts["b"] = limiterEntry{Count: 1, ResetAt: now.Add(2 * time.Minute)}
	limiter.lastSweep = now // keep the periodic sweep out of the way

	limiter.Fail("c")
	if _, exists := limiter.attempts["expired"]; exists {
		t.Fatalf("expired entry was not evicted first: %#v", limiter.attempts)
	}
	if limiter.attempts["a"].Count != 1 || limiter.attempts["b"].Count != 1 {
		t.Fatalf("active counters were disturbed: %#v", limiter.attempts)
	}
	if _, exists := limiter.attempts["c"]; !exists || len(limiter.attempts) != 3 {
		t.Fatalf("new entry not admitted: %#v", limiter.attempts)
	}
}

func TestLoginLimiterGlobalSweep(t *testing.T) {
	limiter := newLoginLimiter(5, time.Minute)
	now := time.Now()
	limiter.attempts["old-1"] = limiterEntry{Count: 1, ResetAt: now.Add(-time.Minute)}
	limiter.attempts["old-2"] = limiterEntry{Count: 2, ResetAt: now.Add(-time.Second)}
	limiter.attempts["active"] = limiterEntry{Count: 2, ResetAt: now.Add(time.Minute)}
	limiter.sweepEvery = time.Minute
	limiter.lastSweep = now

	// Before the sweep interval elapses, expired entries are left for the
	// per-address path.
	if !limiter.Allow("active") {
		t.Fatal("active counter should still be allowed")
	}
	if len(limiter.attempts) != 3 {
		t.Fatalf("early sweep removed entries: %#v", limiter.attempts)
	}

	// Once the interval elapses, a single call sweeps every expired address.
	limiter.lastSweep = now.Add(-2 * time.Minute)
	if !limiter.Allow("active") {
		t.Fatal("active counter should be allowed after the sweep")
	}
	if len(limiter.attempts) != 1 {
		t.Fatalf("global sweep left stale entries: %#v", limiter.attempts)
	}
	if _, exists := limiter.attempts["active"]; !exists {
		t.Fatalf("global sweep removed an active entry: %#v", limiter.attempts)
	}
	if entry := limiter.attempts["active"]; entry.Count != 2 {
		t.Fatalf("global sweep changed a counter: %#v", entry)
	}
}

func TestLoginLimiterCountersAndReset(t *testing.T) {
	limiter := newLoginLimiter(2, time.Minute)
	ip := "192.0.2.10"
	if !limiter.Allow(ip) {
		t.Fatal("new address was not allowed")
	}
	limiter.Fail(ip)
	if limiter.Allow(ip) == false {
		t.Fatal("first failure should still be allowed (count 1 < 2)")
	}
	limiter.Fail(ip)
	if limiter.Allow(ip) {
		t.Fatal("second failure should block (count 2 >= 2)")
	}
	limiter.Reset(ip)
	if !limiter.Allow(ip) {
		t.Fatal("reset did not clear the counter")
	}
	if len(limiter.attempts) != 0 {
		t.Fatalf("reset left an entry: %#v", limiter.attempts)
	}
}

func TestLoginLimiterExpiredEntryResetsCounter(t *testing.T) {
	limiter := newLoginLimiter(1, time.Minute)
	ip := "198.51.100.20"
	limiter.attempts[ip] = limiterEntry{Count: 5, ResetAt: time.Now().Add(-time.Second)}
	limiter.lastSweep = time.Now()

	if !limiter.Allow(ip) {
		t.Fatal("an expired entry should allow the next attempt")
	}
	limiter.Fail(ip)
	if entry := limiter.attempts[ip]; entry.Count != 1 {
		t.Fatalf("expired counter was not reset: %#v", entry)
	}
}
