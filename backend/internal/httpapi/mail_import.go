package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const (
	maxMailImportItems  = 500
	mailImportBatchSize = 40
	mailImportDefaultMB = 50
	mailImportHardMaxMB = 64
)

var errMailImportCancelled = fmt.Errorf("mail import cancelled")
var errMailImportMessageTooLarge = fmt.Errorf("message exceeds the import size limit")

type mailImportItemInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	NotifyEmail string `json:"notifyEmail"`
}

type mailImportRequestOptions struct {
	IncludeJunk       *bool `json:"includeJunk"`
	IncludeTrash      *bool `json:"includeTrash"`
	IncludeDrafts     *bool `json:"includeDrafts"`
	MaxMessageMB      *int  `json:"maxMessageMB"`
	DeleteAfterImport *bool `json:"deleteAfterImport"`
}

type mailImportRequestBody struct {
	Protocol string                    `json:"protocol"`
	Host     string                    `json:"host"`
	Port     int                       `json:"port"`
	Security string                    `json:"security"`
	Items    []mailImportItemInput     `json:"items"`
	Options  *mailImportRequestOptions `json:"options"`

	// Older admin bundles sent these three flags at the top level. Keep
	// accepting them so a cached browser cannot break a migration request.
	IncludeJunk   *bool `json:"includeJunk"`
	IncludeTrash  *bool `json:"includeTrash"`
	IncludeDrafts *bool `json:"includeDrafts"`
	MaxMessageMB  *int  `json:"maxMessageMB"`
}

type mailImportJobRecord struct {
	ID          string
	TenantID    string
	CreatedBy   string
	CreatedByID string
	Protocol    string
	Host        string
	Port        int
	Security    string
	Status      string
	Options     map[string]any
	Total       int
	Validated   int
	Completed   int
	Failed      int
	Messages    int64
	Current     string
	LastError   string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

func pickImportBool(primary, legacy *bool, fallback bool) bool {
	if primary != nil {
		return *primary
	}
	if legacy != nil {
		return *legacy
	}
	return fallback
}

// normalizedOptions is the single source of truth for migration options. It
// accepts the current nested options object and the flat fields used by older
// admin bundles.
func (b mailImportRequestBody) normalizedOptions() map[string]any {
	includeJunk := pickImportBool(nil, b.IncludeJunk, false)
	includeTrash := pickImportBool(nil, b.IncludeTrash, false)
	includeDrafts := pickImportBool(nil, b.IncludeDrafts, true)
	maxMB := mailImportDefaultMB
	if b.MaxMessageMB != nil {
		maxMB = *b.MaxMessageMB
	}
	if b.Options != nil {
		includeJunk = pickImportBool(b.Options.IncludeJunk, b.IncludeJunk, false)
		includeTrash = pickImportBool(b.Options.IncludeTrash, b.IncludeTrash, false)
		includeDrafts = pickImportBool(b.Options.IncludeDrafts, b.IncludeDrafts, true)
		if b.Options.MaxMessageMB != nil {
			maxMB = *b.Options.MaxMessageMB
		}
	}
	if maxMB <= 0 {
		maxMB = mailImportDefaultMB
	}
	if maxMB > mailImportHardMaxMB {
		maxMB = mailImportHardMaxMB
	}
	return map[string]any{
		"includeJunk": includeJunk, "includeTrash": includeTrash,
		"includeDrafts": includeDrafts, "maxMessageMB": maxMB,
	}
}

func mailImportMaxBytes(options map[string]any) int64 {
	maxMB := stateInt(options, "maxMessageMB")
	if maxMB <= 0 {
		maxMB = mailImportDefaultMB
	}
	if maxMB > mailImportHardMaxMB {
		maxMB = mailImportHardMaxMB
	}
	if maxMB < 1 {
		maxMB = 1
	}
	return maxMB << 20
}

func normalizeMailImportEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if email == "" {
		return "", fmt.Errorf("email is required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return "", fmt.Errorf("invalid email address")
	}
	return email, nil
}

func normalizeMailImportItems(input []mailImportItemInput) ([]mailImportItemInput, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("add at least one mailbox")
	}
	seen := map[string]bool{}
	items := make([]mailImportItemInput, 0, len(input))
	for index, item := range input {
		email, err := normalizeMailImportEmail(item.Email)
		if err != nil {
			return nil, fmt.Errorf("row %d: %s", index+1, err)
		}
		if seen[email] {
			return nil, fmt.Errorf("row %d duplicates mailbox %s", index+1, email)
		}
		if strings.TrimSpace(item.Password) == "" {
			return nil, fmt.Errorf("row %d needs a mailbox password", index+1)
		}
		notify := ""
		if strings.TrimSpace(item.NotifyEmail) != "" {
			notify, err = normalizeMailImportEmail(item.NotifyEmail)
			if err != nil {
				return nil, fmt.Errorf("row %d has an invalid notification address", index+1)
			}
		}
		seen[email] = true
		items = append(items, mailImportItemInput{
			Email: email, Password: item.Password, NotifyEmail: notify,
		})
	}
	return items, nil
}

func validMailImportHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func (s *Server) adminMailImportCreate(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body mailImportRequestBody
	if err := decodeJSON(w, r, &body, 8*1024*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(body.Protocol))
	if protocol != "imap" && protocol != "pop3" {
		writeError(w, http.StatusBadRequest, "PROTOCOL", "Choose IMAP or POP3")
		return
	}
	host := strings.TrimSuffix(strings.TrimSpace(body.Host), ".")
	if !validMailImportHost(host) {
		writeError(w, http.StatusBadRequest, "HOST", "Enter a valid old mail server hostname or IP address")
		return
	}
	security := strings.ToLower(strings.TrimSpace(body.Security))
	if security != "ssl" && security != "starttls" && security != "none" {
		security = "ssl"
	}
	if len(body.Items) > maxMailImportItems {
		writeError(w, http.StatusBadRequest, "IMPORT_LIMIT", fmt.Sprintf("Import up to %d mailboxes at a time", maxMailImportItems))
		return
	}
	items, err := normalizeMailImportItems(body.Items)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMPORT_ROW", err.Error())
		return
	}
	options := body.normalizedOptions()
	port := body.Port
	if port <= 0 {
		switch protocol {
		case "imap":
			if security == "ssl" {
				port = 993
			} else {
				port = 143
			}
		case "pop3":
			if security == "ssl" {
				port = 995
			} else {
				port = 110
			}
		}
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, "PORT", "The port must be between 1 and 65535")
		return
	}
	if security != "ssl" && ((protocol == "imap" && port == 993) || (protocol == "pop3" && port == 995)) {
		writeError(w, http.StatusBadRequest, "PORT_SECURITY", "Port 993/995 normally requires SSL/TLS. Select SSL/TLS or use port 143 (IMAP) or 110 (POP3).")
		return
	}
	optionsJSON, _ := json.Marshal(options)
	jobID := newID("mimp")
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not start the migration")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO mail_import_jobs (id, tenant_id, created_by, created_by_user, protocol, host, port, security, status, options, total_items)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,'validating',$9::jsonb,$10)
	`, jobID, sess.ActiveTenantID, sess.User.Name, sess.User.ID, protocol, host, port, security, string(optionsJSON), len(items)); err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not create the migration job")
		return
	}
	for index, item := range items {
		sealed, err := s.sealSecuritySecret(item.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "IMPORT", "Could not protect the imported credentials")
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO mail_import_items (id, job_id, tenant_id, source_email, source_password, notify_email, status)
			VALUES ($1,$2,$3,$4,$5,$6,'pending')
			ON CONFLICT (job_id, source_email) DO NOTHING
		`, newID("mimpi"), jobID, sess.ActiveTenantID, item.Email, sealed, item.NotifyEmail); err != nil {
			writeError(w, http.StatusInternalServerError, "IMPORT", fmt.Sprintf("Could not queue row %d: %v", index+1, err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not save the migration")
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.mail_import_started", host, "Started a mailbox migration", map[string]any{"tenantId": sess.ActiveTenantID, "jobId": jobID, "mailboxes": len(items)})
	s.startMailImportJob(jobID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobId": jobID, "status": "validating", "total": len(items)})
}

func (s *Server) adminMailImportJobs(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text,''), protocol, host, port, security, status,
		       options, total_items, validated_items, completed_items, failed_items, imported_messages, current_item, last_error, created_at, completed_at
		FROM mail_import_jobs WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 40
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not list migrations")
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		job, ok := scanMailImportJob(rows)
		if ok {
			jobs = append(jobs, mailImportJobPayload(job, false))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) adminMailImportJob(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	job, err := s.mailImportJob(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "IMPORT_NOT_FOUND", "Migration not found")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, source_email::text, notify_email::text, target_mode, COALESCE(target_user_id::text,''), target_email::text,
		       target_name, target_role, target_quota_bytes, status, auth_error, imported_messages, total_messages,
		       initial_password, mail_password, notified_at IS NOT NULL, error
		FROM mail_import_items WHERE job_id=$1 ORDER BY created_at, id
	`, job.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "IMPORT", "Could not load migration details")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, source, notify, mode, targetUserID, targetEmail, targetName, role, status, authError, initialPassword, mailPassword, itemError string
		var quota, imported, total int64
		var notified bool
		if rows.Scan(&id, &source, &notify, &mode, &targetUserID, &targetEmail, &targetName, &role, &quota,
			&status, &authError, &imported, &total, &initialPassword, &mailPassword, &notified, &itemError) != nil {
			continue
		}
		plainPassword := ""
		if (notify == "" || !notified) && (status == "completed" || status == "failed") {
			plainPassword, _ = s.openSecuritySecret(initialPassword)
		}
		plainMailPassword := ""
		if status == "completed" || status == "failed" {
			plainMailPassword, _ = s.openSecuritySecret(mailPassword)
		}
		if !sess.can("users.create") {
			plainPassword, plainMailPassword = "", ""
		}
		items = append(items, map[string]any{
			"id": id, "sourceEmail": source, "notifyEmail": notify, "targetMode": mode,
			"targetUserId": targetUserID, "targetEmail": targetEmail, "targetName": targetName,
			"targetRole": role, "targetQuotaBytes": quota, "status": status,
			"authError": authError, "importedMessages": imported, "totalMessages": total,
			"initialPassword": plainPassword, "mailPassword": plainMailPassword, "notified": notified, "error": itemError,
		})
	}
	payload := mailImportJobPayload(job, true)
	payload["items"] = items
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) adminMailImportMapping(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	job, err := s.mailImportJob(r.Context(), r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "IMPORT_NOT_FOUND", "Migration not found")
		return
	}
	if job.Status != "awaiting_mapping" {
		writeError(w, http.StatusConflict, "IMPORT_STATE", "This migration is not waiting for mailbox mapping yet")
		return
	}
	var body struct {
		Items []struct {
			ID          string  `json:"id"`
			TargetMode  string  `json:"targetMode"`
			TargetUser  string  `json:"targetUserId"`
			TargetName  string  `json:"targetName"`
			TargetRole  string  `json:"targetRole"`
			TargetQuota float64 `json:"targetQuotaGb"`
			NotifyEmail string  `json:"notifyEmail"`
		} `json:"items"`
		Options map[string]any `json:"options"`
	}
	if err := decodeJSON(w, r, &body, 4*1024*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if len(body.Items) == 0 {
		writeError(w, http.StatusBadRequest, "IMPORT_MAPPING", "Map at least one validated mailbox")
		return
	}
	ctx := r.Context()
	for _, item := range body.Items {
		mode := strings.ToLower(strings.TrimSpace(item.TargetMode))
		if mode != "existing" && mode != "new" {
			mode = "new"
		}
		var targetUserID, targetEmail, targetName string
		if mode == "existing" {
			if err := s.store.DB.QueryRow(ctx, `
				SELECT id::text, email::text, name FROM users
				WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL AND status='active'
			`, item.TargetUser, job.TenantID).Scan(&targetUserID, &targetEmail, &targetName); err != nil {
				writeError(w, http.StatusBadRequest, "IMPORT_TARGET", "Choose an active user for every existing-mailbox mapping")
				return
			}
		} else {
			var sourceEmail string
			_ = s.store.DB.QueryRow(ctx, `SELECT source_email::text FROM mail_import_items WHERE id=$1 AND job_id=$2`, item.ID, job.ID).Scan(&sourceEmail)
			if sourceEmail == "" {
				writeError(w, http.StatusBadRequest, "IMPORT_TARGET", "A mapping row no longer exists")
				return
			}
			targetEmail = sourceEmail
			targetName = strings.TrimSpace(item.TargetName)
			if targetName == "" {
				local, _ := splitEmail(sourceEmail)
				targetName = strings.ReplaceAll(local, ".", " ")
			}
		}
		role := strings.ToLower(strings.TrimSpace(item.TargetRole))
		if !validUserRole(role) {
			role = "member"
		}
		if (role == "owner" || role == "admin") && !sess.can("roles.manage") {
			role = "member"
		}
		quota := int64(item.TargetQuota * 1024 * 1024 * 1024)
		if quota == 0 {
			quota = 16 * 1024 * 1024 * 1024
		}
		if quota < minImportQuotaByte {
			quota = minImportQuotaByte
		}
		if quota > maxImportQuotaByte {
			quota = maxImportQuotaByte
		}
		notify := strings.ToLower(strings.TrimSpace(item.NotifyEmail))
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE mail_import_items SET target_mode=$3, target_user_id=NULLIF($4,'')::uuid, target_email=$5,
			       target_name=$6, target_role=$7, target_quota_bytes=$8, notify_email=$9,
			       status=CASE WHEN status='auth_failed' THEN 'skipped' ELSE 'queued' END, updated_at=now()
			WHERE id=$1 AND job_id=$2
		`, item.ID, job.ID, mode, targetUserID, targetEmail, targetName, role, quota, notify)
	}
	if len(body.Options) > 0 {
		merged := job.Options
		for key, value := range body.Options {
			switch key {
			case "includeJunk", "includeTrash", "includeDrafts", "maxMessageMB":
				merged[key] = value
			}
		}
		encoded, _ := json.Marshal(merged)
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET options=$2::jsonb, updated_at=now() WHERE id=$1`, job.ID, string(encoded))
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET status='queued', last_error='', updated_at=now() WHERE id=$1`, job.ID)
	s.recordAdminAudit(ctx, sess, sess.User, "admin.mail_import_mapped", job.Host, "Confirmed mailbox mapping", map[string]any{"tenantId": job.TenantID, "jobId": job.ID})
	s.startMailImportJob(job.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "status": "queued"})
}

