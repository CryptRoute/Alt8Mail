package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

const (
	maxUserImportRows  = 1000
	minImportQuotaByte = int64(512 * 1024 * 1024)
	maxImportQuotaByte = int64(1024 * 1024 * 1024 * 1024)
)

type userImportRow struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	QuotaGB     float64  `json:"quotaGb"`
	QuotaBytes  int64    `json:"quotaBytes"`
	NotifyEmail string   `json:"notifyEmail"`
	Aliases     []string `json:"aliases"`
}

type userImportRequest struct {
	Rows []userImportRow `json:"rows"`
}

type userImportJobRecord struct {
	ID          string
	TenantID    string
	CreatedBy   string
	CreatedByID string
	Status      string
	Total       int
	Created     int
	Failed      int
	LastError   string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

func (s *Server) adminStartUserImport(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body userImportRequest
	if err := decodeJSON(w, r, &body, 4*1024*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if len(body.Rows) == 0 {
		writeError(w, http.StatusBadRequest, "IMPORT_EMPTY", "Upload a CSV or spreadsheet with at least one user")
		return
	}
	if len(body.Rows) > maxUserImportRows {
		writeError(w, http.StatusBadRequest, "IMPORT_LIMIT", fmt.Sprintf("Import up to %d users at a time", maxUserImportRows))
		return
	}
	jobID := newID("uimp")
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not start the import")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_import_jobs (id, tenant_id, created_by, created_by_user, status, total_items, last_error)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, 'queued', $5, '')
	`, jobID, sess.ActiveTenantID, sess.User.Name, sess.User.ID, len(body.Rows)); err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not create the import job")
		return
	}
	for index, row := range body.Rows {
		name := strings.TrimSpace(row.Name)
		email := strings.ToLower(strings.TrimSpace(row.Email))
		role := strings.ToLower(strings.TrimSpace(row.Role))
		if !validUserRole(role) {
			role = "member"
		}
		if (role == "owner" || role == "admin") && !sess.can("roles.manage") {
			role = "member"
		}
		notify := strings.ToLower(strings.TrimSpace(row.NotifyEmail))
		quota := normalizeImportQuota(row)
		itemID := newID("uimpi")
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_import_items (id, job_id, tenant_id, email, name, role, quota_bytes, notify_email, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		`, itemID, jobID, sess.ActiveTenantID, email, name, role, quota, notify); err != nil {
			writeError(w, http.StatusInternalServerError, "IMPORT", fmt.Sprintf("Could not queue row %d: %v", index+1, err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not save the import job")
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.users_imported", fmt.Sprintf("%d users", len(body.Rows)), "Started a CSV user import", map[string]any{"tenantId": sess.ActiveTenantID, "jobId": jobID})
	s.startUserImportJob(jobID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobId": jobID, "total": len(body.Rows), "status": "queued"})
}

func (s *Server) adminUserImportJob(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	job, err := s.userImportJob(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "IMPORT_NOT_FOUND", "Import job not found")
		return
	}
	rows, _ := s.store.DB.Query(r.Context(), `
		SELECT id, email::text, name, role, quota_bytes, notify_email, status,
		       COALESCE(user_id::text, ''), temporary_password, mail_password, emailed, error, email_error
		FROM user_import_items WHERE job_id = $1 ORDER BY created_at, id
	`, job.ID)
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, email, name, role, notify, status, userID, tempPassword, mailPassword, itemError, emailError string
		var quota int64
		var emailed bool
		if rows.Scan(&id, &email, &name, &role, &quota, &notify, &status, &userID, &tempPassword, &mailPassword, &emailed, &itemError, &emailError) != nil {
			continue
		}
		plainTemp, _ := s.openSecuritySecret(tempPassword)
		plainMail, _ := s.openSecuritySecret(mailPassword)
		if !sess.can("users.create") {
			plainTemp, plainMail = "", ""
		}
		items = append(items, map[string]any{
			"id": id, "email": email, "name": name, "role": role, "quotaBytes": quota,
			"notifyEmail": notify, "status": status, "userId": userID,
			"temporaryPassword": plainTemp, "mailPassword": plainMail,
			"emailed": emailed, "error": itemError, "emailError": emailError,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": userImportJobPayload(job), "items": items})
}

func (s *Server) adminUserImportJobs(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text, ''), status,
		       total_items, created_items, failed_items, last_error, created_at, completed_at
		FROM user_import_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 25
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not list import jobs")
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var job userImportJobRecord
		if rows.Scan(&job.ID, &job.TenantID, &job.CreatedBy, &job.CreatedByID, &job.Status, &job.Total, &job.Created, &job.Failed, &job.LastError, &job.CreatedAt, &job.CompletedAt) == nil {
			jobs = append(jobs, userImportJobPayload(job))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) userImportJob(ctx context.Context, id, tenantID string) (userImportJobRecord, error) {
	var job userImportJobRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text, ''), status,
		       total_items, created_items, failed_items, last_error, created_at, completed_at
		FROM user_import_jobs WHERE id = $1 AND tenant_id = $2
	`, id, tenantID).Scan(&job.ID, &job.TenantID, &job.CreatedBy, &job.CreatedByID, &job.Status, &job.Total, &job.Created, &job.Failed, &job.LastError, &job.CreatedAt, &job.CompletedAt)
	return job, err
}

func userImportJobPayload(job userImportJobRecord) map[string]any {
	payload := map[string]any{
		"id": job.ID, "createdBy": job.CreatedBy, "status": job.Status,
		"total": job.Total, "created": job.Created, "failed": job.Failed,
		"lastError": job.LastError, "createdAt": formatAdminTime(job.CreatedAt),
	}
	if job.CompletedAt != nil {
		payload["completedAt"] = formatAdminTime(*job.CompletedAt)
	}
	return payload
}

func normalizeImportQuota(row userImportRow) int64 {
	quota := row.QuotaBytes
	if quota == 0 && row.QuotaGB > 0 {
		quota = int64(row.QuotaGB * 1024 * 1024 * 1024)
	}
	if quota == 0 {
		quota = 16 * 1024 * 1024 * 1024
	}
	if quota < minImportQuotaByte {
		quota = minImportQuotaByte
	}
	if quota > maxImportQuotaByte {
		quota = maxImportQuotaByte
	}
	return quota
}

func (s *Server) startUserImportJob(jobID string) {
	s.importMu.Lock()
	if s.activeImports[jobID] {
		s.importMu.Unlock()
		return
	}
	s.activeImports[jobID] = true
	s.importMu.Unlock()
	go s.runUserImportJob(jobID)
}

func (s *Server) runUserImportJob(jobID string) {
	defer func() {
		s.importMu.Lock()
		delete(s.activeImports, jobID)
		s.importMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	var job userImportJobRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text, ''), status,
		       total_items, created_items, failed_items, last_error, created_at, completed_at
		FROM user_import_jobs WHERE id = $1
	`, jobID).Scan(&job.ID, &job.TenantID, &job.CreatedBy, &job.CreatedByID, &job.Status, &job.Total, &job.Created, &job.Failed, &job.LastError, &job.CreatedAt, &job.CompletedAt)
	if err != nil {
		return
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE user_import_jobs SET status='running', updated_at=now() WHERE id=$1 AND status IN ('queued','running')`, jobID)
	type importItem struct {
		ID, Email, Name, Role, Notify string
		Quota                         int64
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, email::text, name, role, quota_bytes, notify_email
		FROM user_import_items WHERE job_id = $1 AND status = 'pending' ORDER BY created_at, id
	`, jobID)
	if err != nil {
		_, _ = s.store.DB.Exec(ctx, `UPDATE user_import_jobs SET status='failed', last_error=$2, completed_at=now(), updated_at=now() WHERE id=$1`, jobID, err.Error())
		return
	}
	items := []importItem{}
	for rows.Next() {
		var item importItem
		if rows.Scan(&item.ID, &item.Email, &item.Name, &item.Role, &item.Quota, &item.Notify) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	created, failed := 0, 0
	for _, item := range items {
		if ctx.Err() != nil {
			_, _ = s.store.DB.Exec(context.Background(), `UPDATE user_import_items SET status='failed', error=$2, updated_at=now() WHERE id=$1 AND status='pending'`, item.ID, "Import stopped: "+ctx.Err().Error())
			failed++
			continue
		}
		tempPassword, mailPassword, userID, err := s.provisionImportedUser(ctx, job, item.ID, item.Email, item.Name, item.Role, item.Quota)
		if err != nil {
			failed++
			_, _ = s.store.DB.Exec(ctx, `UPDATE user_import_items SET status='failed', error=$2, updated_at=now() WHERE id=$1`, item.ID, truncate(err.Error(), 500))
			continue
		}
		created++
		sealedTemp, _ := s.sealSecuritySecret(tempPassword)
		sealedMail, _ := s.sealSecuritySecret(mailPassword)
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE user_import_items SET status='created', user_id=NULLIF($2,'')::uuid,
			       temporary_password=$3, mail_password=$4, error='', updated_at=now()
			WHERE id=$1
		`, item.ID, userID, sealedTemp, sealedMail)
		if item.Notify != "" {
			emailErr := s.sendUserImportNotice(ctx, job.TenantID, job.CreatedByID, item.Notify, item.Name, item.Email, tempPassword)
			emailed := emailErr == nil
			emailError := ""
			if emailErr != nil {
				emailError = truncate(emailErr.Error(), 500)
			}
			_, _ = s.store.DB.Exec(ctx, `UPDATE user_import_items SET emailed=$2, email_error=$3, updated_at=now() WHERE id=$1`, item.ID, emailed, emailError)
		}
	}
	status := "completed"
	lastError := ""
	var totalCreated, totalFailed int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='created'), count(*) FILTER (WHERE status='failed')
		FROM user_import_items WHERE job_id=$1
	`, jobID).Scan(&totalCreated, &totalFailed)
	if totalCreated == 0 && totalFailed > 0 {
		status = "failed"
		lastError = "No users could be created"
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE user_import_jobs SET status=$2, created_items=$3, failed_items=$4,
		       last_error=$5, completed_at=now(), updated_at=now() WHERE id=$1
	`, jobID, status, totalCreated, totalFailed, lastError)
}

func (s *Server) provisionImportedUser(ctx context.Context, job userImportJobRecord, itemID, email, name, role string, quota int64) (string, string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", "", fmt.Errorf("Full name is required")
	}
	local, domain := splitEmail(email)
	if local == "" || domain == "" {
		return "", "", "", fmt.Errorf("A valid email address is required")
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if !validUserRole(role) {
		role = "member"
	}
	if s.adminAddressInUse(ctx, email) {
		return "", "", "", fmt.Errorf("That address is already in use")
	}
	var policy adminTenantPolicy
	var settings []byte
	if err := s.store.DB.QueryRow(ctx, `SELECT settings FROM tenants WHERE id=$1`, job.TenantID).Scan(&settings); err == nil {
		policy = tenantPolicyFromSettings(settings)
	} else {
		policy = defaultTenantPolicy()
	}
	temporaryPassword, err := generateTemporaryPassword()
	if err != nil {
		return "", "", "", fmt.Errorf("Could not generate a password")
	}
	webHash, err := auth.HashPassword(temporaryPassword)
	if err != nil {
		return "", "", "", fmt.Errorf("Could not hash the password")
	}
	mailPassword, err := generateTemporaryPassword()
	if err != nil {
		return "", "", "", fmt.Errorf("Could not generate a mailbox password")
	}
	mailHash, err := hashMailPassword(mailPassword)
	if err != nil {
		return "", "", "", fmt.Errorf("Could not create the mailbox password: %v", err)
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("Could not start user provisioning")
	}
	defer tx.Rollback(ctx)
	if err := ensureTenantDomain(ctx, tx, job.TenantID, email); err != nil {
		return "", "", "", err
	}
	userID, err := provisionUserTx(ctx, tx, job.TenantID, name, email, role, webHash, mailHash, quota, "", "", "", "", "", nil, "active", true, s.cfg.MaildirBase)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return "", "", "", fmt.Errorf("That email or alias is already in use")
		}
		return "", "", "", err
	}
	var expires any
	if policy.Security.PasswordExpiryDays > 0 {
		expires = time.Now().Add(time.Duration(policy.Security.PasswordExpiryDays) * 24 * time.Hour)
	}
	_, _ = tx.Exec(ctx, `UPDATE users SET password_expires_at=$2 WHERE id=$1`, userID, expires)
	if err := tx.Commit(ctx); err != nil {
		return "", "", "", fmt.Errorf("Could not commit user provisioning")
	}
	if stateErr := s.ensureUserState(ctx, email); stateErr != nil {
		s.logger.Warn("imported user state provisioning failed", "email", email, "err", stateErr)
	}
	return temporaryPassword, mailPassword, userID, nil
}

func (s *Server) sendUserImportNotice(ctx context.Context, tenantID, preferredUserID, to, name, mailbox, password string) error {
	fromEmail, fromName, productName, err := s.noticeSenderFor(ctx, tenantID, preferredUserID)
	if err != nil {
		return err
	}
	loginURL := strings.TrimRight(s.cfg.PublicURL, "/") + "/"
	notice := accountNotice{
		TenantID: tenantID, FromEmail: fromEmail, FromName: fromName,
		To: to, ToName: name, Subject: "Your " + productName + " mailbox is ready",
		Heading: "Your mailbox is ready",
		Intro:   "An Alt8Mail mailbox has been created for you. Sign in with the temporary password below and complete the short setup so your name, time zone and signature are right.",
		Mailbox: mailbox, Password: password, Kind: "created",
		Steps: []string{
			"Sign in at " + loginURL + " with " + mailbox + ".",
			"Choose your display name, language, time zone and theme.",
			"Set a new password and add a second factor if your organization requires one.",
			"Add a signature and start sending from your new mailbox.",
		},
		FooterNote: "This message contains a one-time credential. If you were not expecting it, contact your organization's administrator.",
	}
	return s.sendAccountNotice(ctx, notice)
}

func (s *Server) recoverUserImports(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `SELECT id FROM user_import_jobs WHERE status IN ('queued','running') ORDER BY created_at LIMIT 20`)
	if err != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.startUserImportJob(id)
	}
}

func (s *Server) userImportRecoveryWorker(ctx context.Context) {
	s.recoverUserImports(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverUserImports(ctx)
		}
	}
}
