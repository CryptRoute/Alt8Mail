package control

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Fail2Ban protects sshd only. The stock ufw action bans the address on every
// destination port, so a few mistyped SSH passwords can take webmail and the
// Control Plane offline for that address. This action is installed by
// install.sh and alt8mail-update.sh; the constant below keeps System Control
// able to repair the same file on hosts that predate the updater change.
const fail2BanActionContent = `# Alt8Mail SSH-port-scoped UFW ban action.
#
# The stock ufw action installs ` + "`" + `ufw deny from <ip>` + "`" + ` with destination ` + "`" + `any` + "`" + `,
# which blocks every port on the host. A handful of failed SSH passwords then
# takes webmail and System Control offline for that address. This action adds
# the deny rule only for the SSH port that Fail2Ban is protecting.
#
# Fail2Ban supplies <port> from the sshd jail's ` + "`" + `port` + "`" + ` setting. Keep that value
# numeric (install.sh and the updater resolve the real sshd port).

[Definition]

actionstart =
actionstop =
actioncheck =

actionban = ufw prepend deny from <ip> to any port <port> proto tcp comment 'alt8mail-fail2ban-sshd'
actionunban = ufw delete deny from <ip> to any port <port> proto tcp

[Init]

# Fail2Ban interpolates <port> from the jail. The fallback keeps a manual
# ` + "`" + `fail2ban-client` + "`" + ` invocation from accidentally restricting another service.
port = ssh
`

const fail2BanActionMarker = "alt8mail-fail2ban-sshd"

type fail2BanJailConfig struct {
	Enabled   bool
	Bantime   int
	Findtime  int
	MaxRetry  int
	IgnoreIPs []string
	Port      string
	BanAction string
}

type fail2BanStatus struct {
	Installed       bool     `json:"installed"`
	ServiceActive   bool     `json:"serviceActive"`
	Version         string   `json:"version"`
	Jail            string   `json:"jail"`
	Enabled         bool     `json:"enabled"`
	BantimeSeconds  int      `json:"bantimeSeconds"`
	FindtimeSeconds int      `json:"findtimeSeconds"`
	MaxRetry        int      `json:"maxRetry"`
	IgnoreIPs       []string `json:"ignoreIPs"`
	SSHPort         int      `json:"sshPort"`
	BanAction       string   `json:"banAction"`
	PortScoped      bool     `json:"portScoped"`
	LegacyAllPort   bool     `json:"legacyAllPort"`
	ActionInstalled bool     `json:"actionInstalled"`
	ConfigPath      string   `json:"configPath"`
	BannedIPs       []string `json:"bannedIPs"`
	CurrentlyFailed int      `json:"currentlyFailed"`
	TotalFailed     int      `json:"totalFailed"`
	CurrentlyBanned int      `json:"currentlyBanned"`
	TotalBanned     int      `json:"totalBanned"`
	RuntimeError    string   `json:"runtimeError,omitempty"`
}

func defaultFail2BanJail() fail2BanJailConfig {
	return fail2BanJailConfig{
		Enabled: true, Bantime: 3600, Findtime: 600, MaxRetry: 5,
		IgnoreIPs: []string{"127.0.0.1/8", "::1"},
		BanAction: "a8m-ufw-port",
	}
}

func (s *Server) registerFail2BanRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/server/fail2ban", s.require("server.read", s.handleFail2BanStatus))
	mux.HandleFunc("PUT /control/api/server/fail2ban", s.require("server.manage", s.handleFail2BanSave))
	mux.HandleFunc("POST /control/api/server/fail2ban/unban", s.require("server.manage", s.handleFail2BanUnban))
}

func (s *Server) handleFail2BanStatus(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	writeJSON(w, http.StatusOK, s.fail2BanSnapshot(r.Context()))
}

