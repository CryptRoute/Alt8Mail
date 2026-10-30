package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// adminUserSettings groups the mailbox-level controls an administrator needs
// on the dedicated user page: signature library, per-address signatures,
// vacation responder and delegated mailbox access.
func (s *Server) adminUserSignatures(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		signatures, err := s.listSignatures(r.Context(), user.userRecord())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIGNATURES", "Could not load signatures")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"signatures": signatures, "defaults": signatureDefaults(signatures), "max": maxSignatures})
	case http.MethodPost:
		var body signatureBody
		if err := decodeJSON(w, r, &body, 128*1024); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
		name := "Signature"
		if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
			name = truncate(strings.TrimSpace(*body.Name), 80)
		}
		text, htmlBody := "", ""
		if body.BodyText != nil {
			text = *body.BodyText
		}
		if body.BodyHTML != nil {
			htmlBody = *body.BodyHTML
		}
		text, htmlBody, err := normalizeSignatureBodies(text, htmlBody)
		if err != nil {
			writeFromError(w, err)
			return
		}
		id := newID("sig")
		err = s.store.DB.QueryRow(r.Context(), `
			INSERT INTO mail_signatures (id, tenant_id, user_id, name, body_text, body_html)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id
		`, id, user.TenantID, user.ID, name, text, htmlBody).Scan(&id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not create the signature")
			return
		}
		newFlag, replyFlag, forwardFlag := signatureDefaultFlags(body)
		s.applySignatureDefaults(r.Context(), user.ID, id, newFlag, replyFlag, forwardFlag)
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.signature_created", user.Email, "Created signature "+name, map[string]any{"userId": user.ID, "signatureId": id})
		signatures, _ := s.listSignatures(r.Context(), user.userRecord())
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "signatures": signatures, "defaults": signatureDefaults(signatures)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD", "Method not allowed")
	}
}

func (s *Server) adminUserSignature(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	signatureID := r.PathValue("signatureId")
	var existing signatureRecord
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id, name, body_text, body_html, default_new, default_reply, default_forward, updated_at
		FROM mail_signatures WHERE id=$1 AND user_id=$2
	`, signatureID, user.ID).Scan(&existing.ID, &existing.Name, &existing.BodyText, &existing.BodyHTML,
		&existing.DefaultNew, &existing.DefaultReply, &existing.DefaultForward, &existing.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "SIGNATURE_NOT_FOUND", "Signature not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var body signatureBody
		if err := decodeJSON(w, r, &body, 128*1024); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
		name := existing.Name
		if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
			name = truncate(strings.TrimSpace(*body.Name), 80)
		}
		text, htmlBody := existing.BodyText, existing.BodyHTML
		if body.BodyText != nil {
			text = *body.BodyText
		}
		if body.BodyHTML != nil {
			htmlBody = *body.BodyHTML
		}
		text, htmlBody, err = normalizeSignatureBodies(text, htmlBody)
		if err != nil {
			writeFromError(w, err)
			return
		}
		if _, err := s.store.DB.Exec(r.Context(), `
			UPDATE mail_signatures SET name=$2, body_text=$3, body_html=$4, updated_at=now()
			WHERE id=$1 AND user_id=$5
		`, signatureID, name, text, htmlBody, user.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not update the signature")
			return
		}
		newFlag, replyFlag, forwardFlag := signatureDefaultFlags(body)
		s.applySignatureDefaults(r.Context(), user.ID, signatureID, newFlag, replyFlag, forwardFlag)
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.signature_updated", user.Email, "Updated signature "+name, map[string]any{"userId": user.ID, "signatureId": signatureID})
		signatures, _ := s.listSignatures(r.Context(), user.userRecord())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signatures": signatures, "defaults": signatureDefaults(signatures)})
	case http.MethodDelete:
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM mail_signatures WHERE id=$1 AND user_id=$2`, signatureID, user.ID)
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.signature_removed", user.Email, "Removed a signature", map[string]any{"userId": user.ID, "signatureId": signatureID})
		signatures, _ := s.listSignatures(r.Context(), user.userRecord())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signatures": signatures, "defaults": signatureDefaults(signatures)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD", "Method not allowed")
	}
}

func signatureDefaultFlags(body signatureBody) (newFlag, replyFlag, forwardFlag bool) {
	if body.DefaultNew != nil {
		newFlag = *body.DefaultNew
	}
	if body.DefaultReply != nil {
		replyFlag = *body.DefaultReply
	}
	if body.DefaultForward != nil {
		forwardFlag = *body.DefaultForward
	}
	return
}

