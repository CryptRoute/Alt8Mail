package mailcrypto

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

func generatedSMIMECertificate(t *testing.T, email string, notBefore, notAfter time.Time, parent *x509.Certificate, parentKey *rsa.PrivateKey, isCA bool) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: email, Organization: []string{"Alt8Mail Test"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses:        []string{email},
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	if isCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		template.ExtKeyUsage = nil
		template.EmailAddresses = nil
		template.Subject = pkix.Name{CommonName: "Alt8Mail Test CA"}
	}
	issuer := template
	signer := key
	if parent != nil {
		issuer = parent
		signer = parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func generatedSMIMEIdentity(t *testing.T, email string, now time.Time) (*SMIMEIdentity, *x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	cert, key := generatedSMIMECertificate(t, email, now.Add(-time.Hour), now.Add(24*time.Hour), nil, nil, false)
	identity, err := newSMIMEIdentity(cert, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	return identity, cert, key
}

func TestSMIMESignVerifyRoundTrip(t *testing.T) {
	now := time.Now()
	identity, cert, _ := generatedSMIMEIdentity(t, "sender@example.test", now)
	mime := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nSigned body.\r\n")
	signed, err := SignSMIMEMIME(mime, identity)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Analyze(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Protection.Signed || inspection.Protection.Protocol != ProtocolSMIME ||
		inspection.Protection.Kind != KindSMIMESigned {
		t.Fatalf("protection = %#v", inspection.Protection)
	}
	status, _, err := VerifySMIMESignature(signed, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SignatureValidUntrusted {
		t.Fatalf("signature state = %q, reason = %q", status.State, status.Reason)
	}
	if status.Fingerprint != SMIMEFingerprint(cert) || status.SignerEmail != "sender@example.test" {
		t.Fatalf("signature status = %#v", status)
	}
}

func TestSMIMEEncryptDecryptRoundTrip(t *testing.T) {
	now := time.Now()
	recipient, recipientCert, _ := generatedSMIMEIdentity(t, "recipient@example.test", now)
	sender, _, _ := generatedSMIMEIdentity(t, "sender@example.test", now)
	mime := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nSecret body.\r\n")
	encrypted, err := EncryptSMIMEMIME(mime, []*x509.Certificate{recipientCert}, sender, false, true)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Analyze(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Protection.Encrypted || inspection.Protection.Protocol != ProtocolSMIME {
		t.Fatalf("protection = %#v", inspection.Protection)
	}
	plain, status, _, err := DecryptSMIMEMIME(encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, mime) {
		t.Fatalf("decrypted body = %q, want %q", plain, mime)
	}
	if status.State != SignatureNone {
		t.Fatalf("encrypted-only signature = %#v", status)
	}
}

func TestSMIMESignThenEncryptRoundTrip(t *testing.T) {
	now := time.Now()
	recipient, recipientCert, _ := generatedSMIMEIdentity(t, "recipient@example.test", now)
	sender, senderCert, _ := generatedSMIMEIdentity(t, "sender@example.test", now)
	mime := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nSigned and encrypted.\r\n")
	encrypted, err := EncryptSMIMEMIME(mime, []*x509.Certificate{recipientCert}, sender, true, true)
	if err != nil {
		t.Fatal(err)
	}
	plain, status, _, err := DecryptSMIMEMIME(encrypted, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("multipart/signed")) {
		t.Fatalf("decrypted inner entity = %q", plain)
	}
	if status.State != SignatureValidUntrusted || status.Fingerprint != SMIMEFingerprint(senderCert) {
		t.Fatalf("inner signature = %#v", status)
	}
}

func TestSMIMECertificateChainAndExpiry(t *testing.T) {
	now := time.Now()
	root, rootKey := generatedSMIMECertificate(t, "root@example.test", now.Add(-time.Hour), now.Add(48*time.Hour), nil, nil, true)
	leaf, leafKey := generatedSMIMECertificate(t, "alice@example.test", now.Add(-time.Hour), now.Add(24*time.Hour), root, rootKey, false)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	check := CheckSMIMECertificate(leaf, []*x509.Certificate{root}, roots, now, "alice@example.test", SMIMEUsageSign)
	if !check.Valid || !check.ChainValid {
		t.Fatalf("leaf check = %#v", check)
	}
	identity, err := newSMIMEIdentity(leaf, []*x509.Certificate{root}, leafKey)
	if err != nil {
		t.Fatal(err)
	}
	mime := []byte("Content-Type: text/plain\r\n\r\nChained\r\n")
	signed, err := SignSMIMEMIME(mime, identity)
	if err != nil {
		t.Fatal(err)
	}
	status, warnings, err := VerifySMIMESignature(signed, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SignatureValidUntrusted || len(warnings) != 0 {
		t.Fatalf("status = %#v, warnings = %#v", status, warnings)
	}
	expired := CheckSMIMECertificate(leaf, []*x509.Certificate{root}, roots, now.Add(72*time.Hour), "alice@example.test", SMIMEUsageSign)
	if expired.Valid || !strings.Contains(expired.Reason, "expired") {
		t.Fatalf("expired check = %#v", expired)
	}
	wrongEmail := CheckSMIMECertificate(leaf, []*x509.Certificate{root}, roots, now, "bob@example.test", SMIMEUsageSign)
	if wrongEmail.Valid {
		t.Fatalf("wrong-email check = %#v", wrongEmail)
	}
}

func TestSMIMEPKCS12Import(t *testing.T) {
	now := time.Now()
	root, rootKey := generatedSMIMECertificate(t, "root@example.test", now.Add(-time.Hour), now.Add(48*time.Hour), nil, nil, true)
	leaf, leafKey := generatedSMIMECertificate(t, "alice@example.test", now.Add(-time.Hour), now.Add(24*time.Hour), root, rootKey, false)
	pfx, err := pkcs12.Modern.Encode(leafKey, leaf, []*x509.Certificate{root}, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ParsePKCS12Identity(pfx, []byte("test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "alice@example.test" || identity.Fingerprint != SMIMEFingerprint(leaf) {
		t.Fatalf("identity = %#v", identity)
	}
	if len(identity.Chain) != 1 || !bytes.Equal(identity.Chain[0].Raw, root.Raw) {
		t.Fatalf("chain = %#v", identity.Chain)
	}
	if len(identity.PrivateKeyPEM) == 0 || len(identity.CertificatePEM) == 0 {
		t.Fatal("identity PEM material is empty")
	}
	if _, err := ParsePKCS12Identity(pfx, []byte("wrong")); err == nil {
		t.Fatal("wrong PKCS#12 passphrase was accepted")
	}
}

func TestSMIMEProviderInterface(t *testing.T) {
	now := time.Now()
	sender, senderCert, _ := generatedSMIMEIdentity(t, "sender@example.test", now)
	recipient, recipientCert, _ := generatedSMIMEIdentity(t, "recipient@example.test", now)
	provider := SMIMEProvider{}
	mime := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nProvider body.\r\n")
	encrypted, err := provider.Encrypt(context.Background(), EncryptRequest{
		Identity:   IdentityRef{Email: sender.Email, KeyID: sender.Fingerprint},
		PrivateKey: sender.PrivateKeyPEM, Certificate: sender.CertificatePEM,
		CertificateChain: [][]byte{sender.ChainPEM},
		Recipients: []RecipientKey{{
			Email: recipient.Email, Protocol: ProtocolSMIME, KeyID: recipient.Fingerprint,
			Fingerprint: recipient.Fingerprint, PublicKey: certificatePEM(recipientCert),
			Trust: TrustVerified,
		}},
		MIME: mime, Sign: true, EncryptToSelf: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := provider.Decrypt(context.Background(), DecryptRequest{
		Identity:   IdentityRef{Email: recipient.Email, KeyID: recipient.Fingerprint},
		PrivateKey: recipient.PrivateKeyPEM, Certificate: recipient.CertificatePEM,
		CertificateChain: [][]byte{recipient.ChainPEM}, Message: encrypted.MIME,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decrypted.Signature.State != SignatureValidUntrusted || decrypted.Signature.Fingerprint != sender.Fingerprint {
		t.Fatalf("decrypted signature = %#v", decrypted.Signature)
	}
	signed, err := provider.Sign(context.Background(), SignRequest{
		Identity:   IdentityRef{Email: sender.Email, KeyID: sender.Fingerprint},
		PrivateKey: sender.PrivateKeyPEM, Certificate: sender.CertificatePEM,
		CertificateChain: [][]byte{sender.ChainPEM}, MIME: mime,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := provider.Verify(context.Background(), VerifyRequest{
		Protocol: ProtocolSMIME, PublicKey: certificatePEM(senderCert), Message: signed.MIME,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SignatureValidUntrusted || status.Fingerprint != sender.Fingerprint {
		t.Fatalf("provider verify = %#v", status)
	}
}

func certificatePEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

func TestSMIMEInteropWithOpenSSL(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed")
	}
	now := time.Now()
	identity, cert, _ := generatedSMIMEIdentity(t, "alice@example.test", now)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certificatePEM(cert), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, identity.PrivateKeyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	mime := []byte("Content-Type: text/plain; charset=UTF-8\r\n\r\nOpenSSL interop body.\r\n")
	signed, err := SignSMIMEMIME(mime, identity)
	if err != nil {
		t.Fatal(err)
	}
	signedFile := filepath.Join(dir, "signed.eml")
	verifiedFile := filepath.Join(dir, "verified.txt")
	if err := os.WriteFile(signedFile, signed, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(openssl, "smime", "-verify", "-in", signedFile, "-noverify", "-out", verifiedFile)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("openssl could not verify our S/MIME signature: %v\n%s", err, output)
	}
	verified, err := os.ReadFile(verifiedFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(verified, []byte("OpenSSL interop body.")) {
		t.Fatalf("verified content = %q", verified)
	}
	encrypted, err := EncryptSMIMEMIME(mime, []*x509.Certificate{cert}, identity, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encryptedFile := filepath.Join(dir, "encrypted.eml")
	decryptedFile := filepath.Join(dir, "decrypted.txt")
	if err := os.WriteFile(encryptedFile, encrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(openssl, "smime", "-decrypt", "-in", encryptedFile, "-recip", certFile, "-inkey", keyFile, "-out", decryptedFile)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("openssl could not decrypt our S/MIME envelope: %v\n%s", err, output)
	}
	decrypted, err := os.ReadFile(decryptedFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(decrypted, []byte("OpenSSL interop body.")) {
		t.Fatalf("decrypted content = %q", decrypted)
	}
}

func TestSMIMEPEMIdentityImport(t *testing.T) {
	now := time.Now()
	identity, cert, _ := generatedSMIMEIdentity(t, "alice@example.test", now)
	imported, err := ParseSMIMEIdentity(identity.CertificatePEM, identity.PrivateKeyPEM, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Fingerprint != SMIMEFingerprint(cert) || imported.Email != "alice@example.test" {
		t.Fatalf("imported identity = %#v", imported)
	}
	if !bytes.Equal(imported.PrivateKeyPEM, identity.PrivateKeyPEM) {
		t.Fatal("private key changed during PEM round trip")
	}
}