func (s *Server) adminMailImportCancel(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE mail_import_jobs SET status='cancelled', completed_at=now(), updated_at=now()
		WHERE id=$1 AND tenant_id=$2 AND status IN ('validating','awaiting_mapping','queued','running')
	`, r.PathValue("id"), sess.ActiveTenantID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "IMPORT_STATE", "This migration cannot be cancelled")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.mail_import_cancelled", r.PathValue("id"), "Cancelled a mailbox migration", map[string]any{"tenantId": sess.ActiveTenantID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "cancelled"})
}

func scanMailImportJob(rows interface{ Scan(...any) error }) (mailImportJobRecord, bool) {
	var job mailImportJobRecord
	var options []byte
	var completed *time.Time
	if err := rows.Scan(&job.ID, &job.TenantID, &job.CreatedBy, &job.CreatedByID, &job.Protocol, &job.Host, &job.Port,
		&job.Security, &job.Status, &options, &job.Total, &job.Validated, &job.Completed, &job.Failed,
		&job.Messages, &job.Current, &job.LastError, &job.CreatedAt, &completed); err != nil {
		return job, false
	}
	job.Options = map[string]any{}
	_ = json.Unmarshal(options, &job.Options)
	job.CompletedAt = completed
	return job, true
}

func (s *Server) mailImportJob(ctx context.Context, id, tenantID string) (mailImportJobRecord, error) {
	job, ok := scanMailImportJob(s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text,''), protocol, host, port, security, status,
		       options, total_items, validated_items, completed_items, failed_items, imported_messages, current_item, last_error, created_at, completed_at
		FROM mail_import_jobs WHERE id=$1 AND tenant_id=$2
	`, id, tenantID))
	if !ok {
		return job, fmt.Errorf("not found")
	}
	return job, nil
}

