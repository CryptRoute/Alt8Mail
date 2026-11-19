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
	// EnvelopePrefix is unchanged between schema versions. The payload's
	// version field distinguishes v1 from v2, so one verifier can accept both.
	EnvelopePrefix = "A8M1"
	// Version is the current license schema version produced by Sign when the
	// caller does not pin one.
	Version = 2
	// MinVersion is the oldest schema a current verifier accepts.
	MinVersion = 1
)

var (
	ErrMalformed   = errors.New("license envelope is malformed")
	ErrSignature   = errors.New("license signature is invalid")
	ErrClaim       = errors.New("license claims are invalid")
	ErrPublicKey   = errors.New("license public key is invalid")
	ErrPrivateKey  = errors.New("license private key is invalid")
	ErrUnsupported = errors.New("license version is unsupported")
	ErrKeyID       = errors.New("license key id does not match the verifier")
)

// Limits carries optional non-seat capacity ceilings. The seat limit stays in
// License.Seats because every product path uses it; a nil Limits means the
// license sets no additional ceilings. Values are advisory unless the product
// documents enforcement for that key.
type Limits struct {
	StorageBytes int64 `json:"storageBytes,omitempty"`
	Domains      int   `json:"domains,omitempty"`
	Mailboxes    int   `json:"mailboxes,omitempty"`
	APIRequests  int64 `json:"apiRequests,omitempty"`
}

