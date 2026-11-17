package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestSMIMEDirectoryRequiresConfiguration(t *testing.T) {
	api := &Server{cfg: config.Config{}}
	if _, _, err := api.fetchSMIMECertificatesFromDirectory(context.Background(), "alice@example.test"); err == nil {
		t.Fatal("unconfigured S/MIME directory was used")
	}
}

func TestSMIMEDirectoryRejectsPlaintextInProduction(t *testing.T) {
	api := &Server{cfg: config.Config{
		Env: "production", SMIMELDAPURL: "ldap://directory.example.test",
		SMIMELDAPBaseDN: "dc=example,dc=test", SMIMELDAPInsecure: true,
	}}
	_, _, err := api.fetchSMIMECertificatesFromDirectory(context.Background(), "alice@example.test")
	if err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("plaintext LDAP in production error = %v", err)
	}
}
