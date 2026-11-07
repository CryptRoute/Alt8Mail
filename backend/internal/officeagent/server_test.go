package officeagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestCertificate(t *testing.T, dir, hostname string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "office.crt")
	keyPath := filepath.Join(dir, "office.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestComposeContents(t *testing.T) {
	text := composeContents("onlyoffice/documentserver:9.4.0.1", "test-secret", "a8m-office", 8082, "/var/lib/a8m-office/fonts")
	for _, expected := range []string{
		"image: onlyoffice/documentserver:9.4.0.1",
		"JWT_SECRET: \"test-secret\"",
		"127.0.0.1:8082:80",
		"/var/lib/a8m-office/data:/var/www/onlyoffice/Data",
		"/var/lib/a8m-office/fonts:/usr/share/fonts/truetype/custom:ro",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("compose file is missing %q:\n%s", expected, text)
		}
	}
}

func TestValidPublicURLAndImage(t *testing.T) {
	for _, value := range []string{"https://office.example.com", "https://office.example.com/"} {
		if err := validPublicURL(strings.TrimRight(value, "/")); err != nil {
			t.Errorf("valid public URL %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"http://office.example.com", "https://office.example.com/path", "https://localhost", "not-a-url"} {
		if err := validPublicURL(value); err == nil {
			t.Errorf("invalid public URL %q was accepted", value)
		}
	}
	if err := validImage("onlyoffice/documentserver:9.4.0.1"); err != nil {
		t.Errorf("valid image was rejected: %v", err)
	}
	if err := validImage("onlyoffice/documentserver; rm -rf /"); err == nil {
		t.Error("invalid image was accepted")
	}
}

func TestStatusAndInstallWithFakeDocker(t *testing.T) {
	temp := t.TempDir()
	tokenFile := filepath.Join(temp, "agent-token")
	runs := []string{}
	runner := func(ctx context.Context, timeout time.Duration, name string, args ...string) (string, string, error) {
		runs = append(runs, name+" "+strings.Join(args, " "))
		if name == "docker" && len(args) >= 4 && args[0] == "inspect" {
			if strings.Contains(strings.Join(args, " "), "State.Status") {
				return "running\n", "", nil
			}
			return "onlyoffice/documentserver:9.4.0.1\n", "", nil
		}
		return "", "", nil
	}
	server, err := New(Config{
		TokenFile:    tokenFile,
		ComposeFile:  filepath.Join(temp, "office", "docker-compose.yml"),
		SecretFile:   filepath.Join(temp, "office-jwt-secret"),
		StateFile:    filepath.Join(temp, "office", "state.json"),
		NginxSite:    filepath.Join(temp, "nginx", "a8m-office"),
		NginxEnabled: filepath.Join(temp, "nginx-enabled", "a8m-office"),
		FontsDir:     filepath.Join(temp, "office", "fonts"),
		Runner:       runner,
		Health:       func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"publicUrl":"https://office.example.com","image":"onlyoffice/documentserver:9.4.0.1","disableGateway":true}`
	request := httptest.NewRequest(http.MethodPost, "/install", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("install status = %d: %s", response.Code, response.Body.String())
	}
	var status Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Running || status.Status != "running" || !status.SecretPresent {
		t.Fatalf("unexpected install status: %#v", status)
	}
	if status.Version != "9.4.0.1" {
		t.Fatalf("version = %q", status.Version)
	}
	secret, err := os.ReadFile(filepath.Join(temp, "office-jwt-secret"))
	if err != nil || len(strings.TrimSpace(string(secret))) < 32 {
		t.Fatalf("JWT secret was not written: %v", err)
	}
	compose, err := os.ReadFile(filepath.Join(temp, "office", "docker-compose.yml"))
	if err != nil || !strings.Contains(string(compose), "JWT_SECRET") {
		t.Fatalf("compose file was not written: %v", err)
	}
	if !strings.Contains(strings.Join(runs, "\n"), "compose") {
		t.Fatalf("docker compose was not invoked: %v", runs)
	}
}

func TestAgentRequiresBearerToken(t *testing.T) {
	temp := t.TempDir()
	server, err := New(Config{
		TokenFile: filepath.Join(temp, "token"),
		Runner: func(context.Context, time.Duration, string, ...string) (string, string, error) {
			return "", "", nil
		},
		Health: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.Code)
	}
}

func TestNginxContentsProxiesWebSockets(t *testing.T) {
	text := nginxContents("office.example.com", "/etc/ssl/office.crt", "/etc/ssl/office.key", "https://mail.example.com", 8082, "/var/www/certbot")
	for _, expected := range []string{"server_name office.example.com;", "proxy_pass http://127.0.0.1:8082;", "Upgrade $http_upgrade", "proxy_read_timeout 3600s", "location = / { return 302 https://mail.example.com; }", "location = /welcome/ { return 302 https://mail.example.com; }"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("nginx contents are missing %q", expected)
		}
	}
}

func TestValidPortalURL(t *testing.T) {
	for _, value := range []string{"", "https://mail.example.com", "http://mail.example.com/workspace"} {
		if err := validPortalURL(value); err != nil {
			t.Fatalf("valid portal URL %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"mail.example.com", "https://mail.example.com; return 301 https://evil.example", "https://mail.example.com/$host", "https://mail.example.com/\"quoted\""} {
		if err := validPortalURL(value); err == nil {
			t.Fatalf("invalid portal URL %q was accepted", value)
		}
	}
}

func TestInstallReusesPreviousGatewayCertificate(t *testing.T) {
	temp := t.TempDir()
	cert, key := writeTestCertificate(t, temp, "office.example.com")
	site := filepath.Join(temp, "nginx", "a8m-office")
	server, err := New(Config{
		TokenFile:    filepath.Join(temp, "token"),
		ComposeFile:  filepath.Join(temp, "office", "docker-compose.yml"),
		SecretFile:   filepath.Join(temp, "office-jwt-secret"),
		StateFile:    filepath.Join(temp, "state.json"),
		NginxSite:    site,
		NginxEnabled: filepath.Join(temp, "nginx-enabled", "a8m-office"),
		CertRoots:    []string{temp},
		FontsDir:     filepath.Join(temp, "office", "fonts"),
		Runner: func(context.Context, time.Duration, string, ...string) (string, string, error) {
			return "", "", nil
		},
		Health: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.saveState(state{
		PublicURL: "https://old.example.com",
		Image:     "onlyoffice/documentserver:9.4.0.1",
		TLSCert:   cert,
		TLSKey:    key,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.install(context.Background(), installRequest{PublicURL: "https://office.example.com", Image: "onlyoffice/documentserver:9.4.0.1"}); err != nil {
		t.Fatal(err)
	}
	st, err := server.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.PublicURL != "https://office.example.com" || st.TLSCert != cert || st.TLSKey != key {
		t.Fatalf("gateway state was not preserved: %#v", st)
	}
	data, err := os.ReadFile(site)
	if err != nil || !strings.Contains(string(data), "server_name office.example.com;") {
		t.Fatalf("gateway was not rewritten for the new URL: %v\n%s", err, data)
	}
}

func TestGatewayRestoresPreviousConfigWhenNginxRejects(t *testing.T) {
	temp := t.TempDir()
	cert, key := writeTestCertificate(t, temp, "office.example.com")
	site := filepath.Join(temp, "nginx", "a8m-office")
	if err := os.MkdirAll(filepath.Dir(site), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(site, []byte("old gateway"), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled := filepath.Join(temp, "nginx-enabled", "a8m-office")
	server, err := New(Config{
		TokenFile:    filepath.Join(temp, "token"),
		SecretFile:   filepath.Join(temp, "secret"),
		StateFile:    filepath.Join(temp, "state.json"),
		NginxSite:    site,
		NginxEnabled: enabled,
		CertRoots:    []string{temp},
		Runner: func(_ context.Context, _ time.Duration, name string, args ...string) (string, string, error) {
			if name == "nginx" && len(args) == 1 && args[0] == "-t" {
				return "", "invalid gateway", fmt.Errorf("exit 1")
			}
			return "", "", nil
		},
		Health: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.configureGateway(state{PublicURL: "https://office.example.com", TLSCert: cert, TLSKey: key}); err == nil {
		t.Fatal("expected nginx to reject the gateway")
	}
	data, err := os.ReadFile(site)
	if err != nil || string(data) != "old gateway" {
		t.Fatalf("previous gateway was not restored: %q, %v", data, err)
	}
	if _, err := os.Stat(enabled); !os.IsNotExist(err) {
		t.Fatalf("rejected gateway was left enabled: %v", err)
	}
}
