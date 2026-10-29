package control

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// secretBox encrypts panel-held credentials with AES-256-GCM. The key is
// derived from CONTROL_SECRET_KEY, which is separate from webmail secrets.
type secretBox struct {
	aead cipher.AEAD
}

func newSecretBox(secret string) (*secretBox, error) {
	if len(strings.TrimSpace(secret)) < 32 {
		return nil, fmt.Errorf("panel secret must be at least 32 characters")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretBox{aead: aead}, nil
}

func (s *secretBox) Seal(plain string) (string, error) {
	if strings.TrimSpace(plain) == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nil, nonce, []byte(plain), nil)
	return base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (s *secretBox) Open(encoded string) (string, error) {
	if strings.TrimSpace(encoded) == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", fmt.Errorf("invalid sealed value")
	}
	nonce := raw[:s.aead.NonceSize()]
	plain, err := s.aead.Open(nil, nonce, raw[s.aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("could not decrypt panel secret")
	}
	return string(plain), nil
}
