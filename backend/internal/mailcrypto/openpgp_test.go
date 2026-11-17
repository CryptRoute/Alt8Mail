package mailcrypto

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func testMIMEEntity(body string) []byte {
	return []byte("Content-Type: text/plain; charset=UTF-8\r\nMIME-Version: 1.0\r\n\r\n" + body + "\r\n")
}

func TestOpenPGPEncryptDecryptRoundTrip(t *testing.T) {
	recipient, err := GenerateOpenPGPKey("Recipient", "recipient@example.test")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := testMIMEEntity("Top secret phase one body.")
	encrypted, err := EncryptPGPMIME(plaintext, []*openpgp.Entity{recipient.Entity}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Protocol != ProtocolOpenPGP {
		t.Fatalf("wrong protection: %#v", analysis.Protection)
	}
	decrypted, status, err := DecryptPGPMIME(encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted MIME differs:\n%q\n%q", decrypted, plaintext)
	}
	if status.State != SignatureNone {
		t.Fatalf("unexpected signature status: %#v", status)
	}
}

func TestOpenPGPSignEncryptDecryptSignature(t *testing.T) {
	sender, err := GenerateOpenPGPKey("Sender", "sender@example.test")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := GenerateOpenPGPKey("Recipient", "recipient@example.test")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := testMIMEEntity("Signed and encrypted.")
	encrypted, err := EncryptPGPMIME(plaintext, []*openpgp.Entity{recipient.Entity}, sender.Entity, true)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, status, err := DecryptPGPMIME(encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatal("decrypted content mismatch")
	}
	if status.State != SignatureValidUntrusted {
		t.Fatalf("signature state = %q (%s)", status.State, status.Reason)
	}
	if status.KeyID == "" {
		t.Fatal("signature status did not expose the signer key id")
	}
}

func TestOpenPGPSignAndVerify(t *testing.T) {
	sender, err := GenerateOpenPGPKey("Sender", "sender@example.test")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := testMIMEEntity("This is signed only.")
	signed, err := SignPGPMIME(plaintext, sender.Entity)
	if err != nil {
		t.Fatal(err)
	}
	status, err := VerifyPGPMIMESignature(signed, []*openpgp.Entity{sender.Entity})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SignatureValidUntrusted {
		t.Fatalf("state = %q", status.State)
	}
	if strings.ToUpper(status.Fingerprint) != sender.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", status.Fingerprint, sender.Fingerprint)
	}
}

func TestOpenPGPProviderInterface(t *testing.T) {
	sender, err := GenerateOpenPGPKey("Sender", "sender@example.test")
	if err != nil {
		t.Fatal(err)
	}
	provider := OpenPGPProvider{}
	result, err := provider.Sign(context.Background(), SignRequest{
		Identity: IdentityRef{Email: "sender@example.test"}, PrivateKey: []byte(sender.ArmoredPrivate),
		MIME: testMIMEEntity("Provider signed."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != ProtocolOpenPGP || len(result.MIME) == 0 {
		t.Fatalf("sign result = %#v", result)
	}
	status, err := provider.Verify(context.Background(), VerifyRequest{
		Protocol: ProtocolOpenPGP, PublicKey: []byte(sender.ArmoredPublic), Message: result.MIME,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SignatureValidUntrusted {
		t.Fatalf("verify state = %q", status.State)
	}
}
