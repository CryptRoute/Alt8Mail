package addonoauth

import (
	"testing"
	"time"
)

func TestStateRoundTripAndTamper(t *testing.T) {
	state := State{
		TenantID: "tenant", AddonID: "slack", UserID: "user", Scope: "tenant",
		Mode: "managed", Callback: "https://mail.example/api/callback", Nonce: "nonce",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	raw, err := Sign("relay-secret", state)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parsed, err := Parse("relay-secret", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.AddonID != "slack" || parsed.Callback != state.Callback {
		t.Fatalf("unexpected state: %#v", parsed)
	}
	if _, err := Parse("wrong-secret", raw); err == nil {
		t.Fatal("state signed with another secret was accepted")
	}
	last := raw[len(raw)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	tampered := raw[:len(raw)-1] + string(replacement)
	if _, err := Parse("relay-secret", tampered); err == nil {
		t.Fatal("tampered state was accepted")
	}
}

func TestExpiredAndEmptyState(t *testing.T) {
	expired := State{TenantID: "tenant", AddonID: "slack", UserID: "user", Nonce: "nonce", ExpiresAt: time.Now().Add(-time.Second).Unix()}
	raw, _ := Sign("secret", expired)
	if _, err := Parse("secret", raw); err != ErrExpiredState {
		t.Fatalf("expired state error = %v", err)
	}
	if _, err := Sign("", expired); err == nil {
		t.Fatal("empty secret signed a state")
	}
}
