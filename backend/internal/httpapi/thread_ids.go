package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const threadIDBytes = 16

// newThreadID returns a random, opaque, URL-safe thread identifier. The
// 16-byte payload encodes to 22 base64url characters with no padding, and
// deliberately carries no information about where the thread came from.
func newThreadID() string {
	var payload [threadIDBytes]byte
	if _, err := rand.Read(payload[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload[:])
}

// opaqueThreadID derives the same kind of identifier from a stable thread
// root. It is used for imported mail so a crash or replay during the first
// import produces the same ID rather than a duplicate conversation.
func opaqueThreadID(root string) string {
	digest := sha256.Sum256([]byte(root))
	return base64.RawURLEncoding.EncodeToString(digest[:threadIDBytes])
}

// normalizeThreadID maps a legacy value arriving from an old browser tab or
// API client to its canonical opaque form. Current IDs and empty values pass
// through unchanged.
func normalizeThreadID(value string) string {
	value = strings.TrimSpace(value)
	if isLegacyThreadID(value) {
		return opaqueThreadID(value)
	}
	return value
}

// isLegacyThreadID recognizes the two identifiers used before 0.6.0:
// "t_" plus the 16-character random payload from newID("t"), and
// "t_imap_" plus the 16-character hex hash from hashShort. Legacy deep links
// can be translated into the opaque format without touching new IDs, which are
// always 22 characters and carry no origin prefix.
func isLegacyThreadID(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "t_imap_") {
		suffix := value[len("t_imap_"):]
		return len(suffix) == 16 && isHex(suffix)
	}
	if strings.HasPrefix(value, "t_") {
		suffix := value[len("t_"):]
		return len(suffix) == 16 && isBase64URLText(suffix)
	}
	return false
}

func isHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return value != ""
}

func isBase64URLText(value string) bool {
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return value != ""
}
