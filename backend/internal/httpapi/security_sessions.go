package httpapi

import (
	"net/http"
	"strings"
	"time"
)

type sessionView struct {
	ID        string `json:"id"`
	Current   bool   `json:"current"`
	Device    string `json:"device"`
	IP        string `json:"ip"`
	CreatedAt string `json:"createdAt"`
	LastSeen  string `json:"lastSeenAt"`
	ExpiresAt string `json:"expiresAt"`
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, COALESCE(ip::text, ''), user_agent, created_at, last_seen_at, expires_at
		FROM sessions
		WHERE user_id = $1 AND expires_at > now()
		ORDER BY (id = $2) DESC, last_seen_at DESC
	`, sess.User.ID, sess.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not load sign-in sessions")
		return
	}
	defer rows.Close()
	result := []sessionView{}
	for rows.Next() {
		var view sessionView
		var userAgent string
		var created, lastSeen, expires time.Time
		if err := rows.Scan(&view.ID, &view.IP, &userAgent, &created, &lastSeen, &expires); err != nil {
			continue
		}
		view.Current = view.ID == sess.ID
		if slash := strings.Index(view.IP, "/"); slash > 0 {
			view.IP = view.IP[:slash]
		}
		view.Device = describeSessionDevice(userAgent)
		view.CreatedAt = created.UTC().Format(time.RFC3339)
		view.LastSeen = lastSeen.UTC().Format(time.RFC3339)
		view.ExpiresAt = expires.UTC().Format(time.RFC3339)
		result = append(result, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": result, "count": len(result)})
}

func (s *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	tag, err := s.store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, sess.User.ID, sess.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not revoke other sessions")
		return
	}
	s.auditRequest(r, sess, "security.sessions_revoked", "Other sign-in sessions",
		"Revoked all other browser sessions")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": tag.RowsAffected()})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "SESSION_ID", "Session id is required")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE id = $1 AND user_id = $2`, id, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSIONS", "Could not revoke that session")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Session not found")
		return
	}
	if id == sess.ID {
		s.clearSessionCookies(w)
	}
	s.auditRequest(r, sess, "security.session_revoked", "Browser session", "Revoked a sign-in session")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "current": id == sess.ID})
}

func (s *Server) getSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	security, methods, passkeys, remaining := s.securityState(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"enrollmentRequired": sess.User.EnrollmentRequired,
		"security":           security,
		"methods":            methods,
		"passkeys":           passkeys,
		"recoveryRemaining":  remaining,
	})
}

func (s *Server) patchSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		EnrollmentRequired *bool `json:"enrollmentRequired"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	if body.EnrollmentRequired == nil {
		writeError(w, http.StatusBadRequest, "POLICY", "enrollmentRequired is required")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `UPDATE users SET enrollment_required = $2, updated_at = now() WHERE id = $1`, sess.User.ID, *body.EnrollmentRequired); err != nil {
		writeError(w, http.StatusInternalServerError, "POLICY", "Could not save the enrollment policy")
		return
	}
	detail := "First-time second-factor enrollment disabled"
	if *body.EnrollmentRequired {
		detail = "First-time second-factor enrollment enabled"
	}
	s.auditRequest(r, sess, "security.enrollment_policy", "First-time enrollment", detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enrollmentRequired": *body.EnrollmentRequired})
}

func describeSessionDevice(userAgent string) string {
	ua := strings.ToLower(userAgent)
	browser := "Browser"
	switch {
	case strings.Contains(ua, "edg/"):
		browser = "Edge"
	case strings.Contains(ua, "chrome/") && !strings.Contains(ua, "chromium"):
		browser = "Chrome"
	case strings.Contains(ua, "firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "safari/") && !strings.Contains(ua, "chrome"):
		browser = "Safari"
	}
	os := "Unknown OS"
	switch {
	case strings.Contains(ua, "windows nt"):
		os = "Windows"
	case strings.Contains(ua, "android"):
		os = "Android"
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		os = "iOS"
	case strings.Contains(ua, "mac os x"):
		os = "macOS"
	case strings.Contains(ua, "linux"):
		os = "Linux"
	}
	return browser + " on " + os
}
