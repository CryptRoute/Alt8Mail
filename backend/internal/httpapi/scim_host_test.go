package httpapi

import (
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestSCIMBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		public  string
		want    string
		wantErr bool
	}{
		{name: "https origin", public: "https://mail.example.test", want: "https://mail.example.test/scim/v2"},
		{name: "https trailing slash", public: "https://mail.example.test/", want: "https://mail.example.test/scim/v2"},
		{name: "https base path", public: "https://mail.example.test/mail/", want: "https://mail.example.test/mail/scim/v2"},
		{name: "https port", public: "https://mail.example.test:8443", want: "https://mail.example.test:8443/scim/v2"},
		{name: "loopback hostname", public: "http://localhost:8001", want: "http://localhost:8001/scim/v2"},
		{name: "loopback ipv4", public: "http://127.0.0.1:8001", want: "http://127.0.0.1:8001/scim/v2"},
		{name: "loopback ipv6", public: "http://[::1]:8001", want: "http://[::1]:8001/scim/v2"},
		{name: "empty", public: "", wantErr: true},
		{name: "whitespace", public: "   ", wantErr: true},
		{name: "relative", public: "/mail", wantErr: true},
		{name: "cleartext non-loopback", public: "http://mail.example.test", wantErr: true},
		{name: "unsupported scheme", public: "ftp://mail.example.test", wantErr: true},
		{name: "userinfo", public: "https://user:secret@mail.example.test", wantErr: true},
		{name: "query", public: "https://mail.example.test/?tenant=1", wantErr: true},
		{name: "empty query", public: "https://mail.example.test/?", wantErr: true},
		{name: "fragment", public: "https://mail.example.test/#fragment", wantErr: true},
		{name: "empty fragment", public: "https://mail.example.test/#", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{PublicURL: tc.public}}
			got, err := server.scimBaseURL()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scimBaseURL(%q) = %q, want error", tc.public, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("scimBaseURL(%q): %v", tc.public, err)
			}
			if got != tc.want {
				t.Fatalf("scimBaseURL(%q) = %q, want %q", tc.public, got, tc.want)
			}
		})
	}
}

func TestAdminBootstrapPayloadIncludesSCIMURL(t *testing.T) {
	cases := []struct {
		name     string
		public   string
		want     string
		wantNone bool
	}{
		{name: "https origin", public: "https://mail.example.test", want: "https://mail.example.test/scim/v2"},
		{name: "trailing slash", public: "https://mail.example.test/", want: "https://mail.example.test/scim/v2"},
		{name: "base path", public: "https://mail.example.test/mail/", want: "https://mail.example.test/mail/scim/v2"},
		{name: "loopback", public: "http://127.0.0.1:8001", want: "http://127.0.0.1:8001/scim/v2"},
		{name: "empty", public: "", wantNone: true},
		{name: "cleartext non-loopback", public: "http://mail.example.test", wantNone: true},
		{name: "query", public: "https://mail.example.test/?tenant=1", wantNone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{cfg: config.Config{PublicURL: tc.public}, version: "test"}
			payload := server.adminServerPayload()
			got, exists := payload["scimURL"]
			if tc.wantNone {
				if exists {
					t.Fatalf("adminServerPayload().scimURL = %#v, want omitted", got)
				}
				return
			}
			if !exists || got != tc.want {
				t.Fatalf("adminServerPayload().scimURL = %#v (present %v), want %q", got, exists, tc.want)
			}
		})
	}
}
