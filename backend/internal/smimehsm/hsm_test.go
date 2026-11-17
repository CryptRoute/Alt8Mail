package smimehsm

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type fakeSession struct {
	lastSigned  []byte
	lastDecrypt []byte
}

func (s *fakeSession) Sign(value []byte) ([]byte, error) {
	s.lastSigned = append([]byte{}, value...)
	return []byte("signature"), nil
}

func (s *fakeSession) Decrypt(value []byte) ([]byte, error) {
	s.lastDecrypt = append([]byte{}, value...)
	return []byte("plaintext"), nil
}

func (s *fakeSession) Close() {}

func TestTokenSignerBuildsDigestInfo(t *testing.T) {
	certificate := &x509.Certificate{}
	session := &fakeSession{}
	signer := &tokenSigner{certificate: certificate, session: session}
	digest := sha256.Sum256([]byte("payload"))
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if string(signature) != "signature" {
		t.Fatalf("signature = %q", signature)
	}
	prefix, _ := hex.DecodeString(digestPrefixes[crypto.SHA256])
	expected := append(prefix, digest[:]...)
	if !bytes.Equal(session.lastSigned, expected) {
		t.Fatalf("token input was not a SHA-256 DigestInfo")
	}
	if _, err := signer.Sign(rand.Reader, digest[:8], crypto.SHA256); err == nil {
		t.Fatal("a truncated digest must be rejected")
	}
	plain, err := signer.Decrypt(rand.Reader, []byte("ciphertext"), nil)
	if err != nil || string(plain) != "plaintext" || string(session.lastDecrypt) != "ciphertext" {
		t.Fatalf("token decrypt = %q err=%v", plain, err)
	}
}

func TestProviderRejectsInvalidConfiguration(t *testing.T) {
	if _, err := OpenJSON("{"); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if _, err := Open(Config{}); err == nil {
		t.Fatal("a provider without a module path was accepted")
	}
}

type localSigner struct{ key *rsa.PrivateKey }

func (s localSigner) Public() crypto.PublicKey { return &s.key.PublicKey }
func (s localSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.key.Sign(rand.Reader, digest, opts)
}
func (s localSigner) Decrypt(_ io.Reader, ciphertext []byte, opts crypto.DecrypterOpts) ([]byte, error) {
	return s.key.Decrypt(rand.Reader, ciphertext, opts)
}

func TestSMIMEIdentityUsesDecrypter(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:   big.NewInt(42),
		Subject:        pkix.Name{CommonName: "HSM Test"},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses: []string{"hsm@example.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	identity := mailcrypto.SMIMEIdentityForSigner(certificate, nil, localSigner{key: key})
	if identity == nil || identity.Decrypter == nil {
		t.Fatal("the signer was not exposed as a decrypter")
	}
	entity := []byte("Content-Type: text/plain\r\n\r\nHSM plaintext marker\r\n")
	encrypted, err := mailcrypto.EncryptSMIMEMIME(entity, []*x509.Certificate{certificate}, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	plain, _, _, err := mailcrypto.DecryptSMIMEMIME(encrypted, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "HSM plaintext marker") {
		t.Fatalf("decrypted entity = %q", plain)
	}
	signed, err := mailcrypto.SignSMIMEMIME(entity, identity)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := mailcrypto.VerifySMIMESignature(signed, nil, time.Now())
	if err != nil || status.State != mailcrypto.SignatureValidUntrusted {
		t.Fatalf("signature status=%#v err=%v", status, err)
	}
}
