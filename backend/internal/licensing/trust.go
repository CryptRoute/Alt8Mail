package licensing

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
)

// Build-time trust anchor. Release builds inject this with:
//
//	-X github.com/CryptRoute/Alt8Mail/backend/internal/licensing.BuiltinPublicKey=<base64>
//	-X github.com/CryptRoute/Alt8Mail/backend/internal/licensing.BuiltinPublicKeyID=<key-id>
//	-X github.com/CryptRoute/Alt8Mail/backend/internal/licensing.BuiltinPublicKeys=<kid:base64,kid:base64>
//
// BuiltinPublicKeys is the rotation history. The current key is
// BuiltinPublicKey/BuiltinPublicKeyID; older keys remain valid by kid until
// they are removed from the ring. The public keys are not secret. Private keys
// must live in KMS/HSM or an offline signing device, never in the repository or
// a deployment environment.
var (
	BuiltinPublicKey   string
	BuiltinPublicKeyID string
	BuiltinPublicKeys  string
)

var ErrTrustAnchor = errors.New("license verification trust anchor is not configured")

const (
	TrustAnchorBuiltin = "builtin"
	TrustAnchorConfig  = "config"
	TrustAnchorMissing = "missing"
)

// ResolveVerifier chooses the key used to verify license envelopes. A compiled
// vendor key always wins over the deployment configuration. The configured key
// is honored only when allowConfigured is true, which is intended for local
// development, tests and an explicit migration switch. Production must use the
// built-in key: otherwise a root operator can generate their own key pair and
// issue a license for their own deployment.
func ResolveVerifier(configuredKey string, allowConfigured bool) (Verifier, string, error) {
	return ResolveVerifierWithKeys(configuredKey, "", allowConfigured)
}

// ResolveVerifierWithKeys is ResolveVerifier plus a rotation key list in the
// form "kid:base64,kid2:base64". The compiled ring wins; the configured ring is
// only used under the same migration rules as the configured single key.
func ResolveVerifierWithKeys(configuredKey, configuredKeys string, allowConfigured bool) (Verifier, string, error) {
	if strings.TrimSpace(BuiltinPublicKey) != "" || strings.TrimSpace(BuiltinPublicKeys) != "" {
		verifier, err := buildVerifier(BuiltinPublicKey, BuiltinPublicKeyID, BuiltinPublicKeys)
		if err != nil {
			return Verifier{}, TrustAnchorMissing, err
		}
		return verifier, TrustAnchorBuiltin, nil
	}
	if allowConfigured {
		if strings.TrimSpace(configuredKey) == "" && strings.TrimSpace(configuredKeys) == "" {
			return Verifier{}, TrustAnchorMissing, ErrTrustAnchor
		}
		verifier, err := buildVerifier(configuredKey, "", configuredKeys)
		if err != nil {
			return Verifier{}, TrustAnchorMissing, err
		}
		return verifier, TrustAnchorConfig, nil
	}
	return Verifier{}, TrustAnchorMissing, ErrTrustAnchor
}

type parsedKey struct {
	id  string
	key ed25519.PublicKey
}

func buildVerifier(primaryKey, primaryID, ringSpec string) (Verifier, error) {
	parsed, err := parseKeyRing(ringSpec)
	if err != nil {
		return Verifier{}, err
	}
	keys := map[string]ed25519.PublicKey{}
	for _, item := range parsed {
		if item.id != "" {
			keys[item.id] = item.key
		}
	}
	verifier := Verifier{Keys: keys}
	if strings.TrimSpace(primaryKey) != "" {
		key, err := ParsePublicKey(primaryKey)
		if err != nil {
			return Verifier{}, err
		}
		verifier.PublicKey = key
		verifier.KeyID = strings.TrimSpace(primaryID)
		if verifier.KeyID != "" {
			keys[verifier.KeyID] = key
		}
		return verifier, nil
	}
	if len(parsed) == 0 {
		return Verifier{}, ErrTrustAnchor
	}
	verifier.PublicKey = parsed[0].key
	verifier.KeyID = parsed[0].id
	return verifier, nil
}

// parseKeyRing parses "kid:base64,kid2:base64". A bare base64 key is accepted
// only as a single default entry. Base64url contains no colon, so the separator
// is unambiguous even when a key is padded.
func parseKeyRing(spec string) ([]parsedKey, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	entries := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	parsed := make([]parsedKey, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id := ""
		keyText := entry
		if index := strings.Index(entry, ":"); index >= 0 {
			id = strings.TrimSpace(entry[:index])
			keyText = strings.TrimSpace(entry[index+1:])
		}
		key, err := ParsePublicKey(keyText)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", err, id)
		}
		parsed = append(parsed, parsedKey{id: id, key: key})
	}
	return parsed, nil
}

// ConfigKeyAllowed reports whether a deployment may fall back to its configured
// public key. Only explicit development/test environments or an operator
// migration switch may do so.
func ConfigKeyAllowed(env string, explicit bool) bool {
	if explicit {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "development", "dev", "test", "testing":
		return true
	default:
		return false
	}
}
