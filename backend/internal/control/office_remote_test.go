package control

import (
	"strings"
	"testing"
)

func TestRemoteMemoryBytes(t *testing.T) {
	value, err := remoteMemoryBytes("4194304")
	if err != nil || value != 4<<30 {
		t.Fatalf("remoteMemoryBytes = %d, %v", value, err)
	}
	if _, err := remoteMemoryBytes("not-a-number"); err == nil {
		t.Fatal("invalid memory output was accepted")
	}
}

func TestRemoteQuote(t *testing.T) {
	if got := remoteQuote("a'b c"); got != `'a'\''b c'` {
		t.Fatalf("remoteQuote = %q", got)
	}
}

func TestMergeOfficeRemoteTargetHonorsClearHostKey(t *testing.T) {
	settings := officeSettings{
		RemoteHost: "old.example.com", RemotePort: 2222, RemoteUser: "deploy",
		RemoteAuth: "password", RemoteHostKey: "SHA256:old", RemotePublicIP: "203.0.113.9",
		RemoteAgentPath: "/opt/custom/agent",
	}
	target := mergeOfficeRemoteTarget(officeRemoteTarget{}, officeRemoteRequest{ClearHostKey: true}, settings)
	if target.Host != "old.example.com" || target.Port != 2222 || target.User != "deploy" ||
		target.Auth != "password" || target.PublicIP != "203.0.113.9" {
		t.Fatalf("saved target fields were not reused: %#v", target)
	}
	if target.HostKey != "" {
		t.Fatalf("cleared host key was repopulated: %q", target.HostKey)
	}
	if target.AgentPath != "/opt/custom/agent" {
		t.Fatalf("agent path = %q", target.AgentPath)
	}
	pinned := mergeOfficeRemoteTarget(officeRemoteTarget{}, officeRemoteRequest{}, settings)
	if pinned.HostKey != "SHA256:old" {
		t.Fatalf("saved host key was not reused: %q", pinned.HostKey)
	}
	override := mergeOfficeRemoteTarget(officeRemoteTarget{
		Host: "new.example.com", Port: 22, User: "root", Auth: "key",
		HostKey: "SHA256:new", PublicIP: "198.51.100.4",
	}, officeRemoteRequest{
		Host: "new.example.com", Port: 22, User: "root", Auth: "key",
		HostKey: "SHA256:new", PublicIP: "198.51.100.4",
	}, settings)
	if override.Host != "new.example.com" || override.HostKey != "SHA256:new" ||
		override.PublicIP != "198.51.100.4" || override.Port != 22 {
		t.Fatalf("request target was not preferred: %#v", override)
	}
}

func TestRemoteOfficeAgentEnvUsesImage(t *testing.T) {
	text := remoteOfficeAgentEnv("onlyoffice/documentserver:9.4.0.1")
	for _, expected := range []string{
		"OFFICE_AGENT_LISTEN=127.0.0.1:8098",
		"OFFICE_AGENT_IMAGE=onlyoffice/documentserver:9.4.0.1",
		"OFFICE_AGENT_CERTBOT_WEBROOT=/var/www/certbot",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("remote agent env is missing %q:\n%s", expected, text)
		}
	}
}
