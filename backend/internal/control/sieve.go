package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const controlSieveName = "alt8mail-control"

var sieveFolderPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,99}$`)
var sieveEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type sieveCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

type sieveAction struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type controlSieveRule struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Enabled    bool             `json:"enabled"`
	Match      string           `json:"match"`
	Conditions []sieveCondition `json:"conditions"`
	Action     sieveAction      `json:"action"`
}

func sieveQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return `"` + value + `"`
}

func validateSieveRules(rules []controlSieveRule) error {
	if len(rules) > 100 {
		return fmt.Errorf("a maximum of 100 rules is allowed")
	}
	for index, rule := range rules {
		label := fmt.Sprintf("Rule %d", index+1)
		if strings.TrimSpace(rule.Name) == "" {
			return fmt.Errorf("%s: name is required", label)
		}
		if len([]rune(rule.Name)) > 100 || strings.ContainsAny(rule.Name, "\r\n") {
			return fmt.Errorf("%s: name is invalid", label)
		}
		if rule.Match != "any" && rule.Match != "all" {
			return fmt.Errorf("%s: match must be all or any", label)
		}
		if len(rule.Conditions) == 0 || len(rule.Conditions) > 20 {
			return fmt.Errorf("%s: provide 1–20 conditions", label)
		}
		for _, condition := range rule.Conditions {
			if condition.Field != "from" && condition.Field != "to" && condition.Field != "subject" && condition.Field != "body" && condition.Field != "size" {
				return fmt.Errorf("%s: unsupported condition field", label)
			}
			if condition.Operator != "contains" && condition.Operator != "is" && condition.Operator != "matches" && condition.Operator != "notcontains" && condition.Operator != "over" && condition.Operator != "under" {
				return fmt.Errorf("%s: unsupported condition operator", label)
			}
			if condition.Field == "size" {
				if condition.Operator != "over" && condition.Operator != "under" {
					return fmt.Errorf("%s: size operators must be over or under", label)
				}
				if _, err := strconv.Atoi(condition.Value); err != nil {
					return fmt.Errorf("%s: size must be a whole number of kilobytes", label)
				}
			} else if strings.TrimSpace(condition.Value) == "" || len([]rune(condition.Value)) > 300 {
				return fmt.Errorf("%s: condition value is empty or too long", label)
			}
		}
		switch rule.Action.Type {
		case "move":
			if !sieveFolderPattern.MatchString(rule.Action.Value) {
				return fmt.Errorf("%s: folder name is invalid", label)
			}
		case "redirect":
			if !sieveEmailPattern.MatchString(rule.Action.Value) {
				return fmt.Errorf("%s: redirect needs a valid address", label)
			}
		case "markRead", "star", "discard", "keep":
		default:
			return fmt.Errorf("%s: unsupported action", label)
		}
	}
	return nil
}

func renderControlSieve(rules []controlSieveRule) string {
	var b strings.Builder
	b.WriteString("# Managed by Alt8Mail System Control. Changes are overwritten.\n")
	b.WriteString(`require ["fileinto","imap4flags","copy"];` + "\n\n")
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		tests := []string{}
		for _, condition := range rule.Conditions {
			switch condition.Field {
			case "size":
				kb, _ := strconv.Atoi(condition.Value)
				tests = append(tests, fmt.Sprintf("size :%s %d", condition.Operator, kb*1024))
			default:
				var test string
				operator := condition.Operator
				negate := false
				if operator == "notcontains" {
					operator, negate = "contains", true
				}
				switch condition.Field {
				case "from", "to":
					test = fmt.Sprintf("address :%s %s %s", operator, sieveQuote(titleCase(condition.Field)), sieveQuote(condition.Value))
				case "subject":
					test = fmt.Sprintf("header :%s %s %s", operator, sieveQuote("Subject"), sieveQuote(condition.Value))
				case "body":
					test = fmt.Sprintf("body :%s %s", operator, sieveQuote(condition.Value))
				}
				if negate {
					test = "not " + test
				}
				tests = append(tests, test)
			}
		}
		join := "allof"
		if rule.Match == "any" {
			join = "anyof"
		}
		fmt.Fprintf(&b, "# %s\n", strings.ReplaceAll(strings.ReplaceAll(rule.Name, "\r", " "), "\n", " "))
		fmt.Fprintf(&b, "if %s(%s) {\n", join, strings.Join(tests, ",\n    "))
		switch rule.Action.Type {
		case "move":
			fmt.Fprintf(&b, "  fileinto %s;\n  stop;\n", sieveQuote(rule.Action.Value))
		case "markRead":
			b.WriteString("  addflag \"\\\\Seen\";\n")
		case "star":
			b.WriteString("  addflag \"\\\\Flagged\";\n")
		case "discard":
			b.WriteString("  discard;\n  stop;\n")
		case "redirect":
			fmt.Fprintf(&b, "  redirect :copy %s;\n  keep;\n", sieveQuote(rule.Action.Value))
		case "keep":
			b.WriteString("  keep;\n")
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func (s *Server) mailboxSieveUser(ctx context.Context, mailboxID string) (string, error) {
	var email string
	err := s.db.QueryRow(ctx, `SELECT email::text FROM public.mailboxes WHERE id=$1`, mailboxID).Scan(&email)
	return email, err
}

func (s *Server) loadSieveRules(ctx context.Context, userID string) ([]controlSieveRule, bool, error) {
	var raw []byte
	var managed bool
	err := s.db.QueryRow(ctx, `SELECT rules, managed FROM control.sieve_rules WHERE user_id=$1`, userID).Scan(&raw, &managed)
	if err != nil {
		if errorsIsNoRows(err) {
			return []controlSieveRule{}, false, nil
		}
		return nil, false, err
	}
	var rules []controlSieveRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, managed, err
	}
	return rules, managed, nil
}

func (s *Server) sieveListEmail(ctx context.Context, email string) string {
	result, err := runCommand(ctx, 12*time.Second, "doveadm", "sieve", "list", "-u", email)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func (s *Server) handleSieveGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var userID, email string
	if err := s.db.QueryRow(r.Context(), `SELECT user_id::text, email::text FROM public.mailboxes WHERE id=$1`, id).Scan(&userID, &email); err != nil {
		writeFromError(w, err)
		return
	}
	rules, managed, err := s.loadSieveRules(r.Context(), userID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rules": rules, "managed": managed, "scriptName": controlSieveName,
		"script": renderControlSieve(rules), "dovecotScripts": s.sieveListEmail(r.Context(), email),
		"email": email,
	})
}

func (s *Server) handleSievePreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Rules []controlSieveRule `json:"rules"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if err := validateSieveRules(body.Rules); err != nil {
		writeError(w, http.StatusBadRequest, "SIEVE", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"script": renderControlSieve(body.Rules), "ruleCount": len(body.Rules)})
}

