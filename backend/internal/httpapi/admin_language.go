package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// adminUpdateOwnLanguage stores an administrator's explicit interface
// language. An empty value clears the preference so the system default
// selected in the Control Plane applies.
func (s *Server) adminUpdateOwnLanguage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Language string `json:"language"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	language := strings.TrimSpace(body.Language)
	if language != "" && (!preferenceLanguages[language] || !s.webmailLanguageEnabled(r.Context(), language)) {
		writeError(w, http.StatusBadRequest, "LANGUAGE", "Choose an enabled language")
		return
	}
	stored := map[string]any{}
	_ = json.Unmarshal(sess.User.Preferences, &stored)
	if language == "" {
		delete(stored, "language")
	} else {
		stored["language"] = language
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LANGUAGE", "Could not save the language preference")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE users SET preferences=$2, updated_at=now() WHERE id=$1
	`, sess.User.ID, encoded); err != nil {
		s.logger.Error("admin language preference save failed", "user", sess.User.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "LANGUAGE", "Could not save the language preference")
		return
	}
	sess.User.Preferences = encoded
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.language_updated", sess.User.Email,
		"Updated the administration console language", map[string]any{"language": language})
	writeJSON(w, http.StatusOK, map[string]any{"language": language})
}
