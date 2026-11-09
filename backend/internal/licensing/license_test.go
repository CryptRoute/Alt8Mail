package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey
}

func testToken(t *testing.T, privateKey ed25519.PrivateKey, license License) string {
	t.Helper()
	payload, err := json.Marshal(license)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := EnvelopePrefix + "." + encoded
	signature := ed25519.Sign(privateKey, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func validLicense() License {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	return License{
		Version:   Version,
		ID:        "lic_test",
		Tenant:    "tenant_a",
		Edition:   "enterprise",
		Seats:     50,
		Features:  []string{"sso", "scim", "shared_inbox", "sso"},
		IssuedAt:  now.Add(-24 * time.Hour),
		ExpiresAt: now.Add(30 * 24 * time.Hour),
		GraceDays: 7,
	}
}

func TestVerifyValidLicense(t *testing.T) {
	publicKey, privateKey := testKey(t)
	verifier := Verifier{PublicKey: publicKey}
	license, err := verifier.Verify(testToken(t, privateKey, validLicense()))
	if err != nil {
		t.Fatal(err)
	}
	if license.ID != "lic_test" || license.Edition != "enterprise" || license.Seats != 50 {
		t.Fatalf("unexpected claims: %#v", license)
	}
	if len(license.Features) != 3 || !license.HasFeature("SSO") || !license.HasFeature("scim") {
		t.Fatalf("features not normalized: %#v", license.Features)
	}
	status := license.Status(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	if status.State != "valid" || status.InGrace || status.Expired || status.SeatLimit != 50 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.DaysRemaining != 29 {
		t.Fatalf("days remaining = %d, want 29", status.DaysRemaining)
	}
}

func TestLicenseStatusGraceAndExpired(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	license := validLicense()
	license.ExpiresAt = now.Add(-time.Hour)

	grace := license.Status(now)
	if grace.State != "grace" || !grace.InGrace || grace.Expired {
		t.Fatalf("expected grace state, got %#v", grace)
	}
	if grace.DaysRemaining != 7 {
		t.Fatalf("grace days remaining = %d, want 7", grace.DaysRemaining)
	}

	expired := license.Status(now.Add(8 * 24 * time.Hour))
	if expired.State != "expired" || !expired.Expired || expired.InGrace {
		t.Fatalf("expected expired state, got %#v", expired)
	}
	if expired.DaysRemaining != 0 {
		t.Fatalf("expired days remaining = %d, want 0", expired.DaysRemaining)
	}
}

func TestVerifyRejectsInvalidEnvelopes(t *testing.T) {
	publicKey, privateKey := testKey(t)
	verifier := Verifier{PublicKey: publicKey}
	token := testToken(t, privateKey, validLicense())

	tamperedPayload := strings.Split(token, ".")
	tamperedPayload[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"version":1}`))
	if _, err := verifier.Verify(strings.Join(tamperedPayload, ".")); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered payload error = %v, want signature error", err)
	}

	_, otherPrivateKey := testKey(t)
	if _, err := verifier.Verify(testToken(t, otherPrivateKey, validLicense())); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong key error = %v, want signature error", err)
	}

	for _, malformed := range []string{"", "A8M1", "A8M1.payload", "A8M2.payload.signature", "A8M1.!!!.!!!"} {
		if _, err := verifier.Verify(malformed); !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed %q error = %v, want malformed", malformed, err)
		}
	}
}

func TestVerifyRejectsInvalidClaims(t *testing.T) {
	publicKey, privateKey := testKey(t)
	verifier := Verifier{PublicKey: publicKey}

	cases := []struct {
		name   string
		change func(*License)
		want   error
	}{
		{"missing id", func(license *License) { license.ID = " " }, ErrClaim},
		{"missing edition", func(license *License) { license.Edition = "" }, ErrClaim},
		{"zero seats", func(license *License) { license.Seats = 0 }, ErrClaim},
		{"missing expiry", func(license *License) { license.ExpiresAt = time.Time{} }, ErrClaim},
		{"negative grace", func(license *License) { license.GraceDays = -1 }, ErrClaim},
		{"unsupported version", func(license *License) { license.Version = 2 }, ErrUnsupported},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			license := validLicense()
			testCase.change(&license)
			if _, err := verifier.Verify(testToken(t, privateKey, license)); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestParsePublicKey(t *testing.T) {
	publicKey, _ := testKey(t)
	raw := base64.RawURLEncoding.EncodeToString(publicKey)
	parsed, err := ParsePublicKey(raw)
	if err != nil || !parsed.Equal(publicKey) {
		t.Fatalf("raw url key parse failed: %v", err)
	}
	standard := base64.StdEncoding.EncodeToString(publicKey)
	parsed, err = ParsePublicKey(standard)
	if err != nil || !parsed.Equal(publicKey) {
		t.Fatalf("standard key parse failed: %v", err)
	}
	if _, err := ParsePublicKey("not-a-key"); !errors.Is(err, ErrPublicKey) {
		t.Fatalf("invalid key error = %v, want public key error", err)
	}
}
