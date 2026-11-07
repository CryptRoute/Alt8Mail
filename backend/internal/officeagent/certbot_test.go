package officeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureLetsEncryptIssuesAndReusesCertificate(t *testing.T) {
	temp := t.TempDir()
	certbotDir := filepath.Join(temp, "letsencrypt", "live")
	webroot := filepath.Join(temp, "certbot")
	site := filepath.Join(temp, "nginx", "a8m-office")
	certName := "a8m-office-office-example-com"
	certPath := filepath.Join(certbotDir, certName, "fullchain.pem")
	keyPath := filepath.Join(certbotDir, certName, "privkey.pem")
	certbotCalls := 0
	server, err := New(Config{
		TokenFile:      filepath.Join(temp, "token"),
		SecretFile:     filepath.Join(temp, "secret"),
		StateFile:      filepath.Join(temp, "state.json"),
		NginxSite:      site,
		NginxEnabled:   filepath.Join(temp, "nginx-enabled", "a8m-office"),
		CertRoots:      []string{certbotDir},
		CertbotDir:     certbotDir,
		CertbotWebroot: webroot,
		Runner: func(_ context.Context, _ time.Duration, name string, args ...string) (string, string, error) {
			if name == "certbot" {
				certbotCalls++
				if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(certPath, []byte("cert"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(args, " "), "-d office.example.com") {
					t.Fatalf("certbot domain argument missing: %v", args)
				}
			}
			return "", "", nil
		},
		Health: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	cert, key, err := server.ensureLetsEncrypt(context.Background(), "https://office.example.com", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cert != certPath || key != keyPath || certbotCalls != 1 {
		t.Fatalf("unexpected issuance result: %q %q calls=%d", cert, key, certbotCalls)
	}
	if _, err := os.Stat(site); !os.IsNotExist(err) {
		t.Fatalf("the temporary ACME challenge site should be removed after issuance: %v", err)
	}
	if err := os.WriteFile(site, []byte("# previous gateway\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cert, key, err = server.ensureLetsEncrypt(context.Background(), "https://office.example.com", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if certbotCalls != 2 {
		t.Fatalf("certbot should check for renewal on a reconfigure: %d", certbotCalls)
	}
	if cert != certPath || key != keyPath {
		t.Fatalf("existing certificate path was not reused: %q %q", cert, key)
	}
	data, err := os.ReadFile(site)
	if err != nil || strings.TrimSpace(string(data)) != "# previous gateway" {
		t.Fatalf("the previous gateway site was not restored: %v\n%s", err, data)
	}
}

func TestEnsureLetsEncryptRequiresEmail(t *testing.T) {
	temp := t.TempDir()
	server, err := New(Config{
		TokenFile:      filepath.Join(temp, "token"),
		SecretFile:     filepath.Join(temp, "secret"),
		StateFile:      filepath.Join(temp, "state.json"),
		NginxSite:      filepath.Join(temp, "nginx", "site"),
		NginxEnabled:   filepath.Join(temp, "nginx-enabled", "site"),
		CertbotDir:     filepath.Join(temp, "live"),
		CertbotWebroot: filepath.Join(temp, "webroot"),
		Runner:         func(context.Context, time.Duration, string, ...string) (string, string, error) { return "", "", nil },
		Health:         func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.ensureLetsEncrypt(context.Background(), "https://office.example.com", ""); err == nil {
		t.Fatal("expected an email requirement error")
	}
}
