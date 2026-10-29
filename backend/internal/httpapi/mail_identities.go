package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	maxIdentities          = 20
	identityVerifyLifetime = 48 * time.Hour
)

type identityBody struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	ReplyTo   string `json:"replyTo"`
	Signature string `json:"signature"`
}

// mailIdentities returns the sendable addresses a user can choose in compose:
// database aliases (primary, shared aliases and admin-managed aliases) plus
// self-service identities. External identities remain visible but cannot send
// until their verification link is completed.
func (s *Server) mailIdentities(ctx context.Context, user userRecord) ([]map[string]any, error) {
	result := []map[string]any{}
	seen := map[string]bool{}

	rows, err := s.store.DB.Query(ctx, `
		SELECT id::text, email::text, label, kind, can_send
		FROM user_aliases WHERE user_id = $1 AND active = true ORDER BY created_at
	`, user.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, email, label, kind string
		var canSend bool
		if rows.Scan(&id, &email, &label, &kind, &canSend) == nil {
			reason := ""
			if !canSend {
				reason = "Delegated mailbox · read only"
			}
			item := map[string]any{
				"id": id, "email": strings.ToLower(email), "label": label, "kind": kind,
				"canSend": canSend, "status": "verified", "source": "alias",
				"reason": reason,
			}
			result = append(result, item)
			seen[strings.ToLower(email)] = true
		}
	}
	rows.Close()
	if len(result) == 0 {
		result = append(result, map[string]any{
			"id": "primary", "email": strings.ToLower(user.Email), "label": user.Name,
			"kind": "primary", "canSend": true, "status": "verified",
			"source": "alias", "reason": "",
		})
		seen[strings.ToLower(user.Email)] = true
	}

	rows, err = s.store.DB.Query(ctx, `
		SELECT id, email::text, display_name, status, COALESCE(reply_to::text, ''), signature,
		       COALESCE(verification_expires_at, created_at), created_at
		FROM mail_identities WHERE user_id = $1 ORDER BY created_at
	`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, email, name, status, replyTo, signature string
		var verificationExpires, created time.Time
		if rows.Scan(&id, &email, &name, &status, &replyTo, &signature, &verificationExpires, &created) != nil {
			continue
		}
		email = strings.ToLower(email)
		if seen[email] {
			continue
		}
		canSend := status == "verified"
		reason := ""
		if !canSend {
			reason = "Verification pending"
		}
		label := strings.TrimSpace(name)
		if label == "" {
			label = email
		}
		item := map[string]any{
			"id": id, "email": email, "label": label, "kind": "identity",
			"canSend": canSend, "status": status, "source": "identity",
			"reason": reason, "replyTo": replyTo, "signature": signature,
			"createdAt": created.UTC().Format(time.RFC3339),
		}
		if status == "pending" {
			item["verificationExpiresAt"] = verificationExpires.UTC().Format(time.RFC3339)
		}
		result = append(result, item)
		seen[email] = true
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, _ := result[i]["kind"].(string)
		right, _ := result[j]["kind"].(string)
		rank := map[string]int{"primary": 0, "alias": 1, "shared": 2, "identity": 3}
		if rank[left] != rank[right] {
			return rank[left] < rank[right]
		}
		le, _ := result[i]["email"].(string)
		re, _ := result[j]["email"].(string)
		return le < re
	})
	return result, rows.Err()
}

func identityAliases(identities []map[string]any) []any {
	result := make([]any, 0, len(identities))
	for _, item := range identities {
		alias := map[string]any{}
		for _, key := range []string{"id", "email", "label", "kind", "canSend", "reason", "source"} {
			alias[key] = item[key]
		}
		result = append(result, alias)
	}
	return result
}

// syncStateAliases keeps the legacy state alias list current so the existing
// compose/send/draft paths can validate every identity with aliasFor.
func (s *Server) syncStateAliases(ctx context.Context, user userRecord, state map[string]any) error {
	identities, err := s.mailIdentities(ctx, user)
	if err != nil {
		return err
	}
	userMap := stateMap(state, "user")
	if userMap == nil {
		userMap = map[string]any{}
		state["user"] = userMap
	}
	userMap["aliases"] = identityAliases(identities)
	return nil
}

