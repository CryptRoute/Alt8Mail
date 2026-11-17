package mailcrypto

import (
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestWKDURLsKnownVector(t *testing.T) {
	direct, advanced, err := WKDURLs("Joe.Doe@Example.ORG")
	if err != nil {
		t.Fatal(err)
	}
	wantHash := "iy9q119eutrkn8s1mk4r39qejnbu3n5q"
	if direct != "https://openpgpkey.example.org/.well-known/openpgpkey/example.org/hu/"+wantHash {
		t.Fatalf("direct = %q", direct)
	}
	if advanced != "https://example.org/.well-known/openpgpkey/hu/"+wantHash {
		t.Fatalf("advanced = %q", advanced)
	}
}

func TestWKDRejectsInvalidAddress(t *testing.T) {
	for _, address := range []string{"", "not-an-email", "user@", "@example.test", "user@exam ple.test"} {
		if _, _, err := WKDURLs(address); err == nil {
			t.Fatalf("accepted invalid address %q", address)
		}
	}
}

func TestWKDKeyMatchesEmail(t *testing.T) {
	key, err := GenerateOpenPGPKey("Alice", "alice@example.test")
	if err != nil {
		t.Fatal(err)
	}
	entities, err := ParseWKDResponse([]byte(key.ArmoredPublic))
	if err != nil {
		t.Fatal(err)
	}
	if !KeyMatchesEmail(entities[0], "alice@example.test") {
		t.Fatal("key did not match its own email")
	}
	if KeyMatchesEmail(entities[0], "mallory@example.test") {
		t.Fatal("key matched a different email")
	}
	info, err := OpenPGPInfo(entities[0], "alice@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != key.Fingerprint || info.KeyID != key.KeyID {
		t.Fatalf("info = %#v, key = %#v", info, key)
	}
	if _, err := OpenPGPInfo(entities[0], "mallory@example.test"); err == nil {
		t.Fatal("OpenPGPInfo accepted the wrong email")
	}
}

func TestOpenPGPKeyUsableForEncryption(t *testing.T) {
	key, err := GenerateOpenPGPKey("Alice", "alice@example.test")
	if err != nil {
		t.Fatal(err)
	}
	entities, err := ParseWKDResponse([]byte(key.ArmoredPublic))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !OpenPGPKeyUsableForEncryption(entities[0], "alice@example.test", now) {
		t.Fatal("fresh key was not usable for encryption")
	}
	if OpenPGPKeyUsableForEncryption(entities[0], "mallory@example.test", now) {
		t.Fatal("key was usable for an address it does not carry")
	}
	expired, err := openpgp.NewEntity("Expired", "", "expired@example.test", &packet.Config{
		RSABits:         2048,
		Time:            func() time.Time { return now.Add(-48 * time.Hour) },
		KeyLifetimeSecs: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if OpenPGPKeyUsableForEncryption(expired, "expired@example.test", now) {
		t.Fatal("expired key was usable for encryption")
	}
}
