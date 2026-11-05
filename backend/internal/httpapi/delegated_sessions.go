package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const delegatedLaunchTTL = 90 * time.Second

// delegatedSessionAllowed keeps a delegated mailbox window inside the mailbox
// and collaboration surface. Account-security and credential endpoints stay
// reserved for the person who owns the sign-in, not for a delegate.
func delegatedSessionAllowed(method, path string) bool {
	switch {
	case path == "/api/delegation/session":
		return method == http.MethodDelete
	case strings.HasPrefix(path, "/api/delegation/"):
		return false
	case strings.HasPrefix(path, "/api/security"):
		return false
	case strings.HasPrefix(path, "/api/password"):
		return false
	case strings.HasPrefix(path, "/api/onboarding"):
		return false
	case strings.HasPrefix(path, "/api/storage/credentials"), strings.HasPrefix(path, "/api/storage/oauth"):
		return false
	case strings.HasPrefix(path, "/api/storage"):
		return method == http.MethodGet || method == http.MethodHead
	case strings.HasPrefix(path, "/api/file-shares"):
		return false
	case strings.HasPrefix(path, "/api/files/") && strings.HasSuffix(path, "/share"):
		return false
	case strings.HasPrefix(path, "/api/calendar/calendars/") &&
		(strings.Contains(path, "/share") || strings.HasSuffix(path, "/publish") || strings.HasSuffix(path, "/leave")):
		return false
	case strings.HasPrefix(path, "/api/mail/sync"):
		return method == http.MethodGet || method == http.MethodHead
	case strings.HasPrefix(path, "/api/dev"):
		return false
	case path == "/api/audit", strings.HasPrefix(path, "/api/audit.csv"), path == "/api/workspace/audit":
		return false
	}
	readOnly := method == http.MethodGet || method == http.MethodHead
	switch path {
	case "/api/profile", "/api/preferences", "/api/trust":
		return readOnly
	}
	if strings.HasPrefix(path, "/api/push") {
		return readOnly
	}
	return true
}

// delegatedSendPath marks operations that deliver mail on the mailbox owner's
// behalf. A delegate without Send permission can still read and organise the
// mailbox, but cannot send messages, scheduled sends or calendar invitations.
func delegatedSendPath(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	if path == "/api/send" || strings.HasSuffix(path, "/send-now") ||
		path == "/api/reactions" || strings.HasSuffix(path, "/reaction") ||
		strings.HasPrefix(path, "/api/reaction-rules") || path == "/api/sieve" ||
		(strings.HasPrefix(path, "/api/outbox/") && method == http.MethodPatch) {
		return true
	}
	if strings.HasPrefix(path, "/api/calendar/events") || strings.HasPrefix(path, "/api/calendar/polls") {
		return true
	}
	return false
}

// delegatedAccessCurrent re-reads the live grant so revoking a delegate or
// removing a group member closes already-open windows on their next request.
// It also refreshes the send permission without forcing the user to reopen.
func (s *Server) delegatedAccessCurrent(r *http.Request, sess *sessionRecord) bool {
	if sess == nil || !sess.IsDelegated {
		return true
	}
	delegation, ok := s.delegationFor(r.Context(), sess.User.TenantID, sess.User.ID, sess.DelegatedByID)
	if !ok {
		return false
	}
	sess.DelegatedCanSend = delegation.CanSend
	return true
}

func delegatedAuditDetail(sess *sessionRecord, detail string) string {
	if sess == nil || !sess.IsDelegated {
		return detail
	}
	actor := sess.DelegatedByEmail
	if actor == "" {
		actor = sess.DelegatedByID
	}
	note := "Delegated mailbox session"
	if actor != "" {
		note += " opened by " + actor
	}
	if strings.TrimSpace(detail) == "" {
		return note
	}
	return detail + " · " + note
}

func delegatedMailboxView(item mailboxDelegation) map[string]any {
	return map[string]any{
		"id": item.OwnerID, "ownerId": item.OwnerID,
		"name": item.OwnerName, "email": item.OwnerEmail,
		"initials": item.OwnerInitials, "color": item.OwnerColor,
		"canRead": item.CanRead, "canSend": item.CanSend,
	}
}

func (s *Server) delegatedMailboxViews(ctx context.Context, tenantID, delegateID string) []map[string]any {
	delegations := s.delegationsFor(ctx, tenantID, delegateID)
	result := make([]map[string]any, 0, len(delegations))
	for _, delegation := range delegations {
		result = append(result, delegatedMailboxView(delegation))
	}
	return result
}

