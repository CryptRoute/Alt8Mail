// Package licensing verifies signed, offline Alt8Mail license envelopes.
//
// A license is a compact, self-contained token:
//
//	A8M1.<base64url(payload-json)>.<base64url(ed25519-signature)>
//
// The signature covers the literal prefix and encoded payload, so changing
// either invalidates the token. Verification is local and never phones home.
package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	EnvelopePrefix = "A8M1"
	Version        = 1
)

var (
	ErrMalformed   = errors.New("license envelope is malformed")
	ErrSignature   = errors.New("license signature is invalid")
	ErrClaim       = errors.New("license claims are invalid")
	ErrPublicKey   = errors.New("license public key is invalid")
	ErrUnsupported = errors.New("license version is unsupported")
)

// License is the signed, non-secret entitlement document. It intentionally
// contains no customer secrets and is safe to expose in a redacted snapshot.
type License struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant,omitempty"`
	Edition   string    `json:"edition"`
	Seats     int       `json:"seats"`
	Features  []string  `json:"features"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	GraceDays int       `json:"graceDays"`
}

// Status is the lifecycle view of a verified license at a point in time.
type Status struct {
	State         string
	InGrace       bool
	Expired       bool
	SeatLimit     int
	Features      []string
	DaysRemaining int
	ExpiresAt     time.Time
}

// Verifier checks license envelopes against one Ed25519 public key.
type Verifier struct {
	PublicKey ed25519.PublicKey
}

// ParsePublicKey decodes a 32-byte Ed25519 public key from standard or URL-safe
// base64. Raw (unpadded) encodings are accepted as well.
func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	value := strings.TrimSpace(encoded)
	if value == "" {
		return nil, ErrPublicKey
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(value)
		if err == nil && len(decoded) == ed25519.PublicKeySize {
			return ed25519.PublicKey(decoded), nil
		}
	}
	return nil, ErrPublicKey
}

// Verify decodes and authenticates a license envelope. It does not reject an
// expired license; call Status to decide whether the paid edition is usable.
func (v Verifier) Verify(token string) (License, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != EnvelopePrefix || parts[1] == "" || parts[2] == "" {
		return License{}, ErrMalformed
	}
	if len(v.PublicKey) != ed25519.PublicKeySize {
		return License{}, ErrPublicKey
	}
	payload, err := decodeSegment(parts[1])
	if err != nil {
		return License{}, ErrMalformed
	}
	signature, err := decodeSegment(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return License{}, ErrMalformed
	}
	if !ed25519.Verify(v.PublicKey, []byte(EnvelopePrefix+"."+parts[1]), signature) {
		return License{}, ErrSignature
	}
	var license License
	if err := json.Unmarshal(payload, &license); err != nil {
		return License{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if license.Version != Version {
		return License{}, ErrUnsupported
	}
	if strings.TrimSpace(license.ID) == "" || strings.TrimSpace(license.Edition) == "" {
		return License{}, fmt.Errorf("%w: id and edition are required", ErrClaim)
	}
	if license.Seats < 1 {
		return License{}, fmt.Errorf("%w: seats must be positive", ErrClaim)
	}
	if license.ExpiresAt.IsZero() {
		return License{}, fmt.Errorf("%w: expiresAt is required", ErrClaim)
	}
	if license.GraceDays < 0 {
		return License{}, fmt.Errorf("%w: graceDays must not be negative", ErrClaim)
	}
	if !license.IssuedAt.IsZero() && license.IssuedAt.After(license.ExpiresAt) {
		return License{}, fmt.Errorf("%w: issuedAt is after expiresAt", ErrClaim)
	}
	license.ID = strings.TrimSpace(license.ID)
	license.Tenant = strings.TrimSpace(license.Tenant)
	license.Edition = strings.TrimSpace(license.Edition)
	license.Features = normalizeFeatures(license.Features)
	return license, nil
}

// Status reports valid, grace or expired state at the supplied time.
func (l License) Status(now time.Time) Status {
	status := Status{
		State:     "valid",
		SeatLimit: l.Seats,
		Features:  append([]string(nil), l.Features...),
		ExpiresAt: l.ExpiresAt,
	}
	if now.After(l.ExpiresAt) {
		graceEnd := l.ExpiresAt.AddDate(0, 0, l.GraceDays)
		if l.GraceDays > 0 && !now.After(graceEnd) {
			status.State = "grace"
			status.InGrace = true
			status.DaysRemaining = daysRemaining(now, graceEnd)
			return status
		}
		status.State = "expired"
		status.Expired = true
		return status
	}
	status.DaysRemaining = daysRemaining(now, l.ExpiresAt)
	return status
}

// HasFeature reports whether a verified license includes a feature name.
// Feature names are case-insensitive.
func (l License) HasFeature(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, feature := range l.Features {
		if strings.ToLower(feature) == name {
			return true
		}
	}
	return false
}

func decodeSegment(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(value)
	}
	return decoded, err
}

func normalizeFeatures(features []string) []string {
	result := make([]string, 0, len(features))
	seen := map[string]bool{}
	for _, feature := range features {
		feature = strings.TrimSpace(feature)
		if feature == "" || !utf8.ValidString(feature) {
			continue
		}
		key := strings.ToLower(feature)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, feature)
	}
	return result
}

func daysRemaining(now, end time.Time) int {
	if !end.After(now) {
		return 0
	}
	remaining := end.Sub(now)
	days := int(remaining / (24 * time.Hour))
	if remaining%(24*time.Hour) != 0 {
		days++
	}
	return days
}
