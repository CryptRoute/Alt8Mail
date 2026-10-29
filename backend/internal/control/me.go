package control

import (
	"net/http"
	"strings"

	"github.com/meridian-mail/meridian/backend/internal/auth"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user": sessionUserPayload(&sess.User), "session": sessionView(sess),
		"version": s.version, "serverTime": nowRFC3339(),
		"csrfToken": s.signCSRF(sess.CSRF),
	})
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !auth.VerifyPassword(sess.User.PasswordHash, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "PASSWORD", "Current password is incorrect")
		return
	}
	if len(strings.TrimSpace(body.NewPassword)) < 12 {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Use a panel password of at least 12 characters")
		return
	}
	if body.NewPassword == body.CurrentPassword {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Choose a password different from the current one")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PASSWORD", err.Error())
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.admin_users SET password_hash=$2, updated_at=now() WHERE id=$1
	`, sess.User.ID, hash)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.sessions WHERE user_id=$1 AND id<>$2`, sess.User.ID, sess.ID)
	s.actorAudit(r, sess, "auth.password_changed", sess.User.Email, "Changed panel password and revoked other sessions", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedOthers": true})
}