func mailImportJobPayload(job mailImportJobRecord, detailed bool) map[string]any {
	payload := map[string]any{
		"id": job.ID, "createdBy": job.CreatedBy, "protocol": job.Protocol, "host": job.Host,
		"port": job.Port, "security": job.Security, "status": job.Status, "options": job.Options,
		"total": job.Total, "validated": job.Validated, "completed": job.Completed, "failed": job.Failed,
		"importedMessages": job.Messages, "currentItem": job.Current, "lastError": job.LastError,
		"createdAt": formatAdminTime(job.CreatedAt),
	}
	if job.CompletedAt != nil {
		payload["completedAt"] = formatAdminTime(*job.CompletedAt)
	}
	if detailed {
		payload["server"] = fmt.Sprintf("%s://%s:%d", job.Protocol, job.Host, job.Port)
	}
	return payload
}

func (s *Server) startMailImportJob(jobID string) {
	s.importMu.Lock()
	key := "mail:" + jobID
	if s.activeImports[key] {
		s.importMu.Unlock()
		return
	}
	s.activeImports[key] = true
	s.importMu.Unlock()
	go func() {
		defer func() {
			s.importMu.Lock()
			delete(s.activeImports, key)
			s.importMu.Unlock()
		}()
		s.runMailImportJob(jobID)
	}()
}

func (s *Server) runMailImportJob(jobID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	var job mailImportJobRecord
	job, ok := scanMailImportJob(s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, created_by, COALESCE(created_by_user::text,''), protocol, host, port, security, status,
		       options, total_items, validated_items, completed_items, failed_items, imported_messages, current_item, last_error, created_at, completed_at
		FROM mail_import_jobs WHERE id=$1
	`, jobID))
	if !ok {
		return
	}
	switch job.Status {
	case "validating":
		s.validateMailImportJob(ctx, job)
	case "queued", "running":
		s.importMailImportJob(ctx, job)
	}
}

func (s *Server) validateMailImportJob(ctx context.Context, job mailImportJobRecord) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, source_email::text, source_password FROM mail_import_items
		WHERE job_id=$1 AND status='pending' ORDER BY created_at, id
	`, job.ID)
	if err != nil {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET status='failed', last_error=$2, completed_at=now(), updated_at=now() WHERE id=$1`, job.ID, err.Error())
		return
	}
	type validationRow struct{ id, email, password string }
	items := []validationRow{}
	for rows.Next() {
		var item validationRow
		if rows.Scan(&item.id, &item.email, &item.password) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return
		}
		password, err := s.openSecuritySecret(item.password)
		if err != nil {
			_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET status='auth_failed', auth_error=$2, updated_at=now() WHERE id=$1`, item.id, "Stored credential could not be read")
			continue
		}
		_ = s.store.DB.QueryRow(ctx, `UPDATE mail_import_items SET status='validating', updated_at=now() WHERE id=$1`, item.id)
		err = s.validateSourceLogin(job, item.email, password)
		if err != nil {
			_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET status='auth_failed', auth_error=$2, updated_at=now() WHERE id=$1`, item.id, truncate(err.Error(), 500))
		} else {
			_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET status='ready', auth_error='', updated_at=now() WHERE id=$1`, item.id)
		}
	}
	var validated, failed int
	_ = s.store.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='ready'), count(*) FILTER (WHERE status='auth_failed') FROM mail_import_items WHERE job_id=$1`, job.ID).Scan(&validated, &failed)
	if s.mailImportStatus(ctx, job.ID) == "cancelled" {
		return
	}
	status := "awaiting_mapping"
	if validated == 0 {
		status = "failed"
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE mail_import_jobs SET status=$2, validated_items=$3, failed_items=$4,
		       last_error=CASE WHEN $2='failed' THEN 'No mailbox credentials could be validated' ELSE '' END,
		       updated_at=now(), completed_at=CASE WHEN $2='failed' THEN now() ELSE NULL END
		WHERE id=$1
	`, job.ID, status, validated, failed)
}