// launchDelegatedSession mints a one-time code for a new window. The code is
// short-lived and tied to this sign-in, so a copied URL cannot become a
// permanent mailbox credential.
func (s *Server) launchDelegatedSession(w http.ResponseWriter, r *http.Request) {
	parent := s.requireSessionFromContext(r)
	if parent == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
		return
	}
	if parent.IsDelegated {
		writeError(w, http.StatusForbidden, "DELEGATED_CHAIN", "Open another mailbox from your primary Alt8Mail window")
		return
	}
	var body struct {
		OwnerID string `json:"ownerId"`
		Email   string `json:"email"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	ownerID := strings.TrimSpace(body.OwnerID)
	if ownerID == "" && strings.TrimSpace(body.Email) != "" {
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT id::text FROM users
			WHERE tenant_id = $1 AND lower(email::text) = lower($2) AND deleted_at IS NULL
		`, parent.User.TenantID, strings.TrimSpace(body.Email)).Scan(&ownerID)
	}
	if ownerID == "" {
		writeError(w, http.StatusBadRequest, "DELEGATE_OWNER", "Choose a delegated mailbox")
		return
	}
	delegation, ok := s.delegationFor(r.Context(), parent.User.TenantID, ownerID, parent.User.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "DELEGATE_DENIED", "You do not have access to that mailbox")
		return
	}
	if delegation.OwnerID == parent.User.ID {
		writeError(w, http.StatusBadRequest, "DELEGATE_SELF", "That is your own mailbox")
		return
	}
	code := randomToken(32)
	expiresAt := time.Now().Add(delegatedLaunchTTL)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO delegated_launch_tokens (id, parent_session_id, delegate_user_id, owner_user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, hashToken(code), parent.ID, parent.User.ID, delegation.OwnerID, expiresAt)
	if err != nil {
		s.logger.Error("delegated launch code", "err", err)
		writeError(w, http.StatusInternalServerError, "DELEGATE_LAUNCH", "Could not open that mailbox")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM delegated_launch_tokens WHERE expires_at < now() - interval '1 day'`)
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":      code,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		"mailbox":   delegatedMailboxView(delegation),
	})
}

// redeemDelegatedSession exchanges a launch code for a dedicated bearer token
// that lives only in the new window's sessionStorage. It never writes cookies,
// so the primary mailbox session in the original window is untouched.
func (s *Server) redeemDelegatedSession(w http.ResponseWriter, r *http.Request) {
	parent := s.requireSessionFromContext(r)
	if parent == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in to continue")
		return
	}
	if parent.IsDelegated {
		writeError(w, http.StatusForbidden, "DELEGATED_CHAIN", "Open another mailbox from your primary Alt8Mail window")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "DELEGATE_CODE", "This mailbox link is incomplete")
		return
	}
	var ownerID string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE delegated_launch_tokens
		SET used_at = now()
		WHERE id = $1 AND parent_session_id = $2 AND delegate_user_id = $3
		  AND used_at IS NULL AND expires_at > now()
		RETURNING owner_user_id::text
	`, hashToken(code), parent.ID, parent.User.ID).Scan(&ownerID)
	if err != nil {
		writeError(w, http.StatusGone, "DELEGATE_LINK_EXPIRED", "This delegated mailbox link has expired. Open it again from your main mailbox.")
		return
	}
	delegation, ok := s.delegationFor(r.Context(), parent.User.TenantID, ownerID, parent.User.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "DELEGATE_DENIED", "That mailbox delegation has been removed")
		return
	}
	owner, err := s.userByEmail(r.Context(), delegation.OwnerEmail)
	if err != nil || owner.ID != ownerID {
		writeError(w, http.StatusGone, "DELEGATE_OWNER", "That mailbox is no longer available")
		return
	}
	rawToken, csrf, expiresAt, err := s.createDelegatedSession(r, parent, owner, delegation.CanSend)
	if err != nil {
		if errors.Is(err, errDelegatedParentExpired) {
			writeError(w, http.StatusGone, "DELEGATE_LINK_EXPIRED", "Your primary session has ended. Open the delegated mailbox again after signing in.")
			return
		}
		s.logger.Error("delegated session", "err", err)
		writeError(w, http.StatusInternalServerError, "DELEGATE_SESSION", "Could not open that mailbox")
		return
	}
	s.audit(r, parent.User, "delegation.opened", delegation.OwnerEmail,
		"Opened a delegated mailbox window for "+delegation.OwnerEmail+" · session ends with the primary sign-in")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":            rawToken,
		"csrf":             csrf,
		"expiresAt":        expiresAt.UTC().Format(time.RFC3339),
		"sessionKind":      "delegated",
		"delegated":        true,
		"delegatedCanSend": delegation.CanSend,
		"mailbox":          delegatedMailboxView(delegation),
	})
}

var errDelegatedParentExpired = errors.New("delegated parent session expired")

func (s *Server) createDelegatedSession(r *http.Request, parent *sessionRecord, owner userRecord, canSend bool) (string, string, time.Time, error) {
	ttl := s.cfg.SessionTTL
	remaining := time.Until(parent.ExpiresAt)
	if remaining <= 0 {
		return "", "", time.Time{}, errDelegatedParentExpired
	}
	if remaining < ttl {
		ttl = remaining
	}
	raw := randomToken(32)
	csrf := randomToken(32)
	expiresAt := time.Now().Add(ttl)
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO sessions (
			id, user_id, tenant_id, csrf_token, ip, user_agent, expires_at,
			parent_session_id, delegated_by_user_id, delegated_owner_id,
			delegated_can_send, kind
		) VALUES ($1, $2, $3, $4, NULLIF($5, '')::inet, $6, $7, $8, $9, $10, $11, 'delegated')
	`, hashToken(raw), owner.ID, owner.TenantID, csrf, clientIP(r), sanitizeUserAgent(r.UserAgent()),
		expiresAt, parent.ID, parent.User.ID, owner.ID, canSend)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return raw, csrf, expiresAt, nil
}

func (s *Server) endDelegatedSession(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if sess == nil || !sess.IsDelegated {
		writeError(w, http.StatusBadRequest, "NOT_DELEGATED", "This is not a delegated mailbox window")
		return
	}
	s.audit(r, sess.User, "delegation.closed", sess.User.Email, "Closed a delegated mailbox window")
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE id = $1 AND parent_session_id IS NOT NULL`, sess.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegated": true})
}
