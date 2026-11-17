package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestSignRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	license := License{
		Version: Version, ID: "lic_sign_test", Edition: "enterprise", Seats: 7,
		Features: []string{FeatureSSO, FeatureSharedInbox, FeatureSSO},
		IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(24 * time.Hour),
		GraceDays: 7,
	}
	token, err := Sign(privateKey, license)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := (Verifier{PublicKey: publicKey}).Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID != license.ID || parsed.Seats != 7 || len(parsed.Features) != 2 {
		t.Fatalf("parsed license = %#v", parsed)
	}
	if !parsed.HasFeature(FeatureSSO) || !parsed.HasFeature(FeatureSharedInbox) {
		t.Fatalf("features lost: %#v", parsed.Features)
	}
}

func TestSignRejectsInvalidClaims(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sign(privateKey, License{ID: "x", Edition: "enterprise", Seats: 0, ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("zero seats was signed")
	}
	if _, err := Sign(ed25519.PrivateKey("short"), License{ID: "x", Edition: "enterprise", Seats: 1, ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("short key was accepted")
	}
}

func TestSignV2ClaimsRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	license := License{
		Version: Version, ID: "lic_v2", KeyID: "2026-11", Tenant: "tenant_a",
		Edition: "enterprise", Plan: "enterprise", Seats: 120,
		Limits:       &Limits{StorageBytes: 5 << 40, Domains: 4, Mailboxes: 500},
		DeploymentID: "dep_abc123", Features: []string{FeatureSSO, FeatureSCIM},
		IssuedAt: now, ExpiresAt: now.AddDate(1, 0, 0), GraceDays: 14,
	}
	token, err := Sign(privateKey, license)
	if err != nil {
		t.Fatal(err)
	}
	verifier := Verifier{PublicKey: publicKey, KeyID: "2026-11"}
	parsed, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.KeyID != "2026-11" || parsed.Plan != "enterprise" || parsed.DeploymentID != "dep_abc123" {
		t.Fatalf("v2 claims lost: %#v", parsed)
	}
	if parsed.Limits == nil || parsed.Limits.StorageBytes != 5<<40 || parsed.Limits.Domains != 4 {
		t.Fatalf("limits lost: %#v", parsed.Limits)
	}
	status := parsed.Status(now.Add(time.Hour))
	if status.Plan != "enterprise" || status.DeploymentID != "dep_abc123" || status.Trial {
		t.Fatalf("status claims lost: %#v", status)
	}
}

func TestVerifierRejectsKeyIDMismatch(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	token, err := Sign(privateKey, License{
		Version: Version, ID: "lic_kid", KeyID: "2026-11", Edition: "enterprise",
		Seats: 10, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Verifier{PublicKey: publicKey, KeyID: "2027-01"}).Verify(token); !errors.Is(err, ErrKeyID) {
		t.Fatalf("kid mismatch error = %v, want %v", err, ErrKeyID)
	}
	if _, err := (Verifier{PublicKey: publicKey}).Verify(token); err != nil {
		t.Fatalf("verifier without a configured kid rejected the token: %v", err)
	}
}

func TestSignRejectsTrialGrace(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, err = Sign(privateKey, License{
		Version: Version, ID: "lic_trial", Edition: "enterprise", Seats: 10,
		Trial: true, IssuedAt: now, ExpiresAt: now.AddDate(0, 0, 14), GraceDays: 7,
	})
	if !errors.Is(err, ErrClaim) {
		t.Fatalf("trial with grace error = %v, want claim error", err)
	}
}

func TestVerifyAcceptsV1AndRejectsV2FieldsOnV1(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Now().UTC().Truncate(time.Second)
	v1 := License{
		Version: MinVersion, ID: "lic_v1", Edition: "enterprise", Seats: 5,
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), GraceDays: 1,
	}
	parsed, err := (Verifier{PublicKey: publicKey}).Verify(testToken(t, privateKey, v1))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != 1 || parsed.EffectivePlan() != "enterprise" {
		t.Fatalf("v1 compatibility lost: %#v", parsed)
	}
	v1.Plan = "enterprise"
	if _, err := (Verifier{PublicKey: publicKey}).Verify(testToken(t, privateKey, v1)); !errors.Is(err, ErrClaim) {
		t.Fatalf("v1 with a plan error = %v, want claim error", err)
	}
}

func TestSignFuncExternalSigner(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Now().UTC().Truncate(time.Second)
	license := License{
		Version: Version, ID: "lic_external", KeyID: "kms-2026", Edition: "enterprise",
		Plan: "enterprise", Seats: 10, Features: []string{FeatureSSO},
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	called := false
	token, err := SignFunc(func(message []byte) ([]byte, error) {
		called = true
		return ed25519.Sign(privateKey, message), nil
	}, license)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("external signer was not called")
	}
	if _, err := (Verifier{PublicKey: publicKey, KeyID: "kms-2026"}).Verify(token); err != nil {
		t.Fatal(err)
	}
	if _, err := SignFunc(func([]byte) ([]byte, error) { return []byte("short"), nil }, license); !errors.Is(err, ErrSignature) {
		t.Fatalf("short signature error = %v, want %v", err, ErrSignature)
	}
	wantErr := errors.New("hsm unavailable")
	if _, err := SignFunc(func([]byte) ([]byte, error) { return nil, wantErr }, license); !errors.Is(err, wantErr) {
		t.Fatalf("signer error = %v", err)
	}
}
