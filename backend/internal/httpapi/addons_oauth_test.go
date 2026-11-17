package httpapi

import (
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestAddonOAuthMode(t *testing.T) {
	server := &Server{cfg: config.Config{}}
	if got := server.addonOAuthMode("deepl"); got != "" {
		t.Fatalf("non-OAuth add-on mode = %q, want empty", got)
	}
	if got := server.addonOAuthMode("slack"); got != "disabled" {
		t.Fatalf("unconfigured Slack mode = %q, want disabled", got)
	}
	server.cfg.SessionSecret = "session-secret"
	if got := server.addonOAuthMode("slack"); got != "byo" {
		t.Fatalf("Slack with a session secret mode = %q, want byo", got)
	}
	server.cfg.AddonRelayURL = "https://connect.example.test"
	server.cfg.AddonRelaySecret = "relay-secret"
	if got := server.addonOAuthMode("slack"); got != "managed" {
		t.Fatalf("Slack with a relay mode = %q, want managed", got)
	}
}

func TestAddonGatewayBase(t *testing.T) {
	server := &Server{cfg: config.Config{AddonGatewayURL: "https://connect.example.test/"}}
	if got := server.addonGatewayBase(nil); got != "https://connect.example.test" {
		t.Fatalf("gateway base = %q, want explicit gateway URL", got)
	}
	server.cfg.AddonGatewayURL = ""
	server.cfg.PublicURL = "https://mail.example.test/"
	if got := server.addonGatewayBase(nil); got != "https://mail.example.test" {
		t.Fatalf("gateway base = %q, want public URL", got)
	}
}
