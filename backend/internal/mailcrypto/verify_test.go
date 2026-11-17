package mailcrypto

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func TestPGPMIMESignatureVerifiesAgainstRawEntity(t *testing.T) {
	dir := filepath.Join("testdata", "fixtures")
	signedRaw, err := os.ReadFile(filepath.Join(dir, "pgp-mime-signed.eml"))
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(signedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.SignedContent == nil || analysis.Protection.Signature == nil {
		t.Fatal("signed parts missing")
	}
	keyRaw, err := os.ReadFile(filepath.Join(dir, "pgp-key-block.eml"))
	if err != nil {
		t.Fatal(err)
	}
	keyAnalysis, err := Analyze(keyRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyAnalysis.Parts) == 0 {
		t.Fatal("key fixture has no parts")
	}
	keyring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(keyAnalysis.Parts[0].RawBody))
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	signer, err := openpgp.CheckArmoredDetachedSignature(
		keyring,
		bytes.NewReader(SignedEntity(analysis.Protection.SignedContent)),
		bytes.NewReader(analysis.Protection.Signature.RawBody),
		nil,
	)
	if err != nil {
		t.Fatalf("PGP/MIME signature did not verify against the preserved entity: %v", err)
	}
	if signer == nil || len(signer.Identities) == 0 {
		t.Fatal("verified signer has no identity")
	}
}

func TestSMIMESignatureVerifiesWithOpenSSL(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed")
	}
	fixture := filepath.Join("testdata", "fixtures", "smime-signed.eml")
	output := filepath.Join(t.TempDir(), "verified.txt")
	command := exec.Command(openssl, "smime", "-verify", "-in", fixture, "-noverify", "-out", output)
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("openssl could not verify the S/MIME fixture: %v\n%s", err, data)
	}
	verified, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(verified, []byte("Alt8Mail Phase 0 interop fixture.")) {
		t.Fatalf("verified content = %q", verified)
	}
}
