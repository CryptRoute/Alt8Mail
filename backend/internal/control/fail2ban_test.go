package control

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestFail2BanActionContentMatchesDeployFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "fail2ban", "action.d", "a8m-ufw-port.conf"))
	if err != nil {
		t.Skipf("deploy tree is outside the module test working directory: %v", err)
	}
	deployed := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if deployed != fail2BanActionContent {
		t.Fatalf("embedded Fail2Ban action differs from deploy/fail2ban/action.d/a8m-ufw-port.conf")
	}
	if !strings.Contains(fail2BanActionContent, "to any port <port> proto tcp") {
		t.Fatalf("action must scope the ban to the SSH port")
	}
	if strings.Contains(fail2BanActionContent, "to any proto tcp") {
		t.Fatalf("action must not retain an all-port destination")
	}
}

func TestParseFail2BanDuration(t *testing.T) {
	cases := map[string]int{
		"3600": 3600, "1h": 3600, "10m": 600, "1d": 86400,
		"1h30m": 5400, "2w": 1209600, "45s": 45, "": 0, "nope": 0,
	}
	for input, want := range cases {
		if got := parseFail2BanDuration(input); got != want {
			t.Errorf("parseFail2BanDuration(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestNormalizeFail2BanIgnoreIPsAlwaysKeepsLoopback(t *testing.T) {
	got, err := normalizeFail2BanIgnoreIPs("203.0.113.9, 2001:db8::/32 127.0.0.1/8")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1/8", "::1", "203.0.113.9", "2001:db8::/32"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ignore IPs = %#v, want %#v", got, want)
	}
	for _, value := range []string{"0.0.0.0/0", "::/0", "0.0.0.0", "not-an-ip"} {
		if _, err := normalizeFail2BanIgnoreIPs(value); err == nil {
			t.Errorf("normalizeFail2BanIgnoreIPs(%q) accepted a value that would disable protection", value)
		}
	}
}

func TestReadFail2BanJail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a8m.local")
	content := `[DEFAULT]
banaction = a8m-ufw-port
ignoreip = 127.0.0.1/8 ::1 203.0.113.4
bantime = 1h
findtime = 10m
maxretry = 5

[sshd]
enabled = false
port = 56777
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := readFail2BanJail(path)
	if cfg.BanAction != "a8m-ufw-port" || cfg.Bantime != 3600 || cfg.Findtime != 600 || cfg.MaxRetry != 5 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.Enabled || cfg.Port != "56777" {
		t.Fatalf("unexpected jail state: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.IgnoreIPs, []string{"127.0.0.1/8", "::1", "203.0.113.4"}) {
		t.Fatalf("ignore IPs = %#v", cfg.IgnoreIPs)
	}
}

func TestRenderFail2BanJailIsPortScoped(t *testing.T) {
	cfg := fail2BanJailConfig{
		Enabled: true, Bantime: 3600, Findtime: 600, MaxRetry: 5,
		IgnoreIPs: []string{"127.0.0.1/8", "::1"}, Port: "56777",
	}
	rendered := renderFail2BanJail(cfg)
	for _, want := range []string{"banaction = a8m-ufw-port", "bantime = 3600", "port = 56777", "enabled = true"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered jail is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "banaction = ufw\n") {
		t.Errorf("rendered jail still uses the all-port ufw action")
	}
}

func TestDeployJailDoesNotUseAllPortUfwAction(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "fail2ban", "a8m.local"))
	if err != nil {
		t.Skipf("deploy tree is outside the module test working directory: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "banaction = a8m-ufw-port") {
		t.Fatal("deploy jail must use the SSH-port-scoped action")
	}
	if regexp.MustCompile(`(?m)^\s*banaction\s*=\s*ufw\s*$`).MatchString(text) {
		t.Fatal("deploy jail still uses the stock all-port ufw action")
	}
	if !strings.Contains(text, "bantime = 3600") {
		t.Fatal("deploy jail must default to a 60-minute SSH ban")
	}
}

func TestControlUnitCanWriteFail2BanConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "systemd", "alt8mail-control.service"))
	if err != nil {
		t.Skipf("systemd unit is outside the module test working directory: %v", err)
	}
	if !strings.Contains(string(raw), "/etc/fail2ban") {
		t.Fatal("control unit must allow writing /etc/fail2ban so the Fail2Ban panel can save its jail")
	}
}

func TestFirewallViewHasHostWideCaveatAndFail2BanPanel(t *testing.T) {
	view, err := webFiles.ReadFile("web/views-infra.js")
	if err != nil {
		t.Fatalf("firewall view is not embedded: %v", err)
	}
	source := string(view)
	for _, want := range []string{
		"UFW rules are host-wide",
		"A failed SSH login must never block webmail or this Control Plane",
		"SSH protection (Fail2Ban)",
		"Host-wide block rule found",
		"/control/api/server/fail2ban",
		"Apply SSH-only protection",
		"Ban duration (minutes)",
		"Always-allowed addresses",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("firewall view is missing %q", want)
		}
	}
}

func TestFail2BanSnapshotReadsManagedJail(t *testing.T) {
	dir := t.TempDir()
	jail := filepath.Join(dir, "a8m.local")
	action := filepath.Join(dir, "a8m-ufw-port.conf")
	cfg := fail2BanJailConfig{
		Enabled: true, Bantime: 5400, Findtime: 900, MaxRetry: 4,
		IgnoreIPs: []string{"127.0.0.1/8", "::1", "203.0.113.4"}, Port: "56777",
	}
	if err := os.WriteFile(jail, []byte(renderFail2BanJail(cfg)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(action, []byte(fail2BanActionContent), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{cfg: Config{Fail2BanJailFile: jail, Fail2BanActionFile: action}}
	status := server.fail2BanSnapshot(context.Background())
	if !status.Enabled || status.BantimeSeconds != 5400 || status.FindtimeSeconds != 900 || status.MaxRetry != 4 {
		t.Fatalf("snapshot config = %+v", status)
	}
	if status.SSHPort != 56777 || !status.PortScoped || !status.ActionInstalled || status.LegacyAllPort {
		t.Fatalf("snapshot scope = %+v", status)
	}
	if status.BanAction != "a8m-ufw-port" {
		t.Fatalf("ban action = %q", status.BanAction)
	}
}

func TestLegacyFail2BanHostWideRules(t *testing.T) {
	output := `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 56777/tcp                  ALLOW IN    Anywhere
[ 2] 56777/tcp                  DENY IN     198.51.100.99              # alt8mail-fail2ban-sshd
[ 3] Anywhere                   REJECT IN   95.174.68.249              # by Fail2Ban after 5 attempts against sshd
[ 4] Anywhere (v6)              REJECT IN   2001:db8::9                # by Fail2Ban after 5 attempts against sshd
[ 5] Anywhere                   ALLOW IN    Anywhere                   # administrator rule
[ 6] 56777/tcp                  REJECT IN   203.0.113.7                # by Fail2Ban after 2 attempts against sshd
[ 7] Anywhere                   DENY IN     203.0.113.8                # by Fail2Ban after 1 attempts against sshd
`
	got := legacyFail2BanHostWideRules(output)
	want := []legacyFail2BanRule{
		{Number: 7, Action: "DENY", IP: "203.0.113.8"},
		{Number: 4, Action: "REJECT", IP: "2001:db8::9"},
		{Number: 3, Action: "REJECT", IP: "95.174.68.249"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy rules = %#v, want %#v (highest first)", got, want)
	}
}

func TestInstallerAndUpdaterInstallPortScopedAction(t *testing.T) {
	installer, err := os.ReadFile(filepath.Join("..", "..", "..", "installer", "phases", "97-ufw.sh"))
	if err != nil {
		t.Skipf("installer is outside the module test working directory: %v", err)
	}
	updater, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "alt8mail-update.sh"))
	if err != nil {
		t.Skipf("updater is outside the module test working directory: %v", err)
	}
	for name, text := range map[string]string{"installer/phases/97-ufw.sh": string(installer), "alt8mail-update.sh": string(updater)} {
		if !strings.Contains(text, "deploy/fail2ban/action.d/a8m-ufw-port.conf") {
			t.Errorf("%s does not install the port-scoped Fail2Ban action", name)
		}
	}
	updaterText := string(updater)
	if !strings.Contains(updaterText, "ensure_fail2ban_port_action") || !strings.Contains(updaterText, "banaction = a8m-ufw-port") {
		t.Error("updater does not migrate the legacy all-port ban action")
	}
	for _, key := range []string{"A8M_KEEP_RELEASES", "A8M_KEEP_BACKUPS", "prune_old_releases", "prune_old_backups"} {
		if !strings.Contains(updaterText, key) {
			t.Errorf("updater is missing release retention marker %q", key)
		}
	}
}
