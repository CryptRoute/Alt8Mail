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

func TestOfficeOrigins(t *testing.T) {
	got := officeOrigins(
		"https://mail.example.com",
		"https://example.com/",
		"https://mail.example.com",
		"not-a-url",
		"http://insecure.example.com",
	)
	want := "https://mail.example.com,https://example.com"
	if got != want {
		t.Fatalf("officeOrigins = %q, want %q", got, want)
	}
}

func TestOfficeImageEngine(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"onlyoffice/documentserver:9.4.0.1", "onlyoffice"},
		{"docker.io/onlyoffice/documentserver:latest", "onlyoffice"},
		{"altgence/office:25.04", "collabora"},
		{"docker.io/altgence/office:latest", "collabora"},
		{"collabora/code:latest", "collabora"},
		{"registry.example.com/team/custom-office:1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := officeImageEngine(tc.image); got != tc.want {
			t.Errorf("officeImageEngine(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

func TestOfficeStatusFromAgent(t *testing.T) {
	cases := []struct {
		agent officeAgentStatus
		want  string
	}{
		{officeAgentStatus{Status: "running", Running: true, Health: "ok"}, "running"},
		{officeAgentStatus{Status: "running", Running: true, Health: "unhealthy"}, "unhealthy"},
		{officeAgentStatus{Status: "running", Running: true}, "running"},
		{officeAgentStatus{Status: "stopped"}, "stopped"},
		{officeAgentStatus{}, "error"},
	}
	for _, tc := range cases {
		if got := officeStatusFromAgent(tc.agent); got != tc.want {
			t.Errorf("officeStatusFromAgent(%#v) = %q, want %q", tc.agent, got, tc.want)
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
