package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type listenerConfig struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	SASL     bool   `json:"saslRequired"`
}

func defaultListeners() []listenerConfig {
	return []listenerConfig{
		{Protocol: "smtp", Port: 25, TLS: "starttls", SASL: false},
		{Protocol: "submission", Port: 587, TLS: "starttls", SASL: true},
		{Protocol: "smtps", Port: 465, TLS: "implicit", SASL: true},
		{Protocol: "imap", Port: 143, TLS: "starttls", SASL: true},
		{Protocol: "imaps", Port: 993, TLS: "implicit", SASL: true},
		{Protocol: "pop3", Port: 110, TLS: "starttls", SASL: true},
		{Protocol: "pop3s", Port: 995, TLS: "implicit", SASL: true},
	}
}

func parsePostfixListeners(raw string) map[string]listenerConfig {
	result := map[string]listenerConfig{}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[7] != "smtpd" {
			continue
		}
		name := fields[0]
		protocol := ""
		port := 0
		switch {
		case strings.Contains(name, ":"):
			parts := strings.SplitN(name, ":", 2)
			if parsed, err := strconv.Atoi(parts[1]); err == nil {
				port = parsed
			}
			protocol = protocolForPort(port)
		case name == "smtp":
			protocol, port = "smtp", 25
		case name == "submission":
			protocol, port = "submission", 587
		case name == "smtps":
			protocol, port = "smtps", 465
		default:
			if parsed, err := strconv.Atoi(name); err == nil {
				port = parsed
				protocol = protocolForPort(parsed)
			}
		}
		if protocol == "" {
			continue
		}
		tls := "starttls"
		sasl := false
		for j := i + 1; j < len(lines) && (strings.HasPrefix(lines[j], " ") || strings.HasPrefix(lines[j], "\t")); j++ {
			override := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[j]), "-o"))
			switch {
			case strings.HasPrefix(override, "smtpd_tls_wrappermode=yes"):
				tls = "implicit"
			case strings.HasPrefix(override, "smtpd_tls_security_level=encrypt"):
				if tls != "implicit" {
					tls = "starttls"
				}
			case strings.HasPrefix(override, "smtpd_tls_security_level=none"):
				tls = "none"
			case strings.HasPrefix(override, "smtpd_sasl_auth_enable=yes"):
				sasl = true
			}
		}
		result[protocol] = listenerConfig{Protocol: protocol, Port: port, TLS: tls, SASL: sasl}
	}
	return result
}

func protocolForPort(port int) string {
	switch port {
	case 25:
		return "smtp"
	case 587:
		return "submission"
	case 465:
		return "smtps"
	}
	return ""
}

func parseDovecotListeners(ctx context.Context, s *Server) map[string]listenerConfig {
	result := map[string]listenerConfig{
		"imap":  {Protocol: "imap", Port: 143, TLS: "starttls", SASL: true},
		"imaps": {Protocol: "imaps", Port: 993, TLS: "implicit", SASL: true},
		"pop3":  {Protocol: "pop3", Port: 110, TLS: "starttls", SASL: true},
		"pop3s": {Protocol: "pop3s", Port: 995, TLS: "implicit", SASL: true},
	}
	path := filepath.Join(s.cfg.DovecotDir, "conf.d", "99-alt8mail-ports.conf")
	raw, err := os.ReadFile(path)
	if err != nil {
		return result
	}
	lines := strings.Split(string(raw), "\n")
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "inet_listener") {
			fields := strings.Fields(strings.NewReplacer("{", " ", "}", " ").Replace(trimmed))
			if len(fields) >= 2 {
				current = fields[1]
			}
			continue
		}
		if current == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "port =") {
			value, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "port =")))
			item := result[current]
			item.Port = value
			result[current] = item
		}
		if strings.HasPrefix(trimmed, "ssl =") {
			item := result[current]
			item.TLS = statusIf(strings.Contains(trimmed, "yes"), "implicit", "starttls")
			result[current] = item
		}
		if trimmed == "}" && current != "" {
			current = ""
		}
	}
	return result
}