// License is the signed, non-secret entitlement document. It intentionally
// contains no customer secrets and is safe to expose in a redacted snapshot.
type License struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	// KeyID names the signing key (v2). A verifier with a configured key id
	// rejects a token signed under a different key id.
	KeyID          string    `json:"kid,omitempty"`
	Tenant         string    `json:"tenant,omitempty"`
	Edition        string    `json:"edition"`
	Plan           string    `json:"plan,omitempty"`
	Seats          int       `json:"seats"`
	Limits         *Limits   `json:"limits,omitempty"`
	DeploymentID   string    `json:"deploymentId,omitempty"`
	Infrastructure string    `json:"infrastructure,omitempty"`
	Trial          bool      `json:"trial,omitempty"`
	Features       []string  `json:"features"`
	IssuedAt       time.Time `json:"issuedAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	GraceDays      int       `json:"graceDays"`
}

// EffectivePlan returns the signed plan, falling back to the edition for v1
// tokens and for v2 tokens that omitted the optional plan claim.
func (l License) EffectivePlan() string {
	if plan := strings.TrimSpace(l.Plan); plan != "" {
		return plan
	}
	return strings.TrimSpace(l.Edition)
}

// Status is the lifecycle view of a verified license at a point in time.
type Status struct {
	State          string
	InGrace        bool
	Expired        bool
	SeatLimit      int
	Features       []string
	DaysRemaining  int
	ExpiresAt      time.Time
	Plan           string
	DeploymentID   string
	Infrastructure string
	Trial          bool
	Limits         *Limits
}

// Verifier checks license envelopes against one Ed25519 public key, optionally
// with a key ring of rotation keys selected by the token's kid claim.
type Verifier struct {
	PublicKey ed25519.PublicKey
	KeyID     string
	Keys      map[string]ed25519.PublicKey
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
	if len(v.PublicKey) != ed25519.PublicKeySize && len(v.Keys) == 0 {
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
	// The kid is read before signature verification only to select a candidate
	// key. The signature must still verify with that key, and an unknown kid is
	// rejected rather than silently falling back to a rotation key.
	var header struct {
		KeyID string `json:"kid"`
	}
	if err := json.Unmarshal(payload, &header); err != nil {
		return License{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	headerKeyID := strings.TrimSpace(header.KeyID)
	publicKey := v.PublicKey
	selectedKeyID := strings.TrimSpace(v.KeyID)
	signingInput := []byte(EnvelopePrefix + "." + parts[1])
	if headerKeyID != "" {
		if candidate, ok := v.Keys[headerKeyID]; ok {
			publicKey = candidate
			selectedKeyID = headerKeyID
		} else if v.KeyID != "" && strings.EqualFold(v.KeyID, headerKeyID) {
			publicKey = v.PublicKey
			selectedKeyID = v.KeyID
		} else if v.KeyID == "" && len(v.Keys) == 0 && len(v.PublicKey) == ed25519.PublicKeySize {
			// A configured single key without an id accepts any kid for
			// migration compatibility.
		} else {
			return License{}, ErrKeyID
		}
		if len(publicKey) != ed25519.PublicKeySize {
			return License{}, ErrPublicKey
		}
		if !ed25519.Verify(publicKey, signingInput, signature) {
			return License{}, ErrSignature
		}
	} else {
		// Version 1 tokens carry no kid. Try the default key and then the
		// rotation ring so an old v1 token does not break when the default key
		// rotates.
		verified := false
		if len(v.PublicKey) == ed25519.PublicKeySize && ed25519.Verify(v.PublicKey, signingInput, signature) {
			verified = true
		}
		if !verified {
			for keyID, candidate := range v.Keys {
				if ed25519.Verify(candidate, signingInput, signature) {
					publicKey = candidate
					selectedKeyID = keyID
					verified = true
					break
				}
			}
		}
		if !verified {
			return License{}, ErrSignature
		}
	}
	var license License
	if err := json.Unmarshal(payload, &license); err != nil {
		return License{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := normalizeAndValidate(&license); err != nil {
		return License{}, err
	}
	if license.KeyID != "" && selectedKeyID != "" && !strings.EqualFold(selectedKeyID, license.KeyID) {
		return License{}, ErrKeyID
	}
	return license, nil
}

// normalizeAndValidate trims and checks a license for both Sign and Verify so
// a token the portal produces is exactly the token the product accepts.
func normalizeAndValidate(license *License) error {
	if license.Version < MinVersion || license.Version > Version {
		return ErrUnsupported
	}
	license.ID = strings.TrimSpace(license.ID)
	license.KeyID = strings.TrimSpace(license.KeyID)
	license.Tenant = strings.TrimSpace(license.Tenant)
	license.Edition = strings.TrimSpace(license.Edition)
	license.Plan = strings.TrimSpace(license.Plan)
	license.DeploymentID = strings.TrimSpace(license.DeploymentID)
	license.Features = normalizeFeatures(license.Features)
	if license.ID == "" || license.Edition == "" {
		return fmt.Errorf("%w: id and edition are required", ErrClaim)
	}
	if license.Seats < 1 {
		return fmt.Errorf("%w: seats must be positive", ErrClaim)
	}
	if license.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: expiresAt is required", ErrClaim)
	}
	if license.GraceDays < 0 {
		return fmt.Errorf("%w: graceDays must not be negative", ErrClaim)
	}
	if !license.IssuedAt.IsZero() && license.IssuedAt.After(license.ExpiresAt) {
		return fmt.Errorf("%w: issuedAt is after expiresAt", ErrClaim)
	}
	if license.Version >= 2 {
		if license.IssuedAt.IsZero() {
			return fmt.Errorf("%w: issuedAt is required for version 2", ErrClaim)
		}
		if license.Trial && license.GraceDays != 0 {
			return fmt.Errorf("%w: trial licenses must not carry a grace period", ErrClaim)
		}
		if license.Plan == "" {
			license.Plan = license.Edition
		}
		if license.Limits != nil {
			if license.Limits.StorageBytes < 0 || license.Limits.Domains < 0 ||
				license.Limits.Mailboxes < 0 || license.Limits.APIRequests < 0 {
				return fmt.Errorf("%w: limits must not be negative", ErrClaim)
			}
		}
		return nil
	}
	if license.KeyID != "" || license.Plan != "" || license.Limits != nil ||
		license.DeploymentID != "" || license.Trial {
		return fmt.Errorf("%w: key id, plan, limits, deployment id and trial require version 2", ErrClaim)
	}
	return nil
}

// Status reports valid, grace or expired state at the supplied time.
func (l License) Status(now time.Time) Status {
	status := Status{
		State:          "valid",
		SeatLimit:      l.Seats,
		Features:       append([]string(nil), l.Features...),
		ExpiresAt:      l.ExpiresAt,
		Plan:           l.EffectivePlan(),
		DeploymentID:   l.DeploymentID,
		Infrastructure: l.Infrastructure,
		Trial:          l.Trial,
	}
	if l.Limits != nil {
		limits := *l.Limits
		status.Limits = &limits
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
func decodeSegment(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(value)
	}
	return decoded, err
}

// Signer is implemented by the private licensing portal and offline tooling.
// It produces the same A8M1 envelope the verifier consumes. It never belongs
// on a customer server: the private key stays in KMS/HSM or an offline signer.
func Sign(privateKey ed25519.PrivateKey, license License) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", ErrPrivateKey
	}
	return SignFunc(func(message []byte) ([]byte, error) {
		return ed25519.Sign(privateKey, message), nil
	}, license)
}

// SignFunc builds a canonical license envelope and delegates the Ed25519
// signature to sign. It lets the private portal use a KMS, HSM or remote
// signing command without ever loading the private key into this process.
// sign receives the exact bytes the product verifies.
func SignFunc(sign func([]byte) ([]byte, error), license License) (string, error) {
	if sign == nil {
		return "", ErrPrivateKey
	}
	if license.Version == 0 {
		license.Version = Version
	}
	if err := normalizeAndValidate(&license); err != nil {
		return "", err
	}
	payload, err := json.Marshal(license)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := EnvelopePrefix + "." + encoded
	signature, err := sign([]byte(signingInput))
	if err != nil {
		return "", err
	}
	if len(signature) != ed25519.SignatureSize {
		return "", ErrSignature
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
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
