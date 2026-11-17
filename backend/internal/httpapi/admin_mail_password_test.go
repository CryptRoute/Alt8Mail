package httpapi

import (
	"strings"
	"testing"
)

func TestHashMailPasswordUsesDovecotSHA512CryptFormat(t *testing.T) {
	hash, err := hashMailPassword("TestPass123!")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "{SHA512-CRYPT}$6$") {
		t.Fatalf("unexpected Dovecot hash format: %q", hash)
	}
	if _, err := hashMailPassword("line\nbreak"); err == nil {
		t.Fatal("passwords containing line breaks must be rejected")
	}
}