func (s *Server) currentListeners(ctx context.Context) ([]listenerConfig, error) {
	raw, err := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "master.cf"))
	if err != nil {
		return nil, err
	}
	postfix := parsePostfixListeners(string(raw))
	dovecot := parseDovecotListeners(ctx, s)
	defaults := defaultListeners()
	out := make([]listenerConfig, 0, len(defaults))
	for _, fallback := range defaults {
		item := fallback
		if found, ok := postfix[fallback.Protocol]; ok {
			item = found
		}
		if found, ok := dovecot[fallback.Protocol]; ok {
			item = found
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	items, err := s.currentListeners(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"listeners": items, "dovecotPath": filepath.Join(s.cfg.DovecotDir, "conf.d", "99-alt8mail-ports.conf"),
		"masterCf": filepath.Join(s.cfg.PostfixDir, "master.cf"),
	})
}

func validateListeners(items []listenerConfig) error {
	if len(items) != 7 {
		return fmt.Errorf("all seven listeners (smtp, submission, smtps, imap, imaps, pop3, pop3s) are required")
	}
	seenProtocols := map[string]bool{}
	seenPorts := map[int]string{}
	for _, item := range items {
		if item.Port < 1 || item.Port > 65535 {
			return fmt.Errorf("%s port is invalid", item.Protocol)
		}
		if seenProtocols[item.Protocol] {
			return fmt.Errorf("duplicate protocol %s", item.Protocol)
		}
		seenProtocols[item.Protocol] = true
		if existing, ok := seenPorts[item.Port]; ok {
			return fmt.Errorf("%s and %s cannot share port %d", existing, item.Protocol, item.Port)
		}
		seenPorts[item.Port] = item.Protocol
		if item.TLS != "none" && item.TLS != "starttls" && item.TLS != "implicit" {
			return fmt.Errorf("%s has an invalid TLS mode", item.Protocol)
		}
		switch item.Protocol {
		case "smtps", "imaps", "pop3s":
			if item.TLS != "implicit" {
				return fmt.Errorf("%s must use implicit TLS", item.Protocol)
			}
		case "smtp":
			if item.TLS == "implicit" {
				return fmt.Errorf("inbound SMTP cannot use implicit TLS; use the SMTPS listener instead")
			}
		case "submission":
			if item.TLS != "starttls" {
				return fmt.Errorf("submission must use STARTTLS; use the SMTPS listener for implicit TLS")
			}
		}
	}
	return nil
}

func (s *Server) renderMasterCf(items []listenerConfig) string {
	raw, _ := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "master.cf"))
	return renderMasterCfFrom(string(raw), items)
}

// masterServiceBlock is one top-level inet smtpd service and its option lines.
type masterServiceBlock struct {
	start    int
	end      int
	protocol string
	lines    []string
}

func stripListenerBlock(lines []string) []string {
	out := []string{}
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# BEGIN ALT8MAIL LISTENERS" {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == "# END ALT8MAIL LISTENERS" {
				inBlock = false
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

func smtpdProtocolFor(name string, lines []string) string {
	switch strings.ToLower(name) {
	case "smtp", "25":
		return "smtp"
	case "submission", "587":
		return "submission"
	case "smtps", "465":
		return "smtps"
	}
	// Numeric custom ports are only treated as SMTP services when their
	// overrides make the role unambiguous.
	if _, err := strconv.Atoi(name); err != nil {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "smtpd_tls_wrappermode=yes") {
		return "smtps"
	}
	if strings.Contains(joined, "smtpd_sasl_auth_enable=yes") {
		return "submission"
	}
	return "smtp"
}

func parseMasterServiceBlocks(lines []string) []masterServiceBlock {
	blocks := []masterServiceBlock{}
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			i++
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[1] == "inet" && fields[7] == "smtpd" {
			j := i + 1
			for j < len(lines) && (strings.HasPrefix(lines[j], " ") || strings.HasPrefix(lines[j], "\t")) {
				j++
			}
			protocol := smtpdProtocolFor(fields[0], lines[i:j])
			if protocol != "" {
				blocks = append(blocks, masterServiceBlock{start: i, end: j, protocol: protocol, lines: append([]string{}, lines[i:j]...)})
			}
			i = j
			continue
		}
		i++
	}
	return blocks
}

func serviceNameFor(protocol string, port int) string {
	defaults := map[string]int{"smtp": 25, "submission": 587, "smtps": 465}
	if defaults[protocol] == port {
		return protocol
	}
	return strconv.Itoa(port)
}