func (s *Server) fail2BanSnapshot(ctx context.Context) fail2BanStatus {
	cfg := readFail2BanJail(s.cfg.Fail2BanJailFile)
	status := fail2BanStatus{
		Jail: "sshd", Enabled: cfg.Enabled, BantimeSeconds: cfg.Bantime,
		FindtimeSeconds: cfg.Findtime, MaxRetry: cfg.MaxRetry,
		IgnoreIPs: append([]string{}, cfg.IgnoreIPs...), BanAction: cfg.BanAction,
		ConfigPath: s.cfg.Fail2BanJailFile,
		BannedIPs:  []string{},
	}
	status.SSHPort = resolveFail2BanSSHPort(cfg.Port)
	status.ActionInstalled = fail2BanActionPresent(s.cfg.Fail2BanActionFile)
	status.PortScoped = cfg.BanAction == "a8m-ufw-port" && status.ActionInstalled
	status.LegacyAllPort = cfg.BanAction == "ufw"

	client, err := exec.LookPath("fail2ban-client")
	if err != nil {
		return status
	}
	status.Installed = true
	status.Version = firstLine(commandValue(ctx, client, "--version"))
	status.ServiceActive = commandValue(ctx, "systemctl", "is-active", "fail2ban") == "active"

	result, err := runCommand(ctx, 8*time.Second, client, "status", "sshd")
	if err != nil {
		status.RuntimeError = strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout))
		return status
	}
	output := result.Stdout
	status.BannedIPs = parseFail2BanBannedIPs(output)
	status.CurrentlyFailed = parseFail2BanCount(output, `Currently failed:\s*(\d+)`)
	status.TotalFailed = parseFail2BanCount(output, `Total failed:\s*(\d+)`)
	status.CurrentlyBanned = parseFail2BanCount(output, `Currently banned:\s*(\d+)`)
	status.TotalBanned = parseFail2BanCount(output, `Total banned:\s*(\d+)`)
	if len(status.BannedIPs) > status.CurrentlyBanned {
		status.CurrentlyBanned = len(status.BannedIPs)
	}
	return status
}

func firstLine(value string) string {
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return strings.TrimSpace(value)
}

func parseFail2BanBannedIPs(output string) []string {
	ips := []string{}
	match := regexp.MustCompile(`(?m)Banned IP list:\s*(.*)$`).FindStringSubmatch(output)
	if len(match) != 2 {
		return ips
	}
	for _, ip := range strings.Fields(match[1]) {
		if net.ParseIP(ip) != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

func parseFail2BanCount(output, pattern string) int {
	match := regexp.MustCompile(pattern).FindStringSubmatch(output)
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	return value
}

func readFail2BanJail(path string) fail2BanJailConfig {
	cfg := defaultFail2BanJail()
	file, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer file.Close()
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch section {
		case "default", "sshd":
			switch key {
			case "banaction":
				// A per-jail action overrides [DEFAULT]; the managed file only
				// sets it in [DEFAULT], but an older hand-edited jail may set it
				// in [sshd].
				cfg.BanAction = strings.ToLower(value)
			case "ignoreip":
				cfg.IgnoreIPs = strings.Fields(strings.ReplaceAll(value, ",", " "))
			case "bantime":
				if seconds := parseFail2BanDuration(value); seconds > 0 {
					cfg.Bantime = seconds
				}
			case "findtime":
				if seconds := parseFail2BanDuration(value); seconds > 0 {
					cfg.Findtime = seconds
				}
			case "maxretry":
				if retries, err := strconv.Atoi(value); err == nil && retries > 0 {
					cfg.MaxRetry = retries
				}
			case "enabled":
				if section == "sshd" {
					cfg.Enabled = strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes")
				}
			case "port":
				if section == "sshd" {
					cfg.Port = value
				}
			}
		}
	}
	return cfg
}

// parseFail2BanDuration accepts the bare seconds written by System Control and
// the s/m/h/d/w suffixes Fail2Ban itself documents. Compound values such as
// "1h30m" are handled; an unparseable value returns 0 and the caller keeps its
// default.
func parseFail2BanDuration(raw string) int {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return 0
	}
	total := 0
	for len(raw) > 0 {
		index := 0
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			index++
		}
		if index == 0 {
			return 0
		}
		value, err := strconv.Atoi(raw[:index])
		if err != nil || value < 0 {
			return 0
		}
		multiplier := 1
		if index < len(raw) {
			switch raw[index] {
			case 's':
				multiplier = 1
			case 'm':
				multiplier = 60
			case 'h':
				multiplier = 3600
			case 'd':
				multiplier = 86400
			case 'w':
				multiplier = 604800
			default:
				return 0
			}
			index++
		}
		total += value * multiplier
		raw = raw[index:]
	}
	return total
}

