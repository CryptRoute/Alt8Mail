package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidOfficeURL(t *testing.T) {
	for _, value := range []string{"https://office.example.com", "https://office.example.com/"} {
		if err := validOfficeURL(value); err != nil {
			t.Errorf("valid Office URL %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"http://office.example.com", "https://office.example.com/path", "https://localhost", "not-a-url"} {
		if err := validOfficeURL(value); err == nil {
			t.Errorf("invalid Office URL %q was accepted", value)
		}
	}
}

func TestOfficeCertificateCovers(t *testing.T) {
	cert := officeCertificate{SANs: []string{"office.example.com", "*.docs.example.net"}}
	for _, host := range []string{"office.example.com", "team.docs.example.net"} {
		if !officeCertificateCovers(cert, host) {
			t.Errorf("certificate did not cover %s", host)
		}
	}
	for _, host := range []string{"mail.example.com", "deep.team.docs.example.net", "example.com"} {
		if officeCertificateCovers(cert, host) {
			t.Errorf("certificate unexpectedly covered %s", host)
		}
	}
}

func TestOfficeAgentStatusClient(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "office-agent-token")
	if err := os.WriteFile(tokenFile, []byte("test-agent-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-agent-token" {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"installed":true,"running":true,"status":"running","health":"ok","secretPresent":true}`))
	}))
	defer agent.Close()
	server := &Server{cfg: Config{OfficeAgentURL: agent.URL, OfficeAgentToken: tokenFile}}
	status, err := server.officeAgentStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.Health != "ok" {
		t.Fatalf("unexpected agent status: %#v", status)
	}
}