func desiredSmtpdOptions(protocol string, listener listenerConfig) (map[string]string, map[string]bool) {
	desired := map[string]string{}
	remove := map[string]bool{}
	switch protocol {
	case "smtp":
		desired["syslog_name"] = "postfix/smtp"
		if listener.TLS == "none" {
			desired["smtpd_tls_security_level"] = "none"
		} else {
			desired["smtpd_tls_security_level"] = "may"
		}
		remove["smtpd_tls_wrappermode"] = true
		if listener.SASL {
			desired["smtpd_sasl_auth_enable"] = "yes"
			desired["smtpd_client_restrictions"] = "permit_mynetworks,permit_sasl_authenticated,reject"
		} else {
			remove["smtpd_sasl_auth_enable"] = true
		}
	case "submission":
		desired["syslog_name"] = "postfix/submission"
		desired["smtpd_tls_security_level"] = "encrypt"
		desired["smtpd_sasl_auth_enable"] = "yes"
		desired["smtpd_client_restrictions"] = "permit_sasl_authenticated,reject"
		desired["smtpd_relay_restrictions"] = "permit_sasl_authenticated,reject"
		if listener.TLS == "none" {
			desired["smtpd_tls_security_level"] = "none"
		}
		if listener.TLS == "implicit" {
			desired["smtpd_tls_wrappermode"] = "yes"
		} else {
			remove["smtpd_tls_wrappermode"] = true
		}
	case "smtps":
		desired["syslog_name"] = "postfix/smtps"
		desired["smtpd_tls_security_level"] = "encrypt"
		desired["smtpd_tls_wrappermode"] = "yes"
		desired["smtpd_sasl_auth_enable"] = "yes"
		desired["smtpd_client_restrictions"] = "permit_sasl_authenticated,reject"
		desired["smtpd_relay_restrictions"] = "permit_sasl_authenticated,reject"
	}
	return desired, remove
}

func rewriteMasterServiceBlock(lines []string, protocol string, listener listenerConfig) []string {
	if len(lines) == 0 {
		return lines
	}
	header := lines[0]
	fields := strings.Fields(header)
	desiredName := serviceNameFor(protocol, listener.Port)
	if len(fields) > 0 && fields[0] != desiredName {
		if index := strings.Index(header, fields[0]); index >= 0 {
			header = desiredName + header[index+len(fields[0]):]
		}
	}
	desired, remove := desiredSmtpdOptions(protocol, listener)
	seen := map[string]bool{}
	optionLines := []string{}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "-o ") {
			optionLines = append(optionLines, line)
			continue
		}
		keyValue := strings.TrimSpace(strings.TrimPrefix(trimmed, "-o "))
		key, value, ok := strings.Cut(keyValue, "=")
		if !ok {
			optionLines = append(optionLines, line)
			continue
		}
		key = strings.TrimSpace(key)
		currentValue := strings.TrimSpace(value)
		if remove[key] {
			continue
		}
		if target, ok := desired[key]; ok {
			seen[key] = true
			if target == currentValue {
				optionLines = append(optionLines, line)
			} else {
				optionLines = append(optionLines, "  -o "+key+"="+target)
			}
			continue
		}
		optionLines = append(optionLines, line)
	}
	order := []string{"syslog_name", "smtpd_tls_security_level", "smtpd_tls_wrappermode", "smtpd_sasl_auth_enable", "smtpd_client_restrictions", "smtpd_relay_restrictions"}
	for _, key := range order {
		if target, ok := desired[key]; ok && !seen[key] {
			optionLines = append(optionLines, "  -o "+key+"="+target)
		}
	}
	if protocol == "submission" || protocol == "smtps" {
		if !strings.Contains(strings.Join(optionLines, "\n"), "milter_macro_daemon_name=") {
			optionLines = append(optionLines, "  -o milter_macro_daemon_name=ORIGINATING")
		}
	}
	return append([]string{header}, optionLines...)
}

