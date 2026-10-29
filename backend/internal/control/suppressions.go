package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) handleSuppressions(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	direction := strings.TrimSpace(r.URL.Query().Get("direction"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, direction, pattern::text, kind, reason, expires_at, created_at
		FROM control.suppressions
		WHERE ($1='' OR direction=$1)
		  AND ($2='' OR pattern::text ILIKE '%'||$2||'%' OR reason ILIKE '%'||$2||'%')
		ORDER BY direction, pattern
	`, direction, query)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, dir, pattern, kind, reason string
		var expires *time.Time
		var created time.Time
		if rows.Scan(&id, &dir, &pattern, &kind, &reason, &expires, &created) == nil {
			items = append(items, map[string]any{
				"id": id, "direction": dir, "pattern": pattern, "kind": kind, "reason": reason,
				"expiresAt": expires, "createdAt": created.UTC().Format(time.RFC3339),
				"expired": expires != nil && expires.Before(time.Now()),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": items})
}

func normalizeSuppressionPattern(pattern, kind string) (string, string, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if kind == "" {
		if strings.HasPrefix(pattern, "@") {
			kind = "catch_all"
		} else if strings.Contains(pattern, "@") {
			kind = "address"
		} else {
			kind = "domain"
		}
	}
	switch kind {
	case "address":
		parts := strings.Split(pattern, "@")
		if len(parts) != 2 || !validLocalPart(parts[0]) || !validDomain(parts[1]) {
			return "", "", fmt.Errorf("enter a valid email address")
		}
		pattern = parts[0] + "@" + normalizeDomain(parts[1])
	case "domain", "catch_all":
		pattern = strings.TrimPrefix(pattern, "@")
		if !validDomain(pattern) {
			return "", "", fmt.Errorf("enter a valid domain")
		}
		pattern = normalizeDomain(pattern)
		if kind == "catch_all" {
			pattern = "@" + pattern
		}
	default:
		return "", "", fmt.Errorf("kind must be address, domain or catch_all")
	}
	return pattern, kind, nil
}

func parseOptionalExpiry(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
		if err == nil {
			parsed = parsed.Add(23*time.Hour + 59*time.Minute)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("expiry must be an ISO timestamp or YYYY-MM-DD")
	}
	if parsed.Before(time.Now()) {
		return nil, fmt.Errorf("expiry must be in the future")
	}
	return &parsed, nil
}

func (s *Server) handleSuppressionCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Direction string `json:"direction"`
		Pattern   string `json:"pattern"`
		Kind      string `json:"kind"`
		Reason    string `json:"reason"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Direction != "outbound" && body.Direction != "inbound" {
		writeError(w, http.StatusBadRequest, "DIRECTION", "Direction must be outbound or inbound")
		return
	}
	pattern, kind, err := normalizeSuppressionPattern(body.Pattern, body.Kind)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PATTERN", err.Error())
		return
	}
	expires, err := parseOptionalExpiry(body.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "EXPIRY", err.Error())
		return
	}
	reason := truncateText(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body.Reason, "\r", " "), "\n", " ")), 300)
	if reason == "" {
		reason = "Address is suppressed by the system administrator"
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.suppressions (direction, pattern, kind, reason, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text
	`, body.Direction, pattern, kind, reason, expires, sess.User.ID).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "DUPLICATE", "That suppression already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	changed, err := s.writeSuppressionMaps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SUPPRESSION", "Saved, but Postfix maps could not be regenerated: "+err.Error())
		return
	}
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "delivery.suppression_created", pattern, "Created a "+body.Direction+" suppression",
		map[string]any{"kind": kind, "reason": reason})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "postfixChanged": changed})
}

func (s *Server) handleSuppressionUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Pattern   *string `json:"pattern"`
		Kind      *string `json:"kind"`
		Reason    *string `json:"reason"`
		ExpiresAt *string `json:"expiresAt"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var currentPattern, currentKind, currentReason string
	var currentExpiry *time.Time
	if err := s.db.QueryRow(r.Context(), `SELECT pattern::text, kind, reason, expires_at FROM control.suppressions WHERE id=$1`, id).Scan(&currentPattern, &currentKind, &currentReason, &currentExpiry); err != nil {
		writeFromError(w, err)
		return
	}
	pattern := currentPattern
	kind := currentKind
	if body.Pattern != nil || body.Kind != nil {
		if body.Pattern != nil {
			pattern = *body.Pattern
		}
		if body.Kind != nil {
			kind = *body.Kind
		}
		var err error
		pattern, kind, err = normalizeSuppressionPattern(pattern, kind)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PATTERN", err.Error())
			return
		}
	}
	reason := currentReason
	if body.Reason != nil {
		reason = truncateText(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(*body.Reason, "\r", " "), "\n", " ")), 300)
	}
	expiry := currentExpiry
	if body.ExpiresAt != nil {
		expiry, _ = parseOptionalExpiry(*body.ExpiresAt)
	}
	_, err := s.db.Exec(r.Context(), `
		UPDATE control.suppressions SET pattern=$2, kind=$3, reason=$4, expires_at=$5, updated_at=now()
		WHERE id=$1
	`, id, pattern, kind, reason, expiry)
	if err != nil {
		writeFromError(w, err)
		return
	}
	changed, err := s.writeSuppressionMaps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SUPPRESSION", err.Error())
		return
	}
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "delivery.suppression_updated", pattern, "Updated a suppression", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "postfixChanged": changed})
}