func (s *Server) identitySendable(ctx context.Context, userID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	var allowed bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_aliases WHERE user_id=$1 AND email=$2 AND active=true AND can_send=true
		) OR EXISTS(
			SELECT 1 FROM mail_identities WHERE user_id=$1 AND email=$2 AND status='verified'
		)
	`, userID, email).Scan(&allowed)
	return allowed
}

func (s *Server) getIdentities(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	identities, err := s.mailIdentities(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IDENTITIES", "Could not load sending identities")
		return
	}
	domains := []string{}
	for domain := range s.tenantMailDomains(r.Context(), sess.User.TenantID) {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	writeJSON(w, http.StatusOK, map[string]any{
		"identities": identities, "count": len(identities), "domains": domains,
		"externalVerification": true,
	})
}

func (s *Server) createIdentity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body identityBody
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !sieveEmailPattern.MatchString(email) {
		writeError(w, http.StatusBadRequest, "EMAIL", "Enter a valid email address")
		return
	}
	if strings.EqualFold(email, sess.User.Email) {
		writeError(w, http.StatusConflict, "IDENTITY_EXISTS", "Your account address is already a sending identity")
		return
	}
	var existing int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM mail_identities WHERE user_id=$1`, sess.User.ID).Scan(&existing)
	if existing >= maxIdentities {
		writeError(w, http.StatusConflict, "IDENTITY_LIMIT", "You can add up to 20 additional identities")
		return
	}
	domains := s.tenantMailDomains(r.Context(), sess.User.TenantID)
	internal := false
	if index := strings.LastIndex(email, "@"); index >= 0 {
		internal = domains[email[index+1:]]
	}
	var aliasExists bool
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT EXISTS(SELECT 1 FROM user_aliases WHERE user_id=$1 AND email=$2 AND active=true)
	`, sess.User.ID, email).Scan(&aliasExists)
	if aliasExists {
		writeError(w, http.StatusConflict, "IDENTITY_EXISTS", "That address is already an available organization alias.")
		return
	}
	if internal {
		writeError(w, http.StatusForbidden, "IDENTITY_ALIAS", "This internal address is not assigned to you. Ask an administrator to add it as an alias.")
		return
	}
	replyTo := strings.ToLower(strings.TrimSpace(body.ReplyTo))
	if replyTo != "" && !sieveEmailPattern.MatchString(replyTo) {
		writeError(w, http.StatusBadRequest, "REPLY_TO", "Reply-to must be a valid email address")
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	signature := truncate(strings.TrimSpace(body.Signature), 4000)

	var id, status string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id, status FROM mail_identities WHERE user_id=$1 AND email=$2
	`, sess.User.ID, email).Scan(&id, &status)
	if err == nil {
		if status == "verified" {
			writeError(w, http.StatusConflict, "IDENTITY_EXISTS", "That address is already a verified identity")
			return
		}
		// Resend for an existing pending row.
		s.resendIdentityVerification(w, r, sess, id)
		return
	}

	id = newID("identity")
	status = "verified"
	var tokenHash any
	var expires any
	var sentAt any
	var verifiedAt any
	var verificationToken string
	if internal {
		verifiedAt = time.Now().UTC()
	} else {
		status = "pending"
		verificationToken = randomToken(32)
		tokenHash = hashToken(verificationToken)
		now := time.Now().UTC()
		expires = now.Add(identityVerifyLifetime)
		sentAt = now
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO mail_identities (id, tenant_id, user_id, email, display_name, reply_to, signature,
			status, verification_token_hash, verification_expires_at, verification_sent_at, verified_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12)
	`, id, sess.User.TenantID, sess.User.ID, email, name, replyTo, signature, status,
		tokenHash, expires, sentAt, verifiedAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IDENTITY", "Could not save the identity")
		return
	}
	response := map[string]any{
		"ok": true, "id": id, "email": email, "status": status,
		"verified": status == "verified",
	}
	if status == "pending" && verificationToken != "" {
		if err := s.sendIdentityVerification(r.Context(), sess.User, email, name, verificationToken); err != nil {
			s.logger.Warn("identity verification email failed", "err", err, "email", email)
			response["delivery"] = "failed"
			response["message"] = "Identity saved, but the verification email could not be sent. Use Resend in a moment."
		} else {
			response["delivery"] = "sent"
		}
	}
	s.auditRequest(r, sess, "identity.created", email, "Added a sending identity")
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) resendIdentity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	s.resendIdentityVerification(w, r, sess, r.PathValue("id"))
}

func (s *Server) resendIdentityVerification(w http.ResponseWriter, r *http.Request, sess *sessionRecord, id string) {
	var email, name, status string
	var sentAt *time.Time
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT email::text, display_name, status, verification_sent_at
		FROM mail_identities WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&email, &name, &status, &sentAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "IDENTITY_NOT_FOUND", "Identity not found")
		return
	}
	if status == "verified" {
		writeError(w, http.StatusConflict, "IDENTITY_VERIFIED", "That identity is already verified")
		return
	}
	if sentAt != nil && time.Since(*sentAt) < 5*time.Minute {
		writeError(w, http.StatusTooManyRequests, "VERIFY_THROTTLED", "A verification email was just sent. Wait a few minutes before retrying.")
		return
	}
	token := randomToken(32)
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE mail_identities SET verification_token_hash=$2, verification_expires_at=$3,
		       verification_sent_at=now(), updated_at=now() WHERE id=$1 AND user_id=$4
	`, id, hashToken(token), time.Now().UTC().Add(identityVerifyLifetime), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IDENTITY", "Could not create a verification link")
		return
	}
	if err := s.sendIdentityVerification(r.Context(), sess.User, email, name, token); err != nil {
		writeError(w, http.StatusBadGateway, "IDENTITY_EMAIL", "Verification email could not be delivered: "+err.Error())
		return
	}
	s.auditRequest(r, sess, "identity.verification_sent", email, "Sent an identity verification email")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email, "expiresAt": time.Now().UTC().Add(identityVerifyLifetime).Format(time.RFC3339)})
}

