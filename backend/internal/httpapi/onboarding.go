package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// getOnboarding gives the webmail a small, cache-free status payload while the
// guided setup overlay is open. Bootstrap also carries the same flag.
func (s *Server) getOnboarding(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	user, preferences := s.publicUserWithState(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"onboardingRequired":     sess.User.OnboardingRequired,
		"passwordChangeRequired": passwordChangeRequired(sess.User),
		"user":                   user,
		"preferences":            preferences,
	})
}

type onboardingBody struct {
	DisplayName string `json:"displayName"`
	TimeZone    string `json:"timeZone"`
	TimeFormat  string `json:"timeFormat"`
	Language    string `json:"language"`
	Theme       string `json:"theme"`
	Accent      string `json:"accent"`
}

// completeOnboarding is the only way to leave the mandatory first-login flow.
// The server flag is cleared here, after the profile name and sending identity
// labels are updated, so a reload or a second browser cannot skip the flow.
func (s *Server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !sess.User.OnboardingRequired {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alreadyComplete": true})
		return
	}
	if passwordChangeRequired(sess.User) {
		writeError(w, http.StatusConflict, "PASSWORD_CHANGE_REQUIRED", "Set a new password in the security step before finishing setup")
		return
	}
	var body onboardingBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" || len([]rune(name)) > 120 {
		writeError(w, http.StatusBadRequest, "ONBOARDING_NAME", "Enter your full name (1–120 characters)")
		return
	}
	zone := strings.TrimSpace(body.TimeZone)
	if zone == "" {
		writeError(w, http.StatusBadRequest, "ONBOARDING_TIMEZONE", "Choose your time zone")
		return
	}
	if _, err := time.LoadLocation(zone); err != nil {
		writeError(w, http.StatusBadRequest, "ONBOARDING_TIMEZONE", "Choose a valid IANA time zone")
		return
	}
	timeFormat := strings.TrimSpace(body.TimeFormat)
	if timeFormat != "12h" && timeFormat != "24h" {
		writeError(w, http.StatusBadRequest, "ONBOARDING_TIME_FORMAT", "Choose 12-hour or 24-hour time")
		return
	}
	language := strings.TrimSpace(body.Language)
	if language == "" {
		language = "en-US"
	}
	if !preferenceLanguages[language] {
		writeError(w, http.StatusBadRequest, "ONBOARDING_LANGUAGE", "Choose a supported language")
		return
	}
	theme := strings.TrimSpace(body.Theme)
	if theme != "light" && theme != "dark" && theme != "system" {
		writeError(w, http.StatusBadRequest, "ONBOARDING_THEME", "Choose light, dark or system theme")
		return
	}
	accent := strings.TrimSpace(body.Accent)
	if accent != "blue" && accent != "teal" && accent != "violet" && accent != "rose" {
		writeError(w, http.StatusBadRequest, "ONBOARDING_ACCENT", "Choose a supported accent color")
		return
	}
	ctx := r.Context()
	initials := initialsForName(name)
	tag, err := s.store.DB.Exec(ctx, `
		UPDATE users
		SET name=$2, initials=$3, onboarding_required=false, onboarding_completed_at=now(), updated_at=now()
		WHERE id=$1 AND tenant_id=$4 AND deleted_at IS NULL
	`, sess.User.ID, name, initials, sess.User.TenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusInternalServerError, "ONBOARDING", "Could not save your setup")
		return
	}
	// The primary alias label is the From display name in every client.
	// Only generic labels are rewritten; intentional alias names stay intact.
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE user_aliases SET label=$2
		WHERE user_id=$1 AND (
			kind='primary'
			OR (kind='alias' AND (trim(label)='' OR lower(label) IN ('primary', 'alias') OR label=$3))
		)
	`, sess.User.ID, name, sess.User.Name)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE mail_identities SET display_name=$2, updated_at=now()
		WHERE user_id=$1 AND email=$3
	`, sess.User.ID, name, sess.User.Email)

	var current map[string]any
	var encoded []byte
	err = s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		current = statePreferences(state, preferencesFor(sess.User))
		current["timeZone"] = zone
		current["timeFormat"] = timeFormat
		current["language"] = language
		current["theme"] = theme
		current["accent"] = accent
		userState := stateMap(state, "user")
		userState["name"] = name
		userState["preferences"] = current
		payload, err := json.Marshal(current)
		if err != nil {
			return err
		}
		encoded = payload
		appendStateAudit(state, "onboarding.completed", name, name,
			"Completed first-login setup · "+zone+" · "+timeFormat+" · "+language, clientIP(r), sanitizeUserAgent(r.UserAgent()))
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ONBOARDING", "Could not save your workspace preferences")
		return
	}
	if len(encoded) > 0 {
		_, _ = s.store.DB.Exec(ctx, `UPDATE users SET preferences=$2, updated_at=now() WHERE id=$1`, sess.User.ID, encoded)
	}
	s.auditRequest(r, sess, "onboarding.completed", name, "Completed the mandatory first-login setup")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"user": map[string]any{
			"id": sess.User.ID, "email": sess.User.Email, "name": name, "initials": initials,
			"onboardingRequired": false, "passwordChangeRequired": false,
		},
		"preferences": current,
	})
}