func newMasterServiceBlock(protocol string, listener listenerConfig) []string {
	name := serviceNameFor(protocol, listener.Port)
	block := []string{fmt.Sprintf("%-10s inet  n       -       n       -       -       smtpd", name)}
	desired, _ := desiredSmtpdOptions(protocol, listener)
	order := []string{"syslog_name", "smtpd_tls_security_level", "smtpd_tls_wrappermode", "smtpd_sasl_auth_enable", "smtpd_client_restrictions", "smtpd_relay_restrictions"}
	for _, key := range order {
		if target, ok := desired[key]; ok {
			block = append(block, "  -o "+key+"="+target)
		}
	}
	if protocol == "submission" || protocol == "smtps" {
		block = append(block, "  -o milter_macro_daemon_name=ORIGINATING")
	}
	return block
}

func renderMasterCfFrom(raw string, items []listenerConfig) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := stripListenerBlock(strings.Split(raw, "\n"))
	blocks := parseMasterServiceBlocks(lines)
	byProtocol := map[string]masterServiceBlock{}
	for _, block := range blocks {
		if _, exists := byProtocol[block.protocol]; !exists {
			byProtocol[block.protocol] = block
		}
	}
	listeners := map[string]listenerConfig{}
	for _, item := range items {
		listeners[item.Protocol] = item
	}
	out := []string{}
	last := 0
	for _, block := range blocks {
		chosen, ok := byProtocol[block.protocol]
		if !ok || chosen.start != block.start {
			continue
		}
		listener, ok := listeners[block.protocol]
		if !ok {
			continue
		}
		out = append(out, lines[last:block.start]...)
		out = append(out, rewriteMasterServiceBlock(block.lines, block.protocol, listener)...)
		last = block.end
	}
	out = append(out, lines[last:]...)
	for _, protocol := range []string{"smtp", "submission", "smtps"} {
		if _, ok := byProtocol[protocol]; ok {
			continue
		}
		listener, ok := listeners[protocol]
		if !ok {
			continue
		}
		out = append(out, "", "# Added by Alt8Mail System Control")
		out = append(out, newMasterServiceBlock(protocol, listener)...)
	}
	content := strings.TrimRight(strings.Join(out, "\n"), "\n")
	return content + "\n"
}

func renderDovecotPorts(items []listenerConfig) string {
	var b strings.Builder
	b.WriteString("# Managed by Alt8Mail System Control.\n")
	writeListener := func(protocol, listener string) {
		for _, item := range items {
			if item.Protocol != protocol {
				continue
			}
			fmt.Fprintf(&b, "  inet_listener %s {\n    port = %d\n    ssl = %s\n  }\n", listener, item.Port, statusIf(item.TLS == "implicit", "yes", "no"))
		}
	}
	b.WriteString("service imap-login {\n")
	writeListener("imap", "imap")
	writeListener("imaps", "imaps")
	b.WriteString("}\n\nservice pop3-login {\n")
	writeListener("pop3", "pop3")
	writeListener("pop3s", "pop3s")
	b.WriteString("}\n")
	return b.String()
}