func (s *Server) handleSuppressionDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var pattern string
	_ = s.db.QueryRow(r.Context(), `SELECT pattern::text FROM control.suppressions WHERE id=$1`, id).Scan(&pattern)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.suppressions WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Suppression not found")
		return
	}
	changed, err := s.writeSuppressionMaps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SUPPRESSION", err.Error())
		return
	}
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "delivery.suppression_deleted", pattern, "Deleted a suppression", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "postfixChanged": changed})
}

func (s *Server) writeSuppressionMaps(ctx context.Context) (bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT direction, pattern::text, kind, reason
		FROM control.suppressions
		WHERE expires_at IS NULL OR expires_at > now()
		ORDER BY direction, pattern
	`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	outbound := []string{"# Managed by Alt8Mail System Control"}
	inbound := []string{"# Managed by Alt8Mail System Control"}
	for rows.Next() {
		var direction, pattern, kind, reason string
		if rows.Scan(&direction, &pattern, &kind, &reason) != nil {
			continue
		}
		reason = strings.ReplaceAll(strings.ReplaceAll(reason, "\r", " "), "\n", " ")
		line := "REJECT Suppressed by Alt8Mail: " + reason
		keys := []string{pattern}
		if kind == "domain" || kind == "catch_all" {
			domain := strings.TrimPrefix(pattern, "@")
			keys = []string{"@" + domain, domain}
		}
		for _, key := range keys {
			if direction == "inbound" {
				inbound = append(inbound, key+" "+line)
			} else {
				outbound = append(outbound, key+" "+line)
			}
		}
	}
	outPath := filepath.Join(s.cfg.PostfixDir, "suppressions")
	inPath := filepath.Join(s.cfg.PostfixDir, "suppressions_in")
	changedOut, err := writeManagedFile(outPath, strings.Join(outbound, "\n")+"\n", 0o644, 0, 0)
	if err != nil {
		return false, err
	}
	changedIn, err := writeManagedFile(inPath, strings.Join(inbound, "\n")+"\n", 0o644, 0, 0)
	if err != nil {
		return false, err
	}
	if changedOut {
		if _, err := runCommand(ctx, 15*time.Second, "postmap", outPath); err != nil {
			return true, fmt.Errorf("postmap suppressions failed")
		}
	}
	if changedIn {
		if _, err := runCommand(ctx, 15*time.Second, "postmap", inPath); err != nil {
			return true, fmt.Errorf("postmap suppressions_in failed")
		}
	}
	recipientChanged, err := postconfEnsureAppend(ctx, "smtpd_recipient_restrictions",
		"check_recipient_access hash:"+outPath)
	if err != nil {
		return changedOut || changedIn, err
	}
	senderChanged, err := postconfEnsureAppend(ctx, "smtpd_sender_restrictions",
		"check_sender_access hash:"+inPath)
	if err != nil {
		return changedOut || changedIn, err
	}
	_ = os.Chmod(outPath, 0o644)
	return changedOut || changedIn || recipientChanged || senderChanged, nil
}

func postconfEnsureAppend(ctx context.Context, key, directive string) (bool, error) {
	result, err := runCommand(ctx, 10*time.Second, "postconf", "-h", key)
	current := strings.TrimSpace(result.Stdout)
	if err == nil && strings.Contains(current, directive) {
		return false, nil
	}
	next := directive
	if current != "" {
		next += ", " + current
	}
	return postconfEnsure(ctx, key, next)
}