func (s *Server) deleteIdentity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var email string
	err := s.store.DB.QueryRow(r.Context(), `
		DELETE FROM mail_identities WHERE id=$1 AND user_id=$2 RETURNING email::text
	`, r.PathValue("id"), sess.User.ID).Scan(&email)
	if err != nil {
		writeError(w, http.StatusNotFound, "IDENTITY_NOT_FOUND", "Identity not found")
		return
	}
	s.auditRequest(r, sess, "identity.deleted", email, "Removed a sending identity")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) sendIdentityVerification(ctx context.Context, user userRecord, email, name, token string) error {
	link := strings.TrimRight(s.cfg.PublicURL, "/") + "/api/identities/verify?token=" + token
	bodyText := fmt.Sprintf(`Hello%s,

Verify that you want to send email from %s through Alt8Mail.

Open this link to confirm:
%s

The link expires in 48 hours. If you did not request this, ignore this email.

— Alt8Mail`, func() string {
		if name == "" {
			return ""
		}
		return " " + name
	}(), email, link)
	bodyHTML := "<p>Hello" + html.EscapeString(func() string {
		if name == "" {
			return ""
		}
		return " " + name
	}()) + ",</p><p>Verify that you want to send email from <strong>" + html.EscapeString(email) +
		"</strong> through Alt8Mail.</p><p><a href=\"" + html.EscapeString(link) +
		"\">Confirm this sending address</a></p><p>The link expires in 48 hours. If you did not request this, ignore this email.</p><p>— Alt8Mail</p>"
	item := map[string]any{
		"from":      map[string]any{"name": user.Name, "email": user.Email},
		"to":        []any{map[string]any{"name": name, "email": email}},
		"cc":        []any{},
		"bcc":       []any{},
		"subject":   "Verify your Alt8Mail sending address",
		"bodyText":  bodyText,
		"bodyHtml":  bodyHTML,
		"saveCopy":  false,
		"messageId": "",
	}
	_, err := s.deliverSMTP(item, nil)
	return err
}

func (s *Server) identityVerifyPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		s.identityVerifyHTML(w, http.StatusBadRequest, "Verification link missing", "This link does not include a verification token.")
		return
	}
	s.identityVerifyHTML(w, http.StatusOK, "Confirm your sending address",
		`<p>Confirm that this address belongs to you and may be used as a From identity in Alt8Mail.</p>
		 <form method="post" action="/api/identities/verify">
		   <input type="hidden" name="token" value="`+html.EscapeString(token)+`">
		   <button type="submit" class="primary">Verify sending address</button>
		 </form>`)
}

func (s *Server) identityVerifySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.identityVerifyHTML(w, http.StatusBadRequest, "Invalid request", "The verification form could not be read.")
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		s.identityVerifyHTML(w, http.StatusBadRequest, "Verification link missing", "This link does not include a verification token.")
		return
	}
	var id, email string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE mail_identities
		SET status='verified', verified_at=now(), verification_token_hash=NULL,
		    verification_expires_at=NULL, updated_at=now()
		WHERE verification_token_hash=$1 AND status='pending'
		  AND verification_expires_at > now()
		RETURNING id, email::text
	`, hashToken(token)).Scan(&id, &email)
	if err != nil {
		s.identityVerifyHTML(w, http.StatusBadRequest, "Link expired or invalid",
			"This verification link is no longer valid. Ask the account owner to resend it from Settings.")
		return
	}
	s.identityVerifyHTML(w, http.StatusOK, "Sending address verified",
		`<p><strong>`+html.EscapeString(email)+`</strong> is now verified. You can close this page and choose it in the compose window.</p>`)
}

func (s *Server) identityVerifyHTML(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s · Alt8Mail</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f4f1ea;color:#1f2a36;font:15px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.card{width:min(520px,calc(100%% - 40px));background:#fff;border:1px solid #ded8cc;border-radius:18px;padding:34px;box-shadow:0 22px 60px rgba(31,42,54,.12)}h1{font-size:23px;margin:0 0 10px}.mark{width:46px;height:46px;border-radius:13px;background:#16324f;color:#fff;display:grid;place-items:center;margin-bottom:18px;font-weight:700}p{color:#53606d;margin:0 0 16px}.primary{border:0;border-radius:10px;background:#1b6b5f;color:#fff;padding:12px 18px;font:inherit;font-weight:650;cursor:pointer}.primary:hover{background:#15564c}.note{font-size:12px;color:#7b8792;margin-top:22px}</style></head><body><main class="card"><div class="mark">M</div><h1>%s</h1>%s<p class="note">Alt8Mail · enterprise sending identity verification</p></main></body></html>`,
		html.EscapeString(title), html.EscapeString(title), body)
}

// identityForStateRecord is used by the AI/draft paths where only the alias
// label is needed.
func identityLabel(identity map[string]any) string {
	label := stateString(identity, "label")
	if label == "" {
		label = stateString(identity, "email")
	}
	return label
}

func identityJSONList(identities []map[string]any) []byte {
	raw, _ := json.Marshal(identities)
	return raw
}
