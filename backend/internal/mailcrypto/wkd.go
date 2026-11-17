package mailcrypto

import (
	"bytes"
	"crypto/sha1" // #nosec G505 -- WKD requires SHA-1 for the local-part hash.
	"encoding/hex"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// WKDURLs returns the direct and advanced Web Key Directory URLs for an email
// address. The caller must fetch only HTTPS URLs and validate the returned key
// against the address (see KeyMatchesEmail).
func WKDURLs(address string) (direct, advanced string, err error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil || parsed == nil {
		return "", "", fmt.Errorf("invalid email address")
	}
	local, domain, ok := strings.Cut(parsed.Address, "@")
	if !ok || local == "" || domain == "" {
		return "", "", fmt.Errorf("invalid email address")
	}
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if strings.ContainsAny(domain, "/\\ \t\r\n") || strings.ContainsAny(local, "\r\n") {
		return "", "", fmt.Errorf("invalid email address")
	}
	hash := wkdLocalHash(local)
	direct = "https://openpgpkey." + domain + "/.well-known/openpgpkey/" + domain + "/hu/" + hash
	advanced = "https://" + domain + "/.well-known/openpgpkey/hu/" + hash
	return direct, advanced, nil
}

// WKDLocalPartHash returns the z-base-32 SHA-1 hash used by WKD for a
// lowercase local part. It is exported so publication handlers can verify the
// request path against the requested address.
func WKDLocalPartHash(local string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(local))))
	return zbase32(sum[:])
}

func wkdLocalHash(local string) string { return WKDLocalPartHash(local) }

// PublicKeyBinary serialises the public half of an OpenPGP entity as the
// binary keyring WKD expects.
func PublicKeyBinary(entity *openpgp.Entity) ([]byte, error) {
	if entity == nil {
		return nil, fmt.Errorf("OpenPGP key is missing")
	}
	var output bytes.Buffer
	if err := entity.Serialize(&output); err != nil {
		return nil, fmt.Errorf("serialize OpenPGP key: %w", err)
	}
	return output.Bytes(), nil
}

// zbase32 implements the z-base-32 alphabet required by Web Key Directory.
func zbase32(data []byte) string {
	const alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"
	var output strings.Builder
	var bits uint64
	var bitCount uint
	for _, value := range data {
		bits = (bits << 8) | uint64(value)
		bitCount += 8
		for bitCount >= 5 {
			bitCount -= 5
			output.WriteByte(alphabet[(bits>>bitCount)&31])
		}
	}
	if bitCount > 0 {
		output.WriteByte(alphabet[(bits<<(5-bitCount))&31])
	}
	return output.String()
}

// ParseWKDResponse parses either a binary or armored WKD key response.
func ParseWKDResponse(data []byte) ([]*openpgp.Entity, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("WKD response is empty")
	}
	entities, err := openpgp.ReadKeyRing(bytes.NewReader(data))
	if err == nil && len(entities) > 0 {
		return entities, nil
	}
	entities, armorErr := openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	if armorErr == nil && len(entities) > 0 {
		return entities, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse WKD key: %w", err)
	}
	return nil, fmt.Errorf("parse WKD armored key: %w", armorErr)
}

// KeyMatchesEmail verifies that the key carries the requested email identity.
func KeyMatchesEmail(entity *openpgp.Entity, address string) bool {
	if entity == nil {
		return false
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil || parsed == nil {
		return false
	}
	for _, identity := range entity.Identities {
		if identity == nil || identity.UserId == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(identity.UserId.Email), parsed.Address) {
			return true
		}
	}
	return false
}

// OpenPGPKeyUsableForEncryption reports whether the entity can encrypt to the
// given address now: the address identity and primary key must not be revoked,
// and a non-expired encryption key must be available. It is used before a
// public key is published or returned to another browser.
func OpenPGPKeyUsableForEncryption(entity *openpgp.Entity, address string, now time.Time) bool {
	if entity == nil || !KeyMatchesEmail(entity, address) {
		return false
	}
	if entity.Revoked(now) {
		return false
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(address))
	if err != nil || parsed == nil {
		return false
	}
	for _, identity := range entity.Identities {
		if identity == nil || identity.UserId == nil ||
			!strings.EqualFold(strings.TrimSpace(identity.UserId.Email), parsed.Address) {
			continue
		}
		if identity.Revoked(now) {
			return false
		}
	}
	_, ok := entity.EncryptionKey(now)
	return ok
}

// OpenPGPKeyInfo is the public metadata stored for a discovered key.
type OpenPGPKeyInfo struct {
	KeyID       string
	Fingerprint string
	Email       string
	Name        string
	Armored     string
	NotAfter    string
}

func OpenPGPInfo(entity *openpgp.Entity, address string) (OpenPGPKeyInfo, error) {
	if entity == nil || entity.PrimaryKey == nil {
		return OpenPGPKeyInfo{}, fmt.Errorf("OpenPGP key has no primary key")
	}
	if !KeyMatchesEmail(entity, address) {
		return OpenPGPKeyInfo{}, fmt.Errorf("the key does not contain the requested email identity")
	}
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	keyID := fingerprint
	if len(keyID) > 16 {
		keyID = keyID[len(keyID)-16:]
	}
	armored, err := armoredEntity(entity, false)
	if err != nil {
		return OpenPGPKeyInfo{}, err
	}
	name := ""
	for _, identity := range entity.Identities {
		if identity != nil && identity.UserId != nil && strings.EqualFold(identity.UserId.Email, address) {
			name = identity.UserId.Name
			break
		}
	}
	return OpenPGPKeyInfo{KeyID: keyID, Fingerprint: fingerprint, Email: address, Name: name, Armored: armored}, nil
}

// WKDDirectHost returns the host that must be resolved for the direct method.
func WKDDirectHost(domain string) string {
	return "openpgpkey." + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}
