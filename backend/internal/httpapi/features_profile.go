package httpapi

import (
	"context"
	"net/http"
	"strings"
)

type profileRecord struct {
	ID         string
	Email      string
	Name       string
	Title      string
	Department string
	Role       string
	Initials   string
	Color      string
}

func (s *Server) loadProfile(ctx context.Context, userID string) (profileRecord, error) {
	var profile profileRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text, email::text, name, COALESCE(title,''), COALESCE(department,''), role, initials, color
		FROM users WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&profile.ID, &profile.Email, &profile.Name, &profile.Title, &profile.Department,
		&profile.Role, &profile.Initials, &profile.Color)
	return profile, err
}

func publicProfile(profile profileRecord, editable bool) map[string]any {
	return map[string]any{
		"id": profile.ID, "email": profile.Email, "name": profile.Name,
		"title": profile.Title, "department": profile.Department,
		"role": profile.Role, "initials": profile.Initials, "color": profile.Color,
		"editable": editable,
	}
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	profile, err := s.loadProfile(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PROFILE", "Profile not found")
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":  publicProfile(profile, policy.Directory.ProfileEditing || webmailAdminRole(sess.User.Role)),
		"editable": policy.Directory.ProfileEditing || webmailAdminRole(sess.User.Role),
	})
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	if !policy.Directory.ProfileEditing && !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "PROFILE_LOCKED", "Your organization manages profile details for you")
		return
	}
	var body struct {
		Name       *string `json:"name"`
		Title      *string `json:"title"`
		Department *string `json:"department"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	profile, err := s.loadProfile(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PROFILE", "Profile not found")
		return
	}
	previousName := profile.Name
	if body.Name != nil {
		profile.Name = strings.TrimSpace(*body.Name)
	}
	if body.Title != nil {
		profile.Title = truncate(strings.TrimSpace(*body.Title), 160)
	}
	if body.Department != nil {
		profile.Department = truncate(strings.TrimSpace(*body.Department), 160)
	}
	if profile.Name == "" || len([]rune(profile.Name)) > 120 {
		writeError(w, http.StatusBadRequest, "PROFILE_NAME", "Enter a name between 1 and 120 characters")
		return
	}
	initials := initialsForName(profile.Name)
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE users SET name = $2, title = $3, department = $4, initials = $5, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
	`, profile.ID, profile.Name, profile.Title, profile.Department, initials)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PROFILE", "Could not save the profile")
		return
	}
	// The From header uses the alias label, so keep it aligned with the
	// account holder's real name instead of the legacy "Primary" label.
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE user_aliases SET label = $2
		WHERE user_id = $1 AND (
			kind = 'primary'
			OR (kind = 'alias' AND (trim(label) = '' OR lower(label) IN ('primary', 'alias') OR label = $3))
		)
	`, profile.ID, profile.Name, previousName)
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE mail_identities SET display_name = $2, updated_at = now()
		WHERE user_id = $1 AND email = $3 AND (trim(display_name) = '' OR display_name = $4)
	`, profile.ID, profile.Name, profile.Email, previousName)
	profile.Initials = initials
	s.auditRequest(r, sess, "profile.updated", profile.Name, "Updated self-service profile details")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": publicProfile(profile, true)})
}
