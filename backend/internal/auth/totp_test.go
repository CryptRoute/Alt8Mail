package auth

import (
	"testing"
	"time"
)

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 SHA-1 test secret "12345678901234567890" for T=59.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := TOTPCode(secret, time.Unix(59, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("expected 287082, got %s", code)
	}
	if !ValidateTOTP(secret, code, time.Unix(59, 0).UTC(), 0) {
		t.Fatal("valid TOTP was rejected")
	}
	if ValidateTOTP(secret, "000000", time.Unix(59, 0).UTC(), 0) {
		t.Fatal("invalid TOTP was accepted")
	}
}

func TestRecoveryCodesHashAndNormalize(t *testing.T) {
	codes, err := GenerateRecoveryCodes(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 4 {
		t.Fatalf("expected 4 codes, got %d", len(codes))
	}
	hash := HashRecoveryCode(codes[0])
	if hash == HashRecoveryCode(codes[1]) {
		t.Fatal("distinct recovery codes produced the same hash")
	}
	lower := NormalizeRecoveryCode(codes[0])
	if hash != HashRecoveryCode(lower) {
		t.Fatal("normalization did not make the hash case/spacing insensitive")
	}
}
