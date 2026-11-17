package control

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const legacyDovecotHomeExpression = `'/var/vmail/' || m.domain || '/' || m.local_part AS home`
const managedDovecotHomeExpression = `COALESCE(NULLIF(m.mail_home, ''), '/var/vmail/' || m.domain || '/' || m.local_part) AS home`

func (s *Server) handleStorageDovecotUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	path := filepath.Join(s.cfg.DovecotDir, "dovecot-sql.conf.ext")
	raw, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusConflict, "DOVECOT_CONFIG", "Could not read the Dovecot SQL userdb configuration: "+err.Error())
		return
	}
	current := string(raw)
	if strings.Contains(current, "mail_home") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already": true, "message": "Dovecot already reads mail_home."})
		return
	}
	if !strings.Contains(current, legacyDovecotHomeExpression) {
		writeError(w, http.StatusConflict, "DOVECOT_CONFIG", "This Dovecot userdb query is customized. No file was changed. Merge the mail_home fallback into the existing user_query manually, then recheck.")
		return
	}
	updated := strings.Replace(current, legacyDovecotHomeExpression, managedDovecotHomeExpression, 1)
	mode := os.FileMode(0o640)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	uid, gid, ownerOK := fileOwner(path)
	if !ownerOK {
		uid, gid = -1, -1
	}
	if _, err := writeManagedFile(path, updated, mode, uid, gid); err != nil {
		writeError(w, http.StatusInternalServerError, "DOVECOT_CONFIG", "Could not write the Dovecot userdb configuration: "+err.Error())
		return
	}
	restore := func() {
		_, _ = writeManagedFile(path, current, mode, uid, gid)
		_, _ = runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "dovecot")
	}
	if result, err := runCommand(r.Context(), 30*time.Second, "doveconf", "-n"); err != nil {
		restore()
		message := strings.TrimSpace(result.Stderr + " " + result.Stdout)
		writeError(w, http.StatusBadGateway, "DOVECOT_CONFIG", "Dovecot rejected the updated configuration; the previous file was restored. "+message)
		return
	}
	if result, err := runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "dovecot"); err != nil {
		restore()
		writeError(w, http.StatusBadGateway, "DOVECOT_RELOAD", "Dovecot reload failed; the previous configuration was restored. "+strings.TrimSpace(result.Stderr))
		return
	}
	email, home, detail := s.verifyDovecotHome(r)
	if email != "" && home == "" {
		restore()
		message := "Dovecot started with the new query but did not return a home for " + email + ". The previous configuration was restored."
		if detail != "" {
			message += " Dovecot said: " + truncateText(detail, 300)
		}
		writeError(w, http.StatusBadGateway, "DOVECOT_VERIFY", message)
		return
	}
	s.actorAudit(r, sess, "storage.dovecot_userdb_updated", "dovecot-sql.conf.ext", "Applied the mail_home-aware userdb query", map[string]any{
		"backup": true, "verifiedEmail": email, "verifiedHome": home,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "already": false, "verifiedEmail": email, "verifiedHome": home,
		"message": "Dovecot now resolves per-mailbox homes. Existing mail keeps the same path.",
	})
}

func (s *Server) verifyDovecotHome(r *http.Request) (string, string, string) {
	var email string
	if err := s.db.QueryRow(r.Context(), `
		SELECT email::text FROM public.mailboxes
		WHERE active = true ORDER BY created_at DESC LIMIT 1
	`).Scan(&email); err != nil || strings.TrimSpace(email) == "" {
		return "", "", ""
	}
	detail := ""
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		result, err := runCommand(r.Context(), 15*time.Second, "doveadm", "user", email)
		detail = strings.TrimSpace(result.Stderr + " " + result.Stdout)
		if err != nil {
			continue
		}
		if home := parseDovecotHome(result.Stdout); home != "" {
			return email, home, ""
		}
	}
	return email, "", detail
}

// parseDovecotHome accepts both the historical "home: /path" form and the
// tab-separated "home\t/path" output used by current Debian/Ubuntu builds.
func parseDovecotHome(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[0] == "home" || fields[0] == "home:") {
			return fields[1]
		}
	}
	return ""
}
