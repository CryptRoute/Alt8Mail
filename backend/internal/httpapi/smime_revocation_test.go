package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type revocationFixture struct {
	ca      *x509.Certificate
	caKey   *rsa.PrivateKey
	leaf    *x509.Certificate
	leafKey *rsa.PrivateKey
	now     time.Time
}

func newRevocationFixture(t *testing.T) revocationFixture {
	t.Helper()
	now := time.Now().Add(-time.Hour)
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Alt8Mail Revocation Test CA"},
		NotBefore:             now,
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return revocationFixture{ca: ca, caKey: caKey, leafKey: leafKey, now: time.Now()}
}

func (f revocationFixture) leafWith(t *testing.T, ocspURL, crlURL string) *x509.Certificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Revocation Test User"},
		NotBefore:             f.now.Add(-time.Hour),
		NotAfter:              f.now.Add(24 * time.Hour),
		EmailAddresses:        []string{"revocation@example.test"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		OCSPServer:            []string{ocspURL},
		CRLDistributionPoints: []string{crlURL},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, f.ca, &f.leafKey.PublicKey, f.caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestSMIMERevocationRevokedOCSP(t *testing.T) {
	fixture := newRevocationFixture(t)
	var leaf *x509.Certificate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		template := ocsp.Response{
			Status: ocsp.Revoked, SerialNumber: leaf.SerialNumber,
			ThisUpdate: time.Now().Add(-time.Minute), NextUpdate: time.Now().Add(time.Hour),
			RevokedAt: time.Now().Add(-time.Minute), RevocationReason: ocsp.KeyCompromise,
		}
		der, err := ocsp.CreateResponse(fixture.ca, fixture.ca, template, fixture.caKey)
		if err != nil {
			t.Errorf("create response: %v", err)
			http.Error(w, "failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(der)
	}))
	defer server.Close()
	leaf = fixture.leafWith(t, server.URL, "")
	api := &Server{cfg: config.Config{SMIMERevocationMode: "require", SMIMERevocationAllowPrivate: true, SMIMERevocationCache: time.Minute}}
	status, reason := api.smimeRevocationStatus(context.Background(), leaf, []*x509.Certificate{fixture.ca})
	if status != "revoked" || reason == "" {
		t.Fatalf("revocation = %q %q", status, reason)
	}
	check := mailcrypto.SMIMECertificateCheck{Valid: true}
	api.applySMIMERevocation(context.Background(), &check, leaf, []*x509.Certificate{fixture.ca})
	if check.Valid || check.Revocation != "revoked" {
		t.Fatalf("check = %#v", check)
	}
}

func TestSMIMERevocationCRL(t *testing.T) {
	fixture := newRevocationFixture(t)
	var leaf *x509.Certificate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
			Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour),
			RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: leaf.SerialNumber, RevocationTime: now.Add(-time.Minute)}},
		}, fixture.ca, fixture.caKey)
		if err != nil {
			t.Errorf("create CRL: %v", err)
			http.Error(w, "failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(der)
	}))
	defer server.Close()
	leaf = fixture.leafWith(t, "", server.URL)
	api := &Server{cfg: config.Config{SMIMERevocationMode: "soft", SMIMERevocationAllowPrivate: true}}
	status, reason := api.smimeRevocationStatus(context.Background(), leaf, []*x509.Certificate{fixture.ca})
	if status != "revoked" || reason == "" {
		t.Fatalf("CRL revocation = %q %q", status, reason)
	}
}

func TestSMIMERevocationUnavailableModes(t *testing.T) {
	fixture := newRevocationFixture(t)
	leaf := fixture.leafWith(t, "", "")
	soft := &Server{cfg: config.Config{SMIMERevocationMode: "soft"}}
	check := mailcrypto.SMIMECertificateCheck{Valid: true}
	soft.applySMIMERevocation(context.Background(), &check, leaf, []*x509.Certificate{fixture.ca})
	if !check.Valid || len(check.Warnings) == 0 || check.Revocation != "unknown" {
		t.Fatalf("soft check = %#v", check)
	}
	require := &Server{cfg: config.Config{SMIMERevocationMode: "require"}}
	requireCheck := mailcrypto.SMIMECertificateCheck{Valid: true}
	require.applySMIMERevocation(context.Background(), &requireCheck, leaf, []*x509.Certificate{fixture.ca})
	if requireCheck.Valid || requireCheck.Revocation != "unknown" {
		t.Fatalf("require check = %#v", requireCheck)
	}
}