func (s *Server) applySignatureDefaults(ctx context.Context, userID, signatureID string, newFlag, replyFlag, forwardFlag bool) {
	if newFlag {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_new=false WHERE user_id=$1 AND id<>$2`, userID, signatureID)
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_new=true WHERE user_id=$1 AND id=$2`, userID, signatureID)
	}
	if replyFlag {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_reply=false WHERE user_id=$1 AND id<>$2`, userID, signatureID)
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_reply=true WHERE user_id=$1 AND id=$2`, userID, signatureID)
	}
	if forwardFlag {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_forward=false WHERE user_id=$1 AND id<>$2`, userID, signatureID)
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_signatures SET default_forward=true WHERE user_id=$1 AND id=$2`, userID, signatureID)
	}
}

func (s *Server) adminUserIdentitySignatures(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT a.email::text, COALESCE(sig.body_text, ''), COALESCE(sig.body_html, '')
			FROM user_aliases a
			LEFT JOIN mail_identity_signatures sig ON sig.user_id = a.user_id AND sig.email = a.email
			WHERE a.user_id = $1 AND a.active = true ORDER BY (a.kind='primary') DESC, a.email
		`, user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "SIGNATURES", "Could not load address signatures")
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var email, text, htmlBody string
			if rows.Scan(&email, &text, &htmlBody) == nil {
				items = append(items, map[string]any{"email": email, "bodyText": text, "bodyHtml": htmlBody})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"identities": items})
		return
	}
	var body struct {
		Email    string `json:"email"`
		BodyText string `json:"bodyText"`
		BodyHTML string `json:"bodyHtml"`
	}
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	var exists bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM user_aliases WHERE user_id=$1 AND lower(email::text)=lower($2))`, user.ID, email).Scan(&exists)
	if !exists {
		writeError(w, http.StatusBadRequest, "SIGNATURE_IDENTITY", "Choose an address that belongs to this user")
		return
	}
	if strings.TrimSpace(body.BodyText) == "" && strings.TrimSpace(body.BodyHTML) == "" {
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM mail_identity_signatures WHERE user_id=$1 AND email=$2`, user.ID, email)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": true})
		return
	}
	text, htmlBody, err := normalizeSignatureBodies(body.BodyText, body.BodyHTML)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO mail_identity_signatures (tenant_id, user_id, email, body_text, body_html, updated_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (user_id, email) DO UPDATE SET body_text=EXCLUDED.body_text, body_html=EXCLUDED.body_html, updated_at=now()
	`, user.TenantID, user.ID, email, text, htmlBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SIGNATURE", "Could not save the address signature")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.identity_signature_updated", user.Email, "Updated signature for "+email, map[string]any{"userId": user.ID, "email": email})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminUserVacation(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		state, loaded := s.loadStateFor(r.Context(), user.ID)
		if !loaded {
			writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
			return
		}
		vacation := stateMap(stateMap(state, "sieve"), "vacation")
		if len(vacation) == 0 {
			vacation = defaultSieve()["vacation"].(map[string]any)
		}
		writeJSON(w, http.StatusOK, map[string]any{"vacation": vacation})
		return
	}
	var body map[string]any
	if err := decodeJSON(w, r, &body, 128*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var result map[string]any
	err := s.withState(user.ID, user.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		existing := stateMap(state, "sieve")
		allowed := []string{}
		for _, folder := range stateFolderList(state) {
			if folder == nil || stateBool(folder, "virtual") {
				continue
			}
			if id := stateString(folder, "id"); id != "" {
				allowed = append(allowed, id)
			}
		}
		rules, vacation, errors := normalizeSieve(map[string]any{
			"rules":    stateSlice(existing, "rules"),
			"vacation": body,
		}, allowed)
		if len(errors) > 0 {
			return failDetails(http.StatusBadRequest, "VACATION_VALIDATION", "The vacation responder needs attention", map[string]any{"errors": errors})
		}
		forwarding := stateMap(existing, "forwarding")
		if len(forwarding) == 0 {
			forwarding = defaultForwarding()
		}
		next := map[string]any{
			"revision": stateInt(existing, "revision") + 1, "updatedAt": nowISO(),
			"rules": rules, "vacation": vacation, "forwarding": forwarding,
		}
		script := renderSieveWithFolders(next, user.Email, folderMapForSieve(state))
		if err := s.pushSieveScript(r.Context(), user.userRecord(), script); err != nil {
			return failDetails(http.StatusBadGateway, "SIEVE_SERVICE", "Dovecot rejected the vacation responder", map[string]any{"error": err.Error()})
		}
		next["script"] = script
		state["sieve"] = next
		appendStateAudit(state, "vacation.updated", "Vacation responder", sess.User.Name, "Updated by organization administrator", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		result = vacation
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.vacation_updated", user.Email, "Updated the vacation responder", map[string]any{"userId": user.ID, "enabled": stateBool(result, "enabled")})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "vacation": result})
}

func (s *Server) adminUserDelegates(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		delegates := s.delegateRows(r.Context(), user.TenantID, user.ID, true)
		delegations := s.delegateRows(r.Context(), user.TenantID, user.ID, false)
		writeJSON(w, http.StatusOK, map[string]any{"delegates": delegates, "delegations": delegations})
	case http.MethodPost:
		var body struct {
			UserID  string `json:"userId"`
			Email   string `json:"email"`
			CanRead *bool  `json:"canRead"`
			CanSend *bool  `json:"canSend"`
		}
		if err := decodeJSON(w, r, &body, 32*1024); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
		delegateID := strings.TrimSpace(body.UserID)
		if delegateID == "" && strings.TrimSpace(body.Email) != "" {
			_ = s.store.DB.QueryRow(r.Context(), `SELECT id::text FROM users WHERE tenant_id=$1 AND lower(email::text)=lower($2) AND deleted_at IS NULL`, user.TenantID, strings.TrimSpace(body.Email)).Scan(&delegateID)
		}
		if delegateID == "" || delegateID == user.ID {
			writeError(w, http.StatusBadRequest, "DELEGATE", "Choose another user in this organization")
			return
		}
		var valid bool
		_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL)`, delegateID, user.TenantID).Scan(&valid)
		if !valid {
			writeError(w, http.StatusNotFound, "DELEGATE_NOT_FOUND", "User not found")
			return
		}
		canRead, canSend := true, false
		if body.CanRead != nil {
			canRead = *body.CanRead
		}
		if body.CanSend != nil {
			canSend = *body.CanSend
		}
		_, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO mailbox_delegates (id, tenant_id, owner_user_id, delegate_user_id, can_read, can_send, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (owner_user_id, delegate_user_id) DO UPDATE SET can_read=EXCLUDED.can_read, can_send=EXCLUDED.can_send, updated_at=now()
		`, newID("dlg"), user.TenantID, user.ID, delegateID, canRead, canSend, sess.User.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DELEGATE", "Could not grant mailbox access")
			return
		}
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.delegate_added", user.Email, "Granted delegated mailbox access", map[string]any{"userId": user.ID, "delegateId": delegateID})
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "delegates": s.delegateRows(r.Context(), user.TenantID, user.ID, true)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD", "Method not allowed")
	}
}