func (s *Server) validateSourceLogin(job mailImportJobRecord, email, password string) error {
	if job.Protocol == "pop3" {
		return validatePOP3Login(job.Host, job.Port, job.Security, email, password)
	}
	conn, err := dialExternalIMAP(job.Host, job.Port, job.Security)
	if err != nil {
		return err
	}
	defer conn.Logout()
	conn.Timeout = 20 * time.Second
	return conn.Login(email, password)
}

func (s *Server) importMailImportJob(ctx context.Context, job mailImportJobRecord) {
	_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET status='running', started_at=COALESCE(started_at,now()), updated_at=now() WHERE id=$1`, job.ID)
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, source_email::text, source_password, notify_email::text, target_mode,
		       COALESCE(target_user_id::text,''), target_email::text, target_name, target_role, target_quota_bytes, cursor
		FROM mail_import_items WHERE job_id=$1 AND status IN ('queued','running','ready')
		ORDER BY created_at, id
	`, job.ID)
	if err != nil {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET status='failed', last_error=$2, completed_at=now(), updated_at=now() WHERE id=$1`, job.ID, err.Error())
		return
	}
	type importItem struct {
		ID, SourceEmail, Password, NotifyEmail, Mode, TargetUserID, TargetEmail, TargetName, Role, Cursor string
		Quota                                                                                             int64
	}
	items := []importItem{}
	for rows.Next() {
		var item importItem
		if rows.Scan(&item.ID, &item.SourceEmail, &item.Password, &item.NotifyEmail, &item.Mode, &item.TargetUserID,
			&item.TargetEmail, &item.TargetName, &item.Role, &item.Quota, &item.Cursor) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return
		}
		s.runMailImportItem(ctx, job, item.ID, item.SourceEmail, item.Password, item.NotifyEmail, item.Mode,
			item.TargetUserID, item.TargetEmail, item.TargetName, item.Role, item.Quota, item.Cursor)
	}
	var completed, failed int
	var messages int64
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='completed'), count(*) FILTER (WHERE status='failed'),
		       COALESCE(sum(imported_messages),0) FROM mail_import_items WHERE job_id=$1
	`, job.ID).Scan(&completed, &failed, &messages)
	if s.mailImportStatus(ctx, job.ID) == "cancelled" {
		return
	}
	status := "completed"
	if completed == 0 {
		status = "failed"
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE mail_import_jobs SET status=$2, completed_items=$3, failed_items=$4, imported_messages=$5,
		       current_item='', last_error=CASE WHEN $2='failed' THEN 'No mailbox could be imported' ELSE '' END,
		       completed_at=now(), updated_at=now() WHERE id=$1
	`, job.ID, status, completed, failed, messages)
}

func (s *Server) mailImportStatus(ctx context.Context, jobID string) string {
	var status string
	_ = s.store.DB.QueryRow(ctx, `SELECT status FROM mail_import_jobs WHERE id=$1`, jobID).Scan(&status)
	return status
}

func (s *Server) runMailImportItem(ctx context.Context, job mailImportJobRecord, itemID, sourceEmail, sealedPassword, notify, mode, targetUserID, targetEmail, targetName, role string, quota int64, cursorJSON string) {
	update := func(status, errText string) {
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET status=$2, error=$3, updated_at=now(), completed_at=CASE WHEN $2 IN ('completed','failed','skipped') THEN now() ELSE completed_at END WHERE id=$1`, itemID, status, truncate(errText, 500))
	}
	_ = s.store.DB.QueryRow(ctx, `UPDATE mail_import_items SET status='running', started_at=COALESCE(started_at,now()), updated_at=now() WHERE id=$1`, itemID)
	_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_jobs SET current_item=$2, updated_at=now() WHERE id=$1`, job.ID, sourceEmail)

	password, err := s.openSecuritySecret(sealedPassword)
	if err != nil {
		update("failed", "The stored source password could not be read")
		return
	}
	// Create the destination account before importing so a crash leaves a
	// visible, resumable user rather than a half-mapped job.
	if mode == "new" && targetUserID == "" {
		createdID, tempPassword, mailPassword, err := s.provisionImportedUser(ctx, userImportJobRecord{TenantID: job.TenantID}, itemID, targetEmail, targetName, role, quota)
		if err != nil {
			update("failed", "Could not create the destination user: "+err.Error())
			return
		}
		targetUserID = createdID
		sealedTemp, _ := s.sealSecuritySecret(tempPassword)
		sealedMail, _ := s.sealSecuritySecret(mailPassword)
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE mail_import_items SET target_user_id=$2::uuid, target_email=$3, initial_password=$4, mail_password=$5, updated_at=now()
			WHERE id=$1
		`, itemID, targetUserID, targetEmail, sealedTemp, sealedMail)
	}
	if targetEmail == "" && targetUserID != "" {
		_ = s.store.DB.QueryRow(ctx, `SELECT email::text FROM users WHERE id=$1`, targetUserID).Scan(&targetEmail)
	}

	if job.Protocol == "pop3" {
		imported, total, err := s.importPOP3Mailbox(ctx, job, sourceEmail, password, targetEmail, cursorJSON, itemID)
		if err != nil {
			if err == errMailImportCancelled {
				update("skipped", "Cancelled")
				return
			}
			update("failed", err.Error())
			return
		}
		_ = imported
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET imported_messages=$2, total_messages=$3, updated_at=now() WHERE id=$1`, itemID, imported, total)
	} else {
		imported, total, err := s.importIMAPMailbox(ctx, job, sourceEmail, password, targetEmail, cursorJSON, itemID)
		if err != nil {
			if err == errMailImportCancelled {
				update("skipped", "Cancelled")
				return
			}
			update("failed", err.Error())
			return
		}
		_ = imported
		_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET imported_messages=$2, total_messages=$3, updated_at=now() WHERE id=$1`, itemID, imported, total)
	}
	var notified bool
	var emailError string
	if notify != "" {
		var initialPassword string
		_ = s.store.DB.QueryRow(ctx, `SELECT initial_password FROM mail_import_items WHERE id=$1`, itemID).Scan(&initialPassword)
		plain, _ := s.openSecuritySecret(initialPassword)
		noticeErr := s.sendMigrationNotice(ctx, job.TenantID, job.CreatedByID, notify, targetName, targetEmail, plain)
		notified = noticeErr == nil
		if noticeErr != nil {
			emailError = noticeErr.Error()
		}
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE mail_import_items SET notified_at=CASE WHEN $2 THEN now() ELSE NULL END, updated_at=now() WHERE id=$1`, itemID, notified)
	update("completed", emailError)
}

func (s *Server) sendMigrationNotice(ctx context.Context, tenantID, preferredUserID, to, name, mailbox, password string) error {
	fromEmail, fromName, productName, err := s.noticeSenderFor(ctx, tenantID, preferredUserID)
	if err != nil {
		return err
	}
	loginURL := strings.TrimRight(s.cfg.PublicURL, "/") + "/"
	return s.sendAccountNotice(ctx, accountNotice{
		TenantID: tenantID, FromEmail: fromEmail, FromName: fromName,
		To: to, ToName: name, Subject: "Your mailbox has moved to " + productName,
		Heading: "Your mailbox is ready — with your old mail",
		Intro:   "Your organization has moved your mailbox to Alt8Mail. Your existing messages have been copied across, so you can sign in and pick up where you left off.",
		Mailbox: mailbox, Password: password, Kind: "migrated",
		Steps: []string{
			"Sign in at " + loginURL + " with your mailbox address.",
			"Set a new password and complete the short setup.",
			"Check your folders — your inbox and sent mail are already there.",
		},
		FooterNote: "Your previous provider was not changed by this import. Keep using Alt8Mail from now on.",
	})
}

func (s *Server) importIMAPMailbox(ctx context.Context, job mailImportJobRecord, sourceEmail, password, targetEmail, cursorJSON, itemID string) (int64, int64, error) {
	source, err := dialExternalIMAP(job.Host, job.Port, job.Security)
	if err != nil {
		return 0, 0, err
	}
	defer source.Logout()
	source.Timeout = 60 * time.Second
	if err := source.Login(sourceEmail, password); err != nil {
		return 0, 0, fmt.Errorf("source login failed: %w", err)
	}
	target, err := s.dialLocalTarget(targetEmail)
	if err != nil {
		return 0, 0, err
	}
	defer target.Logout()
	folders, err := listIMAPMailboxes(source)
	if err != nil {
		return 0, 0, err
	}
	cursor := map[string]int64{}
	cursorInner := map[string]any{}
	_ = json.Unmarshal([]byte(cursorJSON), &cursorInner)
	if inner, ok := cursorInner["folders"].(map[string]any); ok {
		for name, value := range inner {
			if number, ok := value.(float64); ok {
				cursor[name] = int64(number)
			}
		}
	}
	maxBytes := mailImportMaxBytes(job.Options)
	var imported, total int64
	for _, sourceFolder := range folders {
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return imported, total, errMailImportCancelled
		}
		targetFolder, include := importFolderTarget(sourceFolder, job.Options)
		if !include {
			continue
		}
		if _, err := source.Select(sourceFolder, true); err != nil {
			continue
		}
		total += int64(source.Mailbox().Messages)
		criteria := imap.NewSearchCriteria()
		uids, err := source.UidSearch(criteria)
		if err != nil || len(uids) == 0 {
			continue
		}
		lastUID := cursor[sourceFolder]
		existing := map[string]bool{}
		if targetIDs, err := targetFolderMessageIDs(target, targetFolder); err == nil {
			for id := range targetIDs {
				existing[id] = true
			}
		}
		if _, err := target.Select(targetFolder, false); err != nil {
			_ = target.Create(targetFolder)
			if _, err := target.Select(targetFolder, false); err != nil {
				continue
			}
		}
		section := &imap.BodySectionName{Peek: true}
		for start := 0; start < len(uids); start += mailImportBatchSize {
			if s.mailImportStatus(ctx, job.ID) == "cancelled" {
				return imported, total, errMailImportCancelled
			}
			end := start + mailImportBatchSize
			if end > len(uids) {
				end = len(uids)
			}
			batch := uids[start:end]
			set := new(imap.SeqSet)
			set.AddNum(batch...)
			messages := make(chan *imap.Message, 16)
			done := make(chan error, 1)
			go func() {
				done <- source.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}, messages)
			}()
			for message := range messages {
				if message == nil || int64(message.Uid) <= lastUID {
					continue
				}
				messageID := ""
				if message.Envelope != nil {
					messageID = strings.ToLower(strings.TrimSpace(message.Envelope.MessageId))
				}
				if messageID != "" && existing[messageID] {
					lastUID = int64(message.Uid)
					continue
				}
				body := message.GetBody(section)
				if body == nil {
					continue
				}
				raw, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
				if err != nil || int64(len(raw)) > maxBytes {
					continue
				}
				if err := target.Append(targetFolder, importFlags(message.Flags), message.InternalDate, bytes.NewReader(raw)); err != nil {
					continue
				}
				if messageID != "" {
					existing[messageID] = true
				}
				imported++
				lastUID = int64(message.Uid)
			}
			if err := <-done; err != nil {
				break
			}
			cursor[sourceFolder] = lastUID
			s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
		}
	}
	return imported, total, nil
}

func (s *Server) importPOP3Mailbox(ctx context.Context, job mailImportJobRecord, sourceEmail, password, targetEmail, cursorJSON, itemID string) (int64, int64, error) {
	source, err := dialPOP3(job.Host, job.Port, job.Security)
	if err != nil {
		return 0, 0, err
	}
	defer source.close()
	if err := source.login(sourceEmail, password); err != nil {
		return 0, 0, fmt.Errorf("source login failed: %w", err)
	}
	target, err := s.dialLocalTarget(targetEmail)
	if err != nil {
		return 0, 0, err
	}
	defer target.Logout()
	indexes, err := source.list()
	if err != nil {
		return 0, 0, fmt.Errorf("list POP3 messages: %w", err)
	}
	cursor := map[string]int64{}
	inner := map[string]any{}
	_ = json.Unmarshal([]byte(cursorJSON), &inner)
	if value, ok := inner["pop3Index"].(float64); ok {
		cursor["pop3Index"] = int64(value)
	}
	last := cursor["pop3Index"]
	existing := map[string]bool{}
	if ids, err := targetFolderMessageIDs(target, "INBOX"); err == nil {
		for id := range ids {
			existing[id] = true
		}
	}
	_, _ = target.Select("INBOX", false)
	maxBytes := mailImportMaxBytes(job.Options)
	var imported int64
	for _, index := range indexes {
		if int64(index) <= last {
			continue
		}
		if s.mailImportStatus(ctx, job.ID) == "cancelled" {
			return imported, int64(len(indexes)), errMailImportCancelled
		}
		raw, err := source.retrieve(index, maxBytes)
		if err == errMailImportMessageTooLarge {
			last = int64(index)
			cursor["pop3Index"] = last
			s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
			continue
		}
		if err != nil {
			break
		}
		if int64(len(raw)) > maxBytes {
			last = int64(index)
			cursor["pop3Index"] = last
			s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
			continue
		}
		messageID := rawMessageID(raw)
		if messageID != "" && existing[messageID] {
			last = int64(index)
			continue
		}
		if err := target.Append("INBOX", nil, time.Now(), bytes.NewReader(raw)); err == nil {
			imported++
			if messageID != "" {
				existing[messageID] = true
			}
		}
		last = int64(index)
		cursor["pop3Index"] = last
		s.persistImportCursor(ctx, itemID, job.ID, sourceEmail, cursor, imported)
	}
	return imported, int64(len(indexes)), nil
}

func (s *Server) persistImportCursor(ctx context.Context, itemID, jobID, sourceEmail string, cursor map[string]int64, imported int64) {
	folders := map[string]any{}
	pop3Index := int64(0)
	for key, value := range cursor {
		if key == "pop3Index" {
			pop3Index = value
			continue
		}
		folders[key] = value
	}
	encoded, _ := json.Marshal(map[string]any{"folders": folders, "pop3Index": pop3Index})
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE mail_import_items SET cursor=$2::jsonb, imported_messages=$3, updated_at=now() WHERE id=$1
	`, itemID, encoded, imported)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE mail_import_jobs SET imported_messages=(SELECT COALESCE(sum(imported_messages),0) FROM mail_import_items WHERE job_id=$1),
		       current_item=$2, updated_at=now() WHERE id=$1
	`, jobID, sourceEmail)
}

func (s *Server) dialLocalTarget(targetEmail string) (*imapclient.Client, error) {
	loginUser, loginPassword, err := s.mailLoginForUser(targetEmail)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return nil, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to the Alt8Mail mail server: %w", err)
	}
	conn.Timeout = 90 * time.Second
	if err := conn.Login(loginUser, loginPassword); err != nil {
		conn.Logout()
		return nil, fmt.Errorf("Alt8Mail mailbox login: %w", err)
	}
	ensureIMAPFolders(conn)
	return conn, nil
}

func dialExternalIMAP(host string, port int, security string) (*imapclient.Client, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if security == "ssl" {
		return imapclient.DialTLS(address, tlsConfig)
	}
	conn, err := imapclient.Dial(address)
	if err != nil {
		return nil, fmt.Errorf("connect to the old IMAP server: %w", err)
	}
	if security == "starttls" {
		if err := conn.StartTLS(tlsConfig); err != nil {
			conn.Logout()
			return nil, fmt.Errorf("old server STARTTLS: %w", err)
		}
	}
	return conn, nil
}

func targetFolderMessageIDs(conn *imapclient.Client, folder string) (map[string]bool, error) {
	if _, err := conn.Select(folder, true); err != nil {
		return map[string]bool{}, err
	}
	uids, err := conn.UidSearch(imap.NewSearchCriteria())
	if err != nil {
		return map[string]bool{}, err
	}
	ids := map[string]bool{}
	for start := 0; start < len(uids); start += 500 {
		end := start + 500
		if end > len(uids) {
			end = len(uids)
		}
		set := new(imap.SeqSet)
		set.AddNum(uids[start:end]...)
		messages := make(chan *imap.Message, 64)
		done := make(chan error, 1)
		go func() { done <- conn.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope}, messages) }()
		for message := range messages {
			if message != nil && message.Envelope != nil {
				if id := strings.ToLower(strings.TrimSpace(message.Envelope.MessageId)); id != "" {
					ids[id] = true
				}
			}
		}
		if err := <-done; err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func importFolderTarget(source string, options map[string]any) (string, bool) {
	name := strings.TrimSpace(source)
	upper := strings.ToUpper(name)
	includeJunk := stateBool(options, "includeJunk")
	includeTrash := stateBool(options, "includeTrash")
	includeDrafts := true
	if value, ok := options["includeDrafts"].(bool); ok {
		includeDrafts = value
	}
	switch {
	case upper == "INBOX":
		return "INBOX", true
	case strings.Contains(upper, "SENT"):
		return "Sent", true
	case strings.Contains(upper, "DRAFT"):
		if !includeDrafts {
			return "", false
		}
		return "Drafts", true
	case strings.Contains(upper, "ARCHIVE") || strings.Contains(upper, "ALL MAIL"):
		return "Archive", true
	case strings.Contains(upper, "JUNK") || strings.Contains(upper, "SPAM"):
		if !includeJunk {
			return "", false
		}
		return "Junk", true
	case strings.Contains(upper, "TRASH") || strings.Contains(upper, "DELETED"):
		if !includeTrash {
			return "", false
		}
		return "Trash", true
	default:
		cleaned := strings.TrimSpace(strings.ReplaceAll(name, "\x00", ""))
		if cleaned == "" || len([]rune(cleaned)) > 80 {
			return "Imported", true
		}
		return cleaned, true
	}
}

func importFlags(flags []string) []string {
	allowed := map[string]bool{`\Seen`: true, `\Answered`: true, `\Flagged`: true, `\Draft`: true}
	result := []string{}
	for _, flag := range flags {
		if allowed[flag] {
			result = append(result, flag)
		}
	}
	return result
}

func rawMessageID(raw []byte) string {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	headerEnd := strings.Index(text, "\n\n")
	if headerEnd < 0 {
		headerEnd = len(text)
	}
	for _, line := range strings.Split(text[:headerEnd], "\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "message-id:") {
			return strings.ToLower(strings.TrimSpace(line[len("message-id:"):]))
		}
	}
	return ""
}

func (s *Server) recoverMailImports(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `SELECT id FROM mail_import_jobs WHERE status IN ('validating','queued','running') ORDER BY created_at LIMIT 10`)
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
		s.startMailImportJob(id)
	}
}

func (s *Server) mailImportRecoveryWorker(ctx context.Context) {
	s.recoverMailImports(ctx)
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverMailImports(ctx)
		}
	}
}