func (s *Server) handleSieveSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var userID, email string
	if err := s.db.QueryRow(r.Context(), `SELECT user_id::text, email::text FROM public.mailboxes WHERE id=$1`, id).Scan(&userID, &email); err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		Rules []controlSieveRule `json:"rules"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if err := validateSieveRules(body.Rules); err != nil {
		writeError(w, http.StatusBadRequest, "SIEVE", err.Error())
		return
	}
	raw, _ := json.Marshal(body.Rules)
	enabled := []controlSieveRule{}
	for _, rule := range body.Rules {
		if rule.Enabled {
			enabled = append(enabled, rule)
		}
	}
	if len(enabled) == 0 {
		// Only our named script is removed. A script uploaded through
		// ManageSieve is never touched, and users without panel rules get no
		// managed script at all.
		_, _ = runCommand(r.Context(), 12*time.Second, "doveadm", "sieve", "delete", "-u", email, controlSieveName)
		_, err := s.db.Exec(r.Context(), `
			INSERT INTO control.sieve_rules (user_id, rules, revision, managed, updated_at)
			VALUES ($1,$2::jsonb,1,false,now())
			ON CONFLICT (user_id) DO UPDATE SET rules=EXCLUDED.rules, revision=control.sieve_rules.revision+1,
			  managed=false, updated_at=now()
		`, userID, raw)
		if err != nil {
			writeFromError(w, err)
			return
		}
		s.actorAudit(r, sess, "mail.sieve_cleared", email, "Removed managed Sieve rules", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "managed": false, "active": false})
		return
	}
	script := renderControlSieve(body.Rules)
	// Pigeonhole on Debian does not implement the non-standard :create
	// extension. Create target folders explicitly, then deploy the plain
	// fileinto script. Failures are ignored because the folder may already
	// exist; the final script validation still decides success.
	for _, rule := range enabled {
		if rule.Action.Type == "move" {
			_, _ = runCommand(r.Context(), 12*time.Second, "doveadm", "mailbox", "create", "-u", email, rule.Action.Value)
		}
	}
	result, err := runCommandWithInput(r.Context(), 20*time.Second, script, "doveadm", "sieve", "put", "-a", "-u", email, controlSieveName)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SIEVE", "Dovecot rejected the script: "+strings.TrimSpace(result.Stderr))
		return
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO control.sieve_rules (user_id, rules, revision, managed, updated_at)
		VALUES ($1,$2::jsonb,1,true,now())
		ON CONFLICT (user_id) DO UPDATE SET rules=EXCLUDED.rules, revision=control.sieve_rules.revision+1,
		  managed=true, updated_at=now()
	`, userID, raw)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "mail.sieve_saved", email, "Deployed managed Sieve rules", map[string]any{"rules": len(body.Rules)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "managed": true, "active": true, "script": script, "dovecotScripts": s.sieveListEmail(r.Context(), email)})
}

func runCommandWithInput(ctx context.Context, timeout time.Duration, input, name string, args ...string) (commandResult, error) {
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	cmd.Stdout = &limitedWriter{builder: &stdout, limit: 1 << 20}
	cmd.Stderr = &limitedWriter{builder: &stderr, limit: 1 << 20}
	err := cmd.Run()
	result := commandResult{Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, err
}

func titleCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
