package control

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestReplaceNginxDirective(t *testing.T) {
	content := `server {
    listen 443 ssl;
    ssl_certificate     /etc/letsencrypt/live/example/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
}
`
	updated, count := replaceNginxDirective(content, "ssl_certificate", "/etc/ssl/alt8mail/imported/import-example.crt")
	if count != 1 {
		t.Fatalf("expected one certificate replacement, got %d", count)
	}
	if !strings.Contains(updated, "ssl_certificate     /etc/ssl/alt8mail/imported/import-example.crt;") {
		t.Fatalf("certificate path was not replaced:\n%s", updated)
	}
	if !strings.Contains(updated, "ssl_certificate_key /etc/letsencrypt/live/example/privkey.pem;") {
		t.Fatalf("certificate key path should not have been changed by the certificate directive:\n%s", updated)
	}
	updated, count = replaceNginxDirective(updated, "ssl_certificate_key", "/etc/ssl/alt8mail/imported/import-example.key")
	if count != 1 || !strings.Contains(updated, "ssl_certificate_key /etc/ssl/alt8mail/imported/import-example.key;") {
		t.Fatalf("key path was not replaced:\n%s", updated)
	}
}

func TestNginxTLSPaths(t *testing.T) {
	cert, key := nginxTLSPaths(`server {
  ssl_certificate "/etc/ssl/example/fullchain.pem";
  ssl_certificate_key '/etc/ssl/example/privkey.pem';
}`)
	if cert != "/etc/ssl/example/fullchain.pem" || key != "/etc/ssl/example/privkey.pem" {
		t.Fatalf("unexpected paths: %q %q", cert, key)
	}
}

func TestCertificateAndKeyPairParsing(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "mail.example.test"},
		DNSNames:     []string{"mail.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	certs, _, err := parseCertificateChain(certPEM)
	if err != nil || len(certs) != 1 {
		t.Fatalf("parse chain: %v (certs=%d)", err, len(certs))
	}
	signer, _, err := parseTLSPrivateKey(keyPEM, "")
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if !publicKeysEqual(certs[0].PublicKey, signer.Public()) {
		t.Fatal("parsed key does not match certificate")
	}
	if !certificateMatchesHost(certs[0], "mail.example.test") {
		t.Fatal("expected hostname to match certificate SAN")
	}
	if certificateMatchesHost(certs[0], "other.example.test") {
		t.Fatal("unexpected hostname match")
	}

	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	if publicKeysEqual(certs[0].PublicKey, otherKey.Public()) {
		t.Fatal("different keys should not compare equal")
	}
}

func TestParseTLSPrivateKeyRejectsBadInput(t *testing.T) {
	if _, _, err := parseTLSPrivateKey("not a key", ""); err == nil {
		t.Fatal("expected an invalid private key error")
	}
	if _, _, err := parseCertificateChain("not a certificate"); err == nil {
		t.Fatal("expected an invalid certificate error")
	}
}
