package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func encodedKey(t *testing.T, key ed25519.PublicKey) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString(key)
}

func TestResolveVerifierPrefersBuiltinKey(t *testing.T) {
	builtinPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	configuredPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldID, oldKeys := BuiltinPublicKey, BuiltinPublicKeyID, BuiltinPublicKeys
	t.Cleanup(func() { BuiltinPublicKey, BuiltinPublicKeyID, BuiltinPublicKeys = oldKey, oldID, oldKeys })
	BuiltinPublicKey = encodedKey(t, builtinPub)
	BuiltinPublicKeyID = "2026-11"
	BuiltinPublicKeys = ""

	verifier, source, err := ResolveVerifier(encodedKey(t, configuredPub), true)
	if err != nil {
		t.Fatal(err)
	}
	if source != TrustAnchorBuiltin || verifier.KeyID != "2026-11" {
		t.Fatalf("source=%q keyID=%q", source, verifier.KeyID)
	}
	if string(verifier.PublicKey) != string(builtinPub) {
		t.Fatal("configured key overrode the built-in trust anchor")
	}
}

func TestResolveVerifierConfigFallback(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := BuiltinPublicKey
	oldKeys := BuiltinPublicKeys
	t.Cleanup(func() { BuiltinPublicKey = old; BuiltinPublicKeys = oldKeys })
	BuiltinPublicKey = ""
	BuiltinPublicKeys = ""

	verifier, source, err := ResolveVerifier(encodedKey(t, pub), true)
	if err != nil {
		t.Fatal(err)
	}
	if source != TrustAnchorConfig || string(verifier.PublicKey) != string(pub) {
		t.Fatalf("source=%q key=%x", source, verifier.PublicKey)
	}

	if _, _, err := ResolveVerifier(encodedKey(t, pub), false); !errors.Is(err, ErrTrustAnchor) {
		t.Fatalf("production config fallback error = %v, want ErrTrustAnchor", err)
	}
	if _, _, err := ResolveVerifier("", false); !errors.Is(err, ErrTrustAnchor) {
		t.Fatalf("empty trust anchor error = %v, want ErrTrustAnchor", err)
	}
}

func TestConfigKeyAllowed(t *testing.T) {
	for _, env := range []string{"", "development", "dev", "test", "testing"} {
		if !ConfigKeyAllowed(env, false) {
			t.Fatalf("ConfigKeyAllowed(%q, false) = false", env)
		}
	}
	for _, env := range []string{"production", "staging"} {
		if ConfigKeyAllowed(env, false) {
			t.Fatalf("ConfigKeyAllowed(%q, false) = true", env)
		}
	}
	if !ConfigKeyAllowed("production", true) {
		t.Fatal("explicit migration switch must allow the configured key")
	}
}

func TestResolveVerifierBuiltinKeyRing(t *testing.T) {
	currentPub, currentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldPub, oldPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, oldID, oldKeys := BuiltinPublicKey, BuiltinPublicKeyID, BuiltinPublicKeys
	t.Cleanup(func() { BuiltinPublicKey, BuiltinPublicKeyID, BuiltinPublicKeys = oldKey, oldID, oldKeys })
	BuiltinPublicKey = encodedKey(t, currentPub)
	BuiltinPublicKeyID = "2026-11"
	BuiltinPublicKeys = "2025-01:" + encodedKey(t, oldPub) + ",2026-11:" + encodedKey(t, currentPub)

	verifier, source, err := ResolveVerifierWithKeys("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if source != TrustAnchorBuiltin || verifier.KeyID != "2026-11" {
		t.Fatalf("source=%q keyID=%q", source, verifier.KeyID)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, testCase := range []struct {
		name string
		key  ed25519.PrivateKey
		kid  string
	}{
		{name: "current", key: currentPriv, kid: "2026-11"},
		{name: "rotation history", key: oldPriv, kid: "2025-01"},
	} {
		token, err := Sign(testCase.key, License{
			Version: Version, ID: "lic_" + testCase.name, KeyID: testCase.kid,
			Edition: "enterprise", Seats: 10, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(token); err != nil {
			t.Fatalf("%s key rejected: %v", testCase.name, err)
		}
	}
	unknownToken, err := Sign(oldPriv, License{
		Version: Version, ID: "lic_unknown", KeyID: "2027-01", Edition: "enterprise",
		Seats: 10, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(unknownToken); !errors.Is(err, ErrKeyID) {
		t.Fatalf("unknown kid error = %v, want %v", err, ErrKeyID)
	}
	v1Token, err := Sign(oldPriv, License{
		Version: MinVersion, ID: "lic_v1_rotation", Edition: "enterprise",
		Seats: 5, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(v1Token); err != nil {
		t.Fatalf("v1 token signed by a rotation key rejected: %v", err)
	}
}
