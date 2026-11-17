package control

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCertificate(t *testing.T, dir, name string, template, parent *x509.Certificate, publicKey any, signer any) {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, signer)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), block, 0o644); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("test key placeholder")})
	if err := os.WriteFile(filepath.Join(dir, name+".key"), key, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

func TestReadCertificateDetectsSelfSignedLeaf(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "panel.example.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"panel.example.test"},
	}
	writeTestCertificate(t, dir, "panel", template, template, &key.PublicKey, key)
	info, err := readCertificate(filepath.Join(dir, "panel.crt"), filepath.Join(dir, "panel.key"), "panel")
	if err != nil {
		t.Fatalf("read self-signed: %v", err)
	}
	if !info.SelfSigned {
		t.Fatal("self-issued server certificate was not detected as self-signed")
	}
	if !info.HasKey {
		t.Fatal("key file should be reported present")
	}
	if len(info.SANs) != 1 || info.SANs[0] != "panel.example.test" {
		t.Fatalf("unexpected SANs: %v", info.SANs)
	}
}

func TestReadCertificateDoesNotMarkCAIssuedLeafSelfSigned(t *testing.T) {
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "mail.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"mail.example.test"},
	}
	writeTestCertificate(t, dir, "mail", leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	info, err := readCertificate(filepath.Join(dir, "mail.crt"), filepath.Join(dir, "mail.key"), "mail")
	if err != nil {
		t.Fatalf("read CA-issued: %v", err)
	}
	if info.SelfSigned {
		t.Fatal("CA-issued certificate must not be marked self-signed")
	}
}
