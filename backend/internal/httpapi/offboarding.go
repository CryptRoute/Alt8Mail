package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type offboardingRequest struct {
	TransferTo       string `json:"transferTo"`
	TransferEmail    string `json:"transferEmail"`
	RetainMailbox    *bool  `json:"retainMailbox"`
	CreateLegalHold  bool   `json:"createLegalHold"`
	LegalHoldName    string `json:"legalHoldName"`
	AutoReply        bool   `json:"autoReply"`
	AutoReplyMessage string `json:"autoReplyMessage"`
	Confirm          bool   `json:"confirm"`
}

func (s *Server) adminOffboardUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	ctx := r.Context()
	user, err := s.adminUserByID(ctx, r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	if user.ID == sess.User.ID {
		writeError(w, http.StatusBadRequest, "SELF", "You cannot offboard your own account")
		return
	}
	if user.DeletedAt != nil {
		writeError(w, http.StatusConflict, "ALREADY_CLOSED", "That account is already closed")
		return
	}
	if user.IsSystemAdmin && !sess.User.IsSystemAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only a system administrator can offboard a system administrator")
		return
	}
	if !s.adminCanManageTarget(sess, user) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Only an administrator with role management permission can offboard a privileged account")
		return
	}
	if user.Role == "owner" {
		if protected, _ := s.adminLastOwner(ctx, user); protected {
			writeError(w, http.StatusConflict, "LAST_OWNER", "This is the last owner of the organization; assign another owner first")
			return
		}
	}
	var body offboardingRequest
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "CONFIRM_REQUIRED", "Confirm the offboarding action before continuing")
		return
	}
	retainMailbox := true
	if body.RetainMailbox != nil {
		retainMailbox = *body.RetainMailbox
	}
	transferID := strings.TrimSpace(body.TransferTo)
	transferEmail := ""
	if transferID == "" && strings.TrimSpace(body.TransferEmail) != "" {
		_ = s.store.DB.QueryRow(ctx, `
			SELECT id::text, email::text FROM public.users
			WHERE tenant_id=$1 AND lower(email::text)=lower($2) AND deleted_at IS NULL
		`, sess.ActiveTenantID, strings.TrimSpace(body.TransferEmail)).Scan(&transferID, &transferEmail)
	}
	if transferID != "" {
		if transferID == user.ID {
			writeError(w, http.StatusBadRequest, "OFFBOARD_TRANSFER", "Choose a different mailbox recipient")
			return
		}
		err := s.store.DB.QueryRow(ctx, `
			SELECT email::text FROM public.users
			WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL
		`, transferID, sess.ActiveTenantID).Scan(&transferEmail)
		if err != nil {
			writeError(w, http.StatusNotFound, "OFFBOARD_TRANSFER", "The mailbox recipient was not found")
			return
		}
	}
	autoReplyMessage := truncate(strings.TrimSpace(body.AutoReplyMessage), 4000)
	if transferID != "" && !retainMailbox {
		writeError(w, http.StatusBadRequest, "OFFBOARD_TRANSFER", "Keep the mailbox active when transferring access to a coworker")
		return
	}
	if body.AutoReply && autoReplyMessage == "" {
		support := s.tenantPolicyFor(ctx, sess.ActiveTenantID).Branding.SupportEmail
		if support == "" {
			support = "your administrator"
		}
		autoReplyMessage = "This person has left the organization. Please contact " + support + " for assistance."
	}
	holdName := truncate(strings.TrimSpace(body.LegalHoldName), 120)
	if body.CreateLegalHold && holdName == "" {
		holdName = "Offboarding · " + user.Email
	}
	warnings := []string{}
	autoReplyEnabled := false
	rotatedMailPassword, err := generateTemporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not rotate the mailbox credential")
		return
	}
	rotatedMailHash, err := hashMailPassword(rotatedMailPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not rotate the mailbox credential")
		return
	}
	if body.AutoReply {
		if err := s.enableOffboardingAutoReply(ctx, user, autoReplyMessage); err != nil {
			writeError(w, http.StatusBadGateway, "AUTO_REPLY", "The automatic reply could not be configured: "+err.Error())
			return
		}
		autoReplyEnabled = true
	}

	jobID := newID("offboard")
	delegationGranted := false
	holdID := ""
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not start the offboarding workflow")
		return
	}
	defer tx.Rollback(ctx)
	if transferID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.mailbox_delegates
			  (id, tenant_id, owner_user_id, delegate_user_id, can_read, can_send, created_by)
			VALUES ($1,$2,$3,$4,true,true,$5)
			ON CONFLICT (owner_user_id, delegate_user_id)
			DO UPDATE SET can_read=true, can_send=true, updated_at=now()
		`, newID("dlg"), sess.ActiveTenantID, user.ID, transferID, sess.User.Name); err != nil {
			writeError(w, http.StatusInternalServerError, "OFFBOARD_TRANSFER", "Could not grant mailbox access to the recipient")
			return
		}
		delegationGranted = true
	}
	if body.CreateLegalHold {
		holdID = newID("hold")
		if _, err := tx.Exec(ctx, `
			INSERT INTO public.mail_legal_holds
			  (id, tenant_id, scope, user_id, name, reason, status, created_by, created_by_email)
			VALUES ($1,$2,'user',$3,$4,$5,'active',$6,$7)
		`, holdID, sess.ActiveTenantID, user.ID, holdName, "Created automatically during one-click offboarding", sess.User.ID, sess.User.Email); err != nil {
			writeError(w, http.StatusInternalServerError, "OFFBOARD_HOLD", "Could not place the legal hold")
			return
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE public.users
		SET deleted_at=now(), status='suspended', force_password_change=true,
		    mail_password_hash=$2, password_changed_at=now(), updated_at=now()
		WHERE id=$1
	`, user.ID, rotatedMailHash); err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not close the account")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE public.mailboxes SET active=$2 WHERE user_id=$1`, user.ID, retainMailbox); err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not update the mailbox state")
		return
	}
	revoked := int64(0)
	for _, statement := range []string{
		`DELETE FROM public.sessions WHERE user_id=$1`,
		`DELETE FROM public.auth_challenges WHERE user_id=$1`,
		`DELETE FROM public.admin_sessions WHERE user_id=$1`,
		`DELETE FROM public.trusted_devices WHERE user_id=$1`,
		`DELETE FROM public.admin_auth_challenges WHERE user_id=$1`,
	} {
		if tag, err := tx.Exec(ctx, statement, user.ID); err == nil {
			revoked += tag.RowsAffected()
		}
	}
	summary := map[string]any{
		"userId": user.ID, "userEmail": user.Email, "transferTo": transferID, "transferToEmail": transferEmail,
		"delegationGranted": delegationGranted, "retainMailbox": retainMailbox,
		"legalHoldId": holdID, "autoReply": autoReplyEnabled, "sessionsRevoked": revoked,
		"mailPasswordRotated": true,
	}
	encoded, _ := json.Marshal(summary)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.user_offboarding_jobs
		  (id, tenant_id, user_id, actor_id, transfer_to, retain_mailbox, create_legal_hold, auto_reply, status, summary, completed_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,'completed',$9,now())
	`, jobID, sess.ActiveTenantID, user.ID, sess.User.ID, transferID, retainMailbox, body.CreateLegalHold, autoReplyEnabled, encoded); err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not record the offboarding job")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not commit the offboarding workflow")
		return
	}
	if !retainMailbox && !body.CreateLegalHold {
		warnings = append(warnings, "The mailbox was deactivated. Message data remains available to administrators and eDiscovery until retention removes it.")
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.user_offboarded", user.Email, "Completed one-click offboarding",
		map[string]any{
			"userId": user.ID, "jobId": jobID, "transferTo": transferID, "retainMailbox": retainMailbox,
			"legalHoldId": holdID, "autoReply": autoReplyEnabled, "sessionsRevoked": revoked,
		})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "warnings": warnings,
		"job": map[string]any{
			"id": jobID, "userId": user.ID, "userEmail": user.Email, "status": "completed",
			"transferTo": transferID, "transferToEmail": transferEmail, "delegationGranted": delegationGranted,
			"retainMailbox": retainMailbox, "legalHoldId": holdID, "autoReply": autoReplyEnabled,
			"sessionsRevoked": revoked, "summary": summary,
			"createdAt": time.Now().UTC().Format(time.RFC3339), "completedAt": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

func (s *Server) enableOffboardingAutoReply(ctx context.Context, user adminUser, message string) error {
	return s.withState(user.ID, user.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		existing := stateMap(state, "sieve")
		if len(existing) == 0 {
			existing = defaultSieve()
		}
		vacation := map[string]any{
			"enabled": true, "start": "", "end": "", "days": 30,
			"internal": map[string]any{"subject": "Out of office", "message": message},
			"external": map[string]any{"enabled": false, "subject": "Away from the office", "message": message},
		}
		forwarding := stateMap(existing, "forwarding")
		if len(forwarding) == 0 {
			forwarding = defaultForwarding()
		}
		next := map[string]any{
			"revision": stateInt(existing, "revision") + 1, "updatedAt": nowISO(),
			"rules": stateSlice(existing, "rules"), "vacation": vacation, "forwarding": forwarding,
		}
		script := renderSieveWithFolders(next, user.Email, folderMapForSieve(state))
		if err := s.pushSieveScript(ctx, user.userRecord(), script); err != nil {
			return fmt.Errorf("%s", sanitizeMailText(err.Error()))
		}
		next["script"] = script
		state["sieve"] = next
		appendStateAudit(state, "offboarding.auto_reply", "Automatic reply", "Organization administrator",
			"Enabled an automatic reply during offboarding", "127.0.0.1", "a8m-admin")
		return nil
	})
}

func (s *Server) adminOffboardingJobs(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT j.id, COALESCE(j.user_id::text, ''), COALESCE(u.email::text, ''), u.name,
		       COALESCE(j.transfer_to::text, ''), COALESCE(t.email::text, ''),
		       j.retain_mailbox, j.create_legal_hold, j.auto_reply, j.status,
		       COALESCE(a.email::text, ''), j.summary, j.created_at, j.completed_at
		FROM public.user_offboarding_jobs j
		LEFT JOIN public.users u ON u.id=j.user_id
		LEFT JOIN public.users t ON t.id=j.transfer_to
		LEFT JOIN public.users a ON a.id=j.actor_id
		WHERE j.tenant_id=$1
		ORDER BY j.created_at DESC
		LIMIT 100
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFBOARD", "Could not load offboarding jobs")
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var id, userID, userEmail, userName, transferTo, transferEmail, status, actorEmail string
		var retainMailbox, createLegalHold, autoReply bool
		var summary []byte
		var createdAt time.Time
		var completedAt *time.Time
		if rows.Scan(&id, &userID, &userEmail, &userName, &transferTo, &transferEmail,
			&retainMailbox, &createLegalHold, &autoReply, &status, &actorEmail, &summary, &createdAt, &completedAt) != nil {
			continue
		}
		decoded := map[string]any{}
		_ = json.Unmarshal(summary, &decoded)
		var completed any
		if completedAt != nil {
			completed = completedAt.UTC().Format(time.RFC3339)
		}
		jobs = append(jobs, map[string]any{
			"id": id, "userId": userID, "userEmail": userEmail, "userName": userName,
			"transferTo": transferTo, "transferToEmail": transferEmail,
			"retainMailbox": retainMailbox, "createLegalHold": createLegalHold, "autoReply": autoReply,
			"status": status, "actorEmail": actorEmail, "summary": decoded,
			"createdAt": createdAt.UTC().Format(time.RFC3339), "completedAt": completed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) adminOffboardPreview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	user, err := s.adminUserByID(r.Context(), r.PathValue("id"))
	if err != nil || user.TenantID != sess.ActiveTenantID {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "User not found")
		return
	}
	held, _ := s.userOnLegalHold(r.Context(), user.TenantID, user.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id": user.ID, "email": user.Email, "name": user.Name, "role": user.Role,
			"status": user.Status, "deletedAt": user.DeletedAt, "usedBytes": user.UsedBytes, "quotaBytes": user.QuotaBytes,
		},
		"legalHold": held,
	})
}