func (s *Server) handlePortsPreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Listeners []listenerConfig `json:"listeners"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if err := validateListeners(body.Listeners); err != nil {
		writeError(w, http.StatusBadRequest, "PORTS", err.Error())
		return
	}
	current, _ := s.currentListeners(r.Context())
	currentMaster, _ := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "master.cf"))
	dovecotPath := filepath.Join(s.cfg.DovecotDir, "conf.d", "99-alt8mail-ports.conf")
	currentDovecot, _ := os.ReadFile(dovecotPath)
	afterMaster := s.renderMasterCf(body.Listeners)
	afterDovecot := renderDovecotPorts(body.Listeners)
	writeJSON(w, http.StatusOK, map[string]any{
		"current": current, "listeners": body.Listeners,
		"master": map[string]any{"before": string(currentMaster), "after": afterMaster,
			"diff": simpleLineDiff(string(currentMaster), afterMaster)},
		"dovecot": map[string]any{"before": string(currentDovecot), "after": afterDovecot,
			"diff": simpleLineDiff(string(currentDovecot), afterDovecot)},
	})
}

func (s *Server) handlePortsSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Listeners []listenerConfig `json:"listeners"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if err := validateListeners(body.Listeners); err != nil {
		writeError(w, http.StatusBadRequest, "PORTS", err.Error())
		return
	}
	masterBefore, err := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "master.cf"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	dovecotPath := filepath.Join(s.cfg.DovecotDir, "conf.d", "99-alt8mail-ports.conf")
	dovecotBefore, _ := os.ReadFile(dovecotPath)
	afterMaster := s.renderMasterCf(body.Listeners)
	afterDovecot := renderDovecotPorts(body.Listeners)
	if _, err := writeManagedFile(filepath.Join(s.cfg.PostfixDir, "master.cf"), afterMaster, 0o644, 0, 0); err != nil {
		writeFromError(w, err)
		return
	}
	if _, err := writeManagedFile(dovecotPath, afterDovecot, 0o644, 0, 0); err != nil {
		_ = os.WriteFile(filepath.Join(s.cfg.PostfixDir, "master.cf"), masterBefore, 0o644)
		writeFromError(w, err)
		return
	}
	postfixCheck, checkErr := runCommand(r.Context(), 30*time.Second, "postfix", "check")
	if checkErr != nil {
		_ = os.WriteFile(filepath.Join(s.cfg.PostfixDir, "master.cf"), masterBefore, 0o644)
		_ = os.WriteFile(dovecotPath, dovecotBefore, 0o644)
		writeError(w, http.StatusBadRequest, "POSTFIX", "postfix check rejected the listeners: "+strings.TrimSpace(postfixCheck.Stderr+postfixCheck.Stdout))
		return
	}
	dovecotCheck, dovecotErr := runCommand(r.Context(), 30*time.Second, "doveconf", "-n")
	if dovecotErr != nil {
		_ = os.WriteFile(filepath.Join(s.cfg.PostfixDir, "master.cf"), masterBefore, 0o644)
		_ = os.WriteFile(dovecotPath, dovecotBefore, 0o644)
		writeError(w, http.StatusBadRequest, "DOVECOT", "doveconf rejected the listeners: "+strings.TrimSpace(dovecotCheck.Stderr))
		return
	}
	restoreFiles := func() {
		_ = os.WriteFile(filepath.Join(s.cfg.PostfixDir, "master.cf"), masterBefore, 0o644)
		if len(dovecotBefore) == 0 {
			_ = os.Remove(dovecotPath)
		} else {
			_ = os.WriteFile(dovecotPath, dovecotBefore, 0o644)
		}
	}
	postfixReload, reloadErr := runCommand(r.Context(), 30*time.Second, "postfix", "reload")
	if reloadErr != nil {
		restoreFiles()
		_, _ = runCommand(r.Context(), 30*time.Second, "postfix", "reload")
		writeError(w, http.StatusBadGateway, "RELOAD", "Postfix reload failed; listener files were rolled back: "+strings.TrimSpace(postfixReload.Stderr))
		return
	}
	dovecotReload, dovecotReloadErr := runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "dovecot")
	if dovecotReloadErr != nil {
		restoreFiles()
		_, _ = runCommand(r.Context(), 30*time.Second, "postfix", "reload")
		_, _ = runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "dovecot")
		writeError(w, http.StatusBadGateway, "RELOAD", "Dovecot reload failed; listener files were rolled back: "+strings.TrimSpace(dovecotReload.Stderr))
		return
	}
	_ = s.probe.serviceStatuses(r.Context(), true)
	s.hub.Broadcast("services.ports", map[string]any{"listeners": body.Listeners})
	s.actorAudit(r, sess, "services.ports_updated", "Mail listeners", "Updated SMTP/IMAP/POP3 listener ports",
		map[string]any{"listeners": body.Listeners})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "listeners": body.Listeners})
}

func simpleLineDiff(before, after string) []string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	beforeSet := map[string]int{}
	afterSet := map[string]int{}
	for _, line := range beforeLines {
		beforeSet[line]++
	}
	for _, line := range afterLines {
		afterSet[line]++
	}
	diff := []string{}
	for _, line := range beforeLines {
		if afterSet[line] > 0 {
			afterSet[line]--
			continue
		}
		if strings.TrimSpace(line) != "" {
			diff = append(diff, "- "+line)
		}
	}
	for _, line := range afterLines {
		if beforeSet[line] > 0 {
			beforeSet[line]--
			continue
		}
		if strings.TrimSpace(line) != "" {
			diff = append(diff, "+ "+line)
		}
	}
	if len(diff) == 0 {
		return []string{"No changes"}
	}
	return diff
}