func resolveFail2BanSSHPort(configured string) int {
	if port, err := strconv.Atoi(strings.TrimSpace(configured)); err == nil && port > 0 && port <= 65535 {
		return port
	}
	output := commandValue(context.Background(), "sshd", "-T")
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "port") {
			if port, err := strconv.Atoi(fields[1]); err == nil && port > 0 && port <= 65535 {
				return port
			}
		}
	}
	return 22
}

func fail2BanActionPresent(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(raw), fail2BanActionMarker) &&
		strings.Contains(string(raw), "to any port <port> proto tcp")
}

// normalizeFail2BanIgnoreIPs validates every entry and always keeps loopback
// exempt. The Control Plane terminal and local mail infrastructure connect to
// sshd over 127.0.0.1; banning loopback would break the terminal even when the
// administrator never typed it into the field.
func normalizeFail2BanIgnoreIPs(raw string) ([]string, error) {
	entries := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(entries) > 64 {
		return nil, fmt.Errorf("at most 64 ignore addresses are allowed")
	}
	seen := map[string]bool{}
	result := []string{}
	add := func(value string) {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	add("127.0.0.1/8")
	add("::1")
	for _, entry := range entries {
		if ip := net.ParseIP(entry); ip != nil {
			if ip.IsUnspecified() {
				return nil, fmt.Errorf("%s would exempt every address", entry)
			}
			add(entry)
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", entry)
		}
		ones, _ := network.Mask.Size()
		if ones == 0 {
			return nil, fmt.Errorf("%s would exempt every address", entry)
		}
		add(entry)
	}
	return result, nil
}

func renderFail2BanJail(cfg fail2BanJailConfig) string {
	lines := []string{
		"# Managed by Alt8Mail System Control. Local edits to this file are overwritten.",
		"# SSH failures ban only the SSH port; webmail and the Control Plane stay reachable.",
		"[DEFAULT]",
		"backend = systemd",
		"banaction = a8m-ufw-port",
		"ignoreip = " + strings.Join(cfg.IgnoreIPs, " "),
		fmt.Sprintf("bantime = %d", cfg.Bantime),
		fmt.Sprintf("findtime = %d", cfg.Findtime),
		fmt.Sprintf("maxretry = %d", cfg.MaxRetry),
		"",
		"[sshd]",
		fmt.Sprintf("enabled = %t", cfg.Enabled),
		fmt.Sprintf("port = %s", cfg.Port),
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *Server) handleFail2BanSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.fail2BanMu.Lock()
	defer s.fail2BanMu.Unlock()
	var body struct {
		Enabled         *bool  `json:"enabled"`
		BantimeSeconds  int    `json:"bantimeSeconds"`
		FindtimeSeconds int    `json:"findtimeSeconds"`
		MaxRetry        int    `json:"maxRetry"`
		IgnoreIPs       string `json:"ignoreIPs"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current := readFail2BanJail(s.cfg.Fail2BanJailFile)
	if body.BantimeSeconds < 60 || body.BantimeSeconds > 30*24*3600 {
		writeError(w, http.StatusBadRequest, "FAIL2BAN", "Ban duration must be between 1 minute and 30 days")
		return
	}
	if body.FindtimeSeconds < 30 || body.FindtimeSeconds > 24*3600 {
		writeError(w, http.StatusBadRequest, "FAIL2BAN", "Find window must be between 30 seconds and 24 hours")
		return
	}
	if body.MaxRetry < 1 || body.MaxRetry > 50 {
		writeError(w, http.StatusBadRequest, "FAIL2BAN", "Retries must be between 1 and 50")
		return
	}
	ignoreIPs, err := normalizeFail2BanIgnoreIPs(body.IgnoreIPs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FAIL2BAN", err.Error())
		return
	}
	if err := s.ensureFail2BanInstalled(); err != nil {
		writeError(w, http.StatusConflict, "FAIL2BAN", err.Error())
		return
	}

	next := fail2BanJailConfig{
		Enabled:   current.Enabled,
		Bantime:   body.BantimeSeconds,
		Findtime:  body.FindtimeSeconds,
		MaxRetry:  body.MaxRetry,
		IgnoreIPs: ignoreIPs,
		Port:      strconv.Itoa(resolveFail2BanSSHPort(current.Port)),
		BanAction: "a8m-ufw-port",
	}
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}

	previousJail, _ := os.ReadFile(s.cfg.Fail2BanJailFile)
	previousAction, _ := os.ReadFile(s.cfg.Fail2BanActionFile)
	if _, err := writeManagedFile(s.cfg.Fail2BanActionFile, fail2BanActionContent, 0o644, 0, 0); err != nil {
		writeError(w, http.StatusInternalServerError, "FAIL2BAN", "Could not install the SSH-port ban action: "+err.Error())
		return
	}
	if _, err := writeManagedFile(s.cfg.Fail2BanJailFile, renderFail2BanJail(next), 0o644, 0, 0); err != nil {
		s.restoreFail2BanFiles(previousJail, previousAction)
		writeError(w, http.StatusInternalServerError, "FAIL2BAN", "Could not write the Fail2Ban jail: "+err.Error())
		return
	}

	client, _ := exec.LookPath("fail2ban-client")
	if result, err := runCommand(r.Context(), 20*time.Second, client, "-t"); err != nil {
		s.restoreFail2BanFiles(previousJail, previousAction)
		writeError(w, http.StatusUnprocessableEntity, "FAIL2BAN", "Fail2Ban rejected the configuration: "+strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
		return
	}

	applyErr := ""
	active := commandValue(r.Context(), "systemctl", "is-active", "fail2ban") == "active"
	if active {
		// A full restart is deliberate: Fail2Ban caches action definitions
		// from action.d at process start, and a reload after replacing the
		// SSH ban action can leave the jail with no working action.
		if result, err := runCommand(r.Context(), 45*time.Second, "systemctl", "restart", "fail2ban"); err != nil {
			applyErr = strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout))
		}
	} else if next.Enabled {
		if result, err := runCommand(r.Context(), 45*time.Second, "systemctl", "start", "fail2ban"); err != nil {
			applyErr = strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout))
		}
	}
	if applyErr != "" && commandValue(r.Context(), "systemctl", "is-active", "fail2ban") != "active" && next.Enabled {
		s.restoreFail2BanFiles(previousJail, previousAction)
		_, _ = runCommand(r.Context(), 45*time.Second, "systemctl", "restart", "fail2ban")
		writeError(w, http.StatusBadGateway, "FAIL2BAN", "Fail2Ban did not start: "+applyErr)
		return
	}

	s.actorAudit(r, sess, "server.fail2ban_updated", "sshd",
		"Updated SSH intrusion protection",
		map[string]any{
			"enabled": next.Enabled, "bantimeSeconds": next.Bantime,
			"findtimeSeconds": next.Findtime, "maxRetry": next.MaxRetry,
			"ignoreCount": len(next.IgnoreIPs), "sshPort": next.Port,
			"banScope": "ssh-port-only",
		})
	status := s.fail2BanSnapshot(r.Context())
	if applyErr != "" {
		status.RuntimeError = applyErr
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) restoreFail2BanFiles(previousJail, previousAction []byte) {
	if len(previousJail) > 0 {
		_, _ = writeManagedFile(s.cfg.Fail2BanJailFile, string(previousJail), 0o644, 0, 0)
	} else {
		_ = removeManagedFile(s.cfg.Fail2BanJailFile)
	}
	if len(previousAction) > 0 {
		_, _ = writeManagedFile(s.cfg.Fail2BanActionFile, string(previousAction), 0o644, 0, 0)
	} else {
		_ = removeManagedFile(s.cfg.Fail2BanActionFile)
	}
}

func (s *Server) ensureFail2BanInstalled() error {
	if _, err := exec.LookPath("fail2ban-client"); err != nil {
		return fmt.Errorf("Fail2Ban is not installed on this host")
	}
	if info, err := os.Stat("/etc/fail2ban"); err != nil || !info.IsDir() {
		return fmt.Errorf("Fail2Ban configuration directory is missing")
	}
	return nil
}

// cleanLegacyFail2BanHostWideRules removes the host-wide REJECT/DENY rules
// created by the stock Fail2Ban ufw action before this release. The stock
// action writes a rule whose destination is Anywhere and whose comment is
// "by Fail2Ban after N attempts against sshd". Those are exactly the rules that
// took webmail and System Control offline for a banned SSH address. Only that
// comment, that destination and those actions are matched; an administrator's
// own host-wide rule is left for the Firewall page to show and manage.
func (s *Server) cleanLegacyFail2BanHostWideRules(ctx context.Context) {
	s.fail2BanMu.Lock()
	defer s.fail2BanMu.Unlock()
	ufw, err := exec.LookPath("ufw")
	if err != nil {
		return
	}
	result, err := runCommand(ctx, 10*time.Second, ufw, "status", "numbered")
	if err != nil {
		return
	}
	numbers := legacyFail2BanHostWideRules(result.Stdout)
	if len(numbers) == 0 {
		return
	}
	removed := 0
	for _, rule := range numbers {
		// Delete by rule specification, never by the numbered position: a
		// concurrent ban can renumber ufw between the listing and the delete.
		if _, err := runCommand(ctx, 20*time.Second, ufw, "--force", "delete", strings.ToLower(rule.Action), "from", rule.IP); err == nil {
			removed++
		}
	}
	if removed > 0 && s.logger != nil {
		s.logger.Warn("removed legacy all-port Fail2Ban UFW rules; SSH bans are now port-scoped",
			"removed", removed)
	}
}

type legacyFail2BanRule struct {
	Number int
	Action string
	IP     string
}

func legacyFail2BanHostWideRules(output string) []legacyFail2BanRule {
	pattern := regexp.MustCompile(`(?i)^\[\s*(\d+)\]\s+Anywhere(?:\s+\(v6\))?\s+(DENY|REJECT)\s+IN\s+(\S+).*#\s*by Fail2Ban after \d+ attempts against sshd\s*$`)
	rules := []legacyFail2BanRule{}
	for _, line := range strings.Split(output, "\n") {
		match := pattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 4 {
			continue
		}
		if strings.EqualFold(match[3], "Anywhere") || strings.EqualFold(match[3], "Anywhere(v6)") {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err == nil {
			rules = append(rules, legacyFail2BanRule{Number: number, Action: match[2], IP: match[3]})
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Number > rules[j].Number })
	return rules
}

func (s *Server) handleFail2BanUnban(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.fail2BanMu.Lock()
	defer s.fail2BanMu.Unlock()
	var body struct {
		IP string `json:"ip"`
	}
	if err := decodeJSON(w, r, &body, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	ip := net.ParseIP(strings.TrimSpace(body.IP))
	if ip == nil {
		writeError(w, http.StatusBadRequest, "FAIL2BAN", "Address is invalid")
		return
	}
	if err := s.ensureFail2BanInstalled(); err != nil {
		writeError(w, http.StatusConflict, "FAIL2BAN", err.Error())
		return
	}
	client, _ := exec.LookPath("fail2ban-client")
	result, err := runCommand(r.Context(), 30*time.Second, client, "set", "sshd", "unbanip", ip.String())
	if err != nil {
		writeError(w, http.StatusBadGateway, "FAIL2BAN", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
		return
	}
	s.actorAudit(r, sess, "server.fail2ban_unban", ip.String(), "Unbanned an SSH address", nil)
	writeJSON(w, http.StatusOK, s.fail2BanSnapshot(r.Context()))
}

// autoMigrateFail2BanScope closes the bootstrap gap for hosts that update
// from a release whose jail used the stock all-port `ufw` action. The new
// control binary starts before an administrator can click Save, so migrate the
// one known-unsafe setting while preserving every duration, retry and ignore
// value the administrator configured. Only `banaction = ufw` is touched; an
// unknown custom action is left for the Firewall page to report.
func (s *Server) autoMigrateFail2BanScope(ctx context.Context) {
	s.fail2BanMu.Lock()
	defer s.fail2BanMu.Unlock()

	jail := s.cfg.Fail2BanJailFile
	action := s.cfg.Fail2BanActionFile
	if jail == "" || action == "" {
		return
	}
	if _, err := os.Stat(jail); err != nil {
		return
	}
	cfg := readFail2BanJail(jail)
	if cfg.BanAction != "ufw" {
		return
	}
	client, err := exec.LookPath("fail2ban-client")
	if err != nil {
		return
	}
	previousAction, _ := os.ReadFile(action)
	if _, err := writeManagedFile(action, fail2BanActionContent, 0o644, 0, 0); err != nil {
		if s.logger != nil {
			s.logger.Warn("could not install the SSH-port Fail2Ban action", "err", err)
		}
		return
	}
	ignoreIPs, err := normalizeFail2BanIgnoreIPs(strings.Join(cfg.IgnoreIPs, " "))
	if err != nil {
		ignoreIPs = defaultFail2BanJail().IgnoreIPs
	}
	port := resolveFail2BanSSHPort(cfg.Port)
	next := cfg
	next.BanAction = "a8m-ufw-port"
	next.Port = strconv.Itoa(port)
	next.IgnoreIPs = ignoreIPs

	previous, _ := os.ReadFile(jail)
	if _, err := writeManagedFile(jail, renderFail2BanJail(next), 0o644, 0, 0); err != nil {
		if len(previousAction) > 0 {
			_, _ = writeManagedFile(action, string(previousAction), 0o644, 0, 0)
		} else {
			_ = removeManagedFile(action)
		}
		if s.logger != nil {
			s.logger.Warn("could not migrate the Fail2Ban jail to SSH-port bans", "err", err)
		}
		return
	}
	if result, err := runCommand(ctx, 20*time.Second, client, "-t"); err != nil {
		if len(previous) > 0 {
			_, _ = writeManagedFile(jail, string(previous), 0o644, 0, 0)
		}
		if len(previousAction) > 0 {
			_, _ = writeManagedFile(action, string(previousAction), 0o644, 0, 0)
		} else {
			_ = removeManagedFile(action)
		}
		if s.logger != nil {
			s.logger.Warn("Fail2Ban rejected the SSH-port jail migration; restored the previous files",
				"err", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
		}
		return
	}
	if commandValue(ctx, "systemctl", "is-active", "fail2ban") == "active" {
		// Reload does not reliably replace an action definition that changed
		// on disk; restart so the port-scoped action is the one the jail uses.
		if result, err := runCommand(ctx, 45*time.Second, "systemctl", "restart", "fail2ban"); err != nil && s.logger != nil {
			s.logger.Warn("Fail2Ban restart after SSH-port migration failed",
				"err", strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
		}
	}
	if s.logger != nil {
		s.logger.Info("migrated Fail2Ban SSH bans to the port-scoped UFW action", "port", port)
	}
}