func (s *Server) adminUserDelegate(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, ok := s.adminTargetUser(w, r, sess.ActiveTenantID)
	if !ok {
		return
	}
	delegateID := r.PathValue("delegateId")
	switch r.Method {
	case http.MethodPatch:
		var body struct {
			CanRead *bool `json:"canRead"`
			CanSend *bool `json:"canSend"`
		}
		if err := decodeJSON(w, r, &body, 16*1024); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
		canRead, canSend := true, false
		if body.CanRead != nil {
			canRead = *body.CanRead
		}
		if body.CanSend != nil {
			canSend = *body.CanSend
		}
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE mailbox_delegates SET can_read=$3, can_send=$4, updated_at=now() WHERE owner_user_id=$1 AND delegate_user_id=$2`, user.ID, delegateID, canRead, canSend)
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.delegate_updated", user.Email, "Updated delegated mailbox access", map[string]any{"userId": user.ID, "delegateId": delegateID})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegates": s.delegateRows(r.Context(), user.TenantID, user.ID, true)})
	case http.MethodDelete:
		_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM mailbox_delegates WHERE owner_user_id=$1 AND delegate_user_id=$2 AND tenant_id=$3`, user.ID, delegateID, user.TenantID)
		s.recordAdminAudit(r.Context(), sess, sess.User, "admin.delegate_removed", user.Email, "Removed delegated mailbox access", map[string]any{"userId": user.ID, "delegateId": delegateID})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delegates": s.delegateRows(r.Context(), user.TenantID, user.ID, true)})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD", "Method not allowed")
	}
}

func (s *Server) delegateRows(ctx context.Context, tenantID, userID string, owners bool) []map[string]any {
	// owners=true lists people who can open this user's mailbox; owners=false
	// lists mailboxes this user can open.
	column, joinColumn := "d.delegate_user_id", "d.owner_user_id"
	if !owners {
		column, joinColumn = "d.owner_user_id", "d.delegate_user_id"
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.name, u.email::text, u.initials, u.color, d.can_read, d.can_send
		FROM mailbox_delegates d JOIN users u ON u.id = `+column+`
		WHERE `+joinColumn+` = $1 AND d.tenant_id = $2 AND u.deleted_at IS NULL
		ORDER BY u.name, u.email
	`, userID, tenantID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, name, email, initials, color string
		var canRead, canSend bool
		if rows.Scan(&id, &name, &email, &initials, &color, &canRead, &canSend) == nil {
			result = append(result, map[string]any{
				"userId": id, "name": name, "email": email, "initials": initials,
				"color": color, "canRead": canRead, "canSend": canSend,
			})
		}
	}
	return result
}

func (s *Server) adminTargetUser(w http.ResponseWriter, r *http.Request, tenantID string) (adminUser, bool) {
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return adminUser{}, false
	}
	return user, true
}
