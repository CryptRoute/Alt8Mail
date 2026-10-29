package control

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
	"github.com/meridian-mail/meridian/backend/internal/stateinit"
)

func (s *Server) registerMailboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/mailboxes", s.require("mailboxes.read", s.handleMailboxes))
	mux.HandleFunc("POST /control/api/mailboxes", s.require("mail.manage", s.handleMailboxCreate))
	mux.HandleFunc("GET /control/api/mailboxes/{id}", s.require("mailboxes.read", s.handleMailboxDetail))
	mux.HandleFunc("PATCH /control/api/mailboxes/{id}", s.require("mail.manage", s.handleMailboxUpdate))
	mux.HandleFunc("DELETE /control/api/mailboxes/{id}", s.require("mail.manage", s.handleMailboxDelete))
	mux.HandleFunc("POST /control/api/mailboxes/{id}/password", s.require("mail.manage", s.handleMailboxPassword))
	mux.HandleFunc("GET /control/api/mailboxes/{id}/history", s.require("mailboxes.read", s.handleMailboxHistory))
	mux.HandleFunc("GET /control/api/mailboxes/{id}/sieve", s.require("mailboxes.read", s.handleSieveGet))
	mux.HandleFunc("PUT /control/api/mailboxes/{id}/sieve", s.require("mail.manage", s.handleSieveSave))
	mux.HandleFunc("POST /control/api/mailboxes/{id}/sieve/preview", s.require("mailboxes.read", s.handleSievePreview))
}

func initials(value string) string {
	parts := strings.Fields(value)
	if len(parts) >= 2 {
		return strings.ToUpper(string(parts[0][0]) + string(parts[len(parts)-1][0]))
	}
	if len(value) >= 2 {
		return strings.ToUpper(value[:2])
	}
	return strings.ToUpper(value)
}

func avatarColor(value string) string {
	palette := []string{"#3f5d7a", "#2f7d68", "#6b4fa1", "#9a5b33", "#23648a", "#7c3f58"}
	sum := 0
	for _, char := range value {
		sum += int(char)
	}
	return palette[sum%len(palette)]
}

func (s *Server) handleMailboxes(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	statusFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if statusFilter == "" {
		// Preserve the historical includeDisabled flag for older clients while
		// the panel defaults to the full account inventory.
		if queryBool(r, "includeDisabled", false) {
			statusFilter = "all"
		} else {
			statusFilter = "enabled"
		}
	}
	if statusFilter != "all" && statusFilter != "enabled" && statusFilter != "suspended" {
		writeError(w, http.StatusBadRequest, "STATUS", "Status filter must be all, enabled or suspended")
		return
	}
	pageSize := queryInt(r, "pageSize", 0, 0, 5000)
	if pageSize == 0 {
		pageSize = queryInt(r, "limit", 100, 1, 5000)
	}
	if pageSize > 5000 {
		pageSize = 5000
	}
	page := queryInt(r, "page", 1, 1, 1_000_000)
	sortBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort")))
	if sortBy == "" {
		sortBy = "email"
	}
	orderBy, ok := map[string]string{
		"email":     "m.email ASC",
		"name":      "lower(u.name) ASC",
		"status":    "CASE WHEN u.status='active' AND m.active AND m.provision_status <> 'error' AND u.deleted_at IS NULL THEN 0 WHEN m.active THEN 1 ELSE 2 END ASC, m.email ASC",
		"quota":     "CASE WHEN u.quota_bytes > 0 THEN u.used_bytes::float8 / u.quota_bytes::float8 ELSE -1 END DESC, m.email ASC",
		"used":      "u.used_bytes DESC, m.email ASC",
		"created":   "m.created_at DESC, m.email ASC",
		"lastLogin": "u.last_login_at DESC NULLS LAST, m.email ASC",
	}[sortBy]
	if !ok {
		writeError(w, http.StatusBadRequest, "SORT", "Sort must be email, name, status, quota, used, created or lastLogin")
		return
	}
	dirValue := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dir")))
	if dirValue != "desc" {
		dirValue = "asc"
	}
	if dirValue == "desc" {
		orderBy = reversePrimaryOrder(orderBy)
	}
	where := `WHERE ($1='' OR m.email::text ILIKE '%'||$1||'%' OR u.name ILIKE '%'||$1||'%')`
	switch statusFilter {
	case "enabled":
		where += ` AND u.status='active' AND u.deleted_at IS NULL AND m.active AND m.provision_status <> 'error'`
	case "suspended":
		where += ` AND (u.status <> 'active' OR u.deleted_at IS NOT NULL OR NOT m.active OR m.provision_status = 'error')`
	}
	var total, enabledCount, suspendedCount, nearQuota int64
	if err := s.db.QueryRow(r.Context(), `
		SELECT count(*),
		       count(*) FILTER (WHERE u.status='active' AND u.deleted_at IS NULL AND m.active AND m.provision_status <> 'error'),
		       count(*) FILTER (WHERE u.status <> 'active' OR u.deleted_at IS NOT NULL OR NOT m.active OR m.provision_status = 'error'),
		       count(*) FILTER (WHERE u.quota_bytes > 0 AND u.used_bytes::float8 / u.quota_bytes::float8 > 0.85)
		FROM public.mailboxes m JOIN public.users u ON u.id=m.user_id
		`+where, query).Scan(&total, &enabledCount, &suspendedCount, &nearQuota); err != nil {
		writeFromError(w, err)
		return
	}
	pages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.Query(r.Context(), `
		SELECT m.id::text, m.email::text, m.active, m.provision_status, m.created_at,
		       u.id::text, u.name, u.status, u.quota_bytes, u.used_bytes, u.last_login_at,
		       m.domain::text, m.local_part::text
		FROM public.mailboxes m
		JOIN public.users u ON u.id=m.user_id
		`+where+`
		ORDER BY `+orderBy+`
		LIMIT $2 OFFSET $3
	`, query, pageSize, offset)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var mailboxID, email, provision, userID, name, status, domain, local string
		var active bool
		var created time.Time
		var quota, used int64
		var lastLogin *time.Time
		if rows.Scan(&mailboxID, &email, &active, &provision, &created, &userID, &name, &status,
			&quota, &used, &lastLogin, &domain, &local) == nil {
			item := map[string]any{
				"id": mailboxID, "email": email, "active": active, "provisionStatus": provision,
				"createdAt": created.UTC().Format(time.RFC3339), "userId": userID, "name": name,
				"status": status, "quotaBytes": quota, "usedBytes": used, "domain": domain, "localPart": local,
				"enabled": status == "active" && active && provision != "error",
			}
			if lastLogin != nil {
				item["lastLoginAt"] = lastLogin.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mailboxes": items, "count": len(items), "total": total, "page": page,
		"pageSize": pageSize, "pages": pages, "sort": sortBy,
		"dir": dirValue, "status": statusFilter,
		"summary": map[string]any{"total": total, "enabled": enabledCount, "suspended": suspendedCount, "nearQuota": nearQuota},
	})
}

// reversePrimaryOrder flips only the first sort key and keeps the stable
// e-mail tiebreaker ascending. It is used by the panel's direction toggle
// without accepting raw SQL from the query string.
func reversePrimaryOrder(order string) string {
	parts := strings.SplitN(order, ",", 2)
	primary := strings.TrimSpace(parts[0])
	nulls := ""
	if strings.HasSuffix(primary, " NULLS LAST") {
		primary = strings.TrimSuffix(primary, " NULLS LAST")
		nulls = " NULLS LAST"
	} else if strings.HasSuffix(primary, " NULLS FIRST") {
		primary = strings.TrimSuffix(primary, " NULLS FIRST")
		nulls = " NULLS FIRST"
	}
	switch {
	case strings.HasSuffix(primary, " ASC"):
		primary = strings.TrimSuffix(primary, " ASC") + " DESC"
	case strings.HasSuffix(primary, " DESC"):
		primary = strings.TrimSuffix(primary, " DESC") + " ASC"
	default:
		primary += " DESC"
	}
	primary += nulls
	if len(parts) == 2 {
		return primary + ", " + strings.TrimSpace(parts[1])
	}
	return primary
}

func (s *Server) handleMailboxCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name       string `json:"name"`
		LocalPart  string `json:"localPart"`
		DomainID   string `json:"domainId"`
		Domain     string `json:"domain"`
		Password   string `json:"password"`
		QuotaBytes int64  `json:"quotaBytes"`
		SendTo     string `json:"sendTo"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	local := normalizeLocal(body.LocalPart)
	if body.DomainID != "" {
		if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id=$1 AND status IN ('manual','verified')`, body.DomainID).Scan(&body.Domain); err != nil {
			writeError(w, http.StatusBadRequest, "DOMAIN", "Choose a managed domain")
			return
		}
	}
	domain := normalizeDomain(body.Domain)
	if !validLocalPart(local) || !validDomain(domain) {
		writeError(w, http.StatusBadRequest, "ADDRESS", "Enter a valid mailbox local part and domain")
		return
	}
	email := local + "@" + domain
	var tenantID, domainStatus string
	if err := s.db.QueryRow(r.Context(), `SELECT tenant_id::text, status FROM public.mail_domains WHERE domain=$1`, domain).Scan(&tenantID, &domainStatus); err != nil || (domainStatus != "manual" && domainStatus != "verified") {
		writeError(w, http.StatusBadRequest, "DOMAIN", "The domain is not managed by this panel")
		return
	}
	var conflict int
	_ = s.db.QueryRow(r.Context(), `
		SELECT (SELECT count(*) FROM public.users WHERE email=$1)
		     + (SELECT count(*) FROM public.mailboxes WHERE email=$1)
		     + (SELECT count(*) FROM public.user_aliases WHERE email=$1)
	`, email).Scan(&conflict)
	if conflict > 0 {
		writeError(w, http.StatusConflict, "MAILBOX", "That address already exists as a mailbox or alias")
		return
	}
	if body.Name == "" {
		body.Name = titleWords(strings.ReplaceAll(local, ".", " "))
	}
	if body.QuotaBytes == 0 {
		body.QuotaBytes = 16 * 1024 * 1024 * 1024
	}
	if body.QuotaBytes < 256*1024*1024 || body.QuotaBytes > 1024*1024*1024*1024 {
		writeError(w, http.StatusBadRequest, "QUOTA", "Quota must be between 256 MB and 1 TB")
		return
	}
	temporary := strings.TrimSpace(body.Password)
	if temporary == "" {
		var err error
		temporary, err = randomPassword(20)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not generate a password")
			return
		}
	}
	if len(temporary) < 12 {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Use at least 12 characters for a mailbox password")
		return
	}
	webHash, err := auth.HashPassword(temporary)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PASSWORD", err.Error())
		return
	}
	mailHash, err := hashMailPassword(temporary)
	if err != nil {
		writeError(w, http.StatusBadGateway, "MAIL_PASSWORD", "Could not create a Dovecot password hash: "+err.Error())
		return
	}
	maildir := s.maildirPath(domain, local)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var userID, mailboxID string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO public.users (tenant_id,email,name,initials,color,role,status,password_hash,
		  mail_password_hash,password_changed_at,quota_bytes,force_password_change)
		VALUES ($1,$2,$3,$4,$5,'member','active',$6,$7,now(),$8,true)
		RETURNING id::text
	`, tenantID, email, body.Name, initials(body.Name), avatarColor(email), webHash, mailHash, body.QuotaBytes).Scan(&userID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "MAILBOX", "That address already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `
		INSERT INTO public.user_aliases (tenant_id,user_id,email,label,kind,can_send,active)
		VALUES ($1,$2,$3,'Primary','primary',true,true)
	`, tenantID, userID, email)
	if err != nil {
		writeFromError(w, err)
		return
	}
	err = tx.QueryRow(r.Context(), `
		INSERT INTO public.mailboxes (tenant_id,user_id,email,domain,local_part,maildir_path,active,provision_status)
		VALUES ($1,$2,$3,$4,$5,$6,true,'pending') RETURNING id::text
	`, tenantID, userID, email, domain, local, maildir).Scan(&mailboxID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	path, err := s.ensureMaildir(domain, local)
	if err == nil {
		_, _ = s.db.Exec(r.Context(), `UPDATE public.mailboxes SET maildir_path=$2, provision_status='ready', provisioned_at=now() WHERE id=$1`, mailboxID, path)
		maildir = path
	} else {
		_, _ = s.db.Exec(r.Context(), `UPDATE public.mailboxes SET provision_status='error', provision_error=$2 WHERE id=$1`, mailboxID, truncateText(err.Error(), 500))
	}
	if err := s.ensureUserState(r.Context(), email); err != nil {
		s.logger.Warn("control user state initialization failed", "email", email, "err", err)
	}
	emailed := false
	if strings.TrimSpace(body.SendTo) != "" {
		sent := s.runSMTPTransaction(r.Context(), s.defaultMailFrom(r.Context()), body.SendTo,
			"Your new Alt8Mail mailbox password",
			"A mailbox has been created for "+email+".\r\n\r\nTemporary password: "+temporary+"\r\n\r\nChange it after your first sign-in.")
		emailed = sent.Err == nil
	}
	s.actorAudit(r, sess, "mail.mailbox_created", email, "Created a mailbox account", map[string]any{"quotaBytes": body.QuotaBytes, "provisionError": err != nil})
	s.notifyEvent(r.Context(), "user.created", map[string]any{"email": email, "name": body.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "id": mailboxID, "userId": userID, "email": email,
		"temporaryPassword": temporary, "emailed": emailed, "maildir": maildir,
		"message": "Copy the temporary password now. It is not stored in clear text.",
	})
}

func (s *Server) ensureUserState(ctx context.Context, email string) error {
	state, err := stateinit.Build(ctx, s.db, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var userID, tenantID string
	if err := s.db.QueryRow(ctx, `SELECT id::text, tenant_id::text FROM public.users WHERE email=$1`, email).Scan(&userID, &tenantID); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO public.app_states (user_id, tenant_id, state, updated_at)
		VALUES ($1,$2,$3::jsonb,now())
		ON CONFLICT (user_id) DO NOTHING
	`, userID, tenantID, raw)
	return err
}

func (s *Server) handleMailboxDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var mailboxID, email, name, status, provision, domain, local, maildir, userID string
	var active bool
	var quota, used int64
	var createdAt time.Time
	var lastLogin *time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT m.id::text,m.email::text,u.name,u.status,m.provision_status,m.domain::text,m.local_part::text,
		       m.maildir_path,u.quota_bytes,u.used_bytes,m.active,m.created_at,u.last_login_at,
		       m.user_id::text
		FROM public.mailboxes m JOIN public.users u ON u.id=m.user_id WHERE m.id=$1
	`, id).Scan(&mailboxID, &email, &name, &status, &provision, &domain, &local, &maildir,
		&quota, &used, &active, &createdAt, &lastLogin, &userID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	item := map[string]any{
		"id": mailboxID, "email": email, "name": name, "status": status, "active": active,
		"provisionStatus": provision, "domain": domain, "localPart": local, "maildir": maildir,
		"quotaBytes": quota, "usedBytes": used, "createdAt": createdAt.UTC().Format(time.RFC3339),
		"userId": userID,
	}
	if lastLogin != nil {
		item["lastLoginAt"] = lastLogin.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": item})
}

func (s *Server) handleMailboxUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name       *string `json:"name"`
		QuotaBytes *int64  `json:"quotaBytes"`
		Status     *string `json:"status"`
		Active     *bool   `json:"active"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var userID string
	if err := s.db.QueryRow(r.Context(), `SELECT user_id::text FROM public.mailboxes WHERE id=$1`, id).Scan(&userID); err != nil {
		writeFromError(w, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "NAME", "Display name cannot be empty")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE public.users SET name=$2, initials=$3, updated_at=now() WHERE id=$1`, userID, name, initials(name))
		if err != nil {
			writeFromError(w, err)
			return
		}
	}
	if body.QuotaBytes != nil {
		if *body.QuotaBytes < 256*1024*1024 || *body.QuotaBytes > 1024*1024*1024*1024 {
			writeError(w, http.StatusBadRequest, "QUOTA", "Quota must be between 256 MB and 1 TB")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE public.users SET quota_bytes=$2, updated_at=now() WHERE id=$1`, userID, *body.QuotaBytes)
		if err != nil {
			writeFromError(w, err)
			return
		}
	}
	if body.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*body.Status))
		if status != "active" && status != "suspended" {
			writeError(w, http.StatusBadRequest, "STATUS", "Status must be active or suspended")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE public.users SET status=$2, updated_at=now() WHERE id=$1`, userID, status)
		if err != nil {
			writeFromError(w, err)
			return
		}
		if status == "suspended" {
			_, _ = tx.Exec(r.Context(), `UPDATE public.mailboxes SET active=false WHERE user_id=$1`, userID)
		} else {
			_, _ = tx.Exec(r.Context(), `UPDATE public.mailboxes SET active=true WHERE user_id=$1`, userID)
		}
	}
	if body.Active != nil {
		_, err = tx.Exec(r.Context(), `UPDATE public.mailboxes SET active=$2 WHERE id=$1`, id, *body.Active)
		if err != nil {
			writeFromError(w, err)
			return
		}
		if *body.Active {
			_, _ = tx.Exec(r.Context(), `UPDATE public.users SET status='active' WHERE id=$1`, userID)
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "mail.mailbox_updated", id, "Updated mailbox settings", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMailboxDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		ConfirmEmail string `json:"confirmEmail"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var email, userID, domain, local string
	if err := s.db.QueryRow(r.Context(), `
		SELECT m.email::text, m.user_id::text, m.domain::text, m.local_part::text
		FROM public.mailboxes m WHERE m.id=$1
	`, id).Scan(&email, &userID, &domain, &local); err != nil {
		writeFromError(w, err)
		return
	}
	if normalizeDomain(strings.TrimSpace(body.ConfirmEmail)) != normalizeDomain(email) {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type the full mailbox address to confirm deletion")
		return
	}
	archive := ""
	maildir := s.maildirPath(domain, local)
	if pathExists(maildir) {
		archiveDir := filepath.Join(s.cfg.BackupDir, "deleted-mailboxes")
		_ = os.MkdirAll(archiveDir, 0o700)
		archive = filepath.Join(archiveDir, strings.ReplaceAll(email, "@", "_")+"-"+time.Now().UTC().Format("20060102-150405"))
		if err := movePath(r.Context(), filepath.Dir(maildir), archive); err != nil {
			writeError(w, http.StatusInternalServerError, "ARCHIVE", "Could not archive the mailbox: "+err.Error())
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM public.users WHERE id=$1`, userID); err != nil {
		if archive != "" {
			_ = movePath(context.Background(), archive, filepath.Dir(maildir))
		}
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "mail.mailbox_deleted", email, "Deleted a mailbox account", map[string]any{"archive": archive})
	s.notifyEvent(r.Context(), "user.deleted", map[string]any{"email": email})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email, "archive": archive})
}

func (s *Server) probeDovecotAuth(ctx context.Context, email, password string) (bool, string) {
	flushCtx, flushCancel := context.WithTimeout(ctx, 8*time.Second)
	_, _ = runCommand(flushCtx, 8*time.Second, "doveadm", "auth", "cache", "flush", email)
	flushCancel()
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, "doveadm", "auth", "test", email)
	command.Stdin = strings.NewReader(password + "\n")
	output, err := command.CombinedOutput()
	text := string(output)
	if strings.Contains(text, "auth succeeded") {
		return true, "Dovecot accepted the new password hash"
	}
	if err != nil {
		return false, strings.TrimSpace(text)
	}
	return false, "Dovecot did not accept the new password hash"
}

func (s *Server) handleMailboxPassword(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Password string `json:"password"`
		SendTo   string `json:"sendTo"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var email, userID, oldWeb, oldMail string
	if err := s.db.QueryRow(r.Context(), `
		SELECT m.email::text, u.id::text, COALESCE(u.password_hash,''), COALESCE(u.mail_password_hash,'')
		FROM public.mailboxes m JOIN public.users u ON u.id=m.user_id WHERE m.id=$1
	`, id).Scan(&email, &userID, &oldWeb, &oldMail); err != nil {
		writeFromError(w, err)
		return
	}
	temporary := strings.TrimSpace(body.Password)
	if temporary == "" {
		var err error
		temporary, err = randomPassword(20)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PASSWORD", "Could not generate a password")
			return
		}
	}
	if len(temporary) < 12 {
		writeError(w, http.StatusBadRequest, "PASSWORD", "Use at least 12 characters")
		return
	}
	webHash, err := auth.HashPassword(temporary)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PASSWORD", err.Error())
		return
	}
	mailHash, err := hashMailPassword(temporary)
	if err != nil {
		writeError(w, http.StatusBadGateway, "MAIL_PASSWORD", "Could not create a Dovecot hash: "+err.Error())
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE public.users SET password_hash=$2, mail_password_hash=$3, password_changed_at=now(),
		  force_password_change=true, updated_at=now() WHERE id=$1
	`, userID, webHash, mailHash)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if oldWeb != "" {
		_, _ = s.db.Exec(r.Context(), `INSERT INTO public.password_history (user_id,password_hash) VALUES ($1,$2)`, userID, oldWeb)
	}
	ok, probeDetail := s.probeDovecotAuth(r.Context(), email, temporary)
	if !ok {
		_, _ = s.db.Exec(r.Context(), `UPDATE public.users SET password_hash=$2, mail_password_hash=$3 WHERE id=$1`, userID, nullIfEmpty(oldWeb), nullIfEmpty(oldMail))
		writeError(w, http.StatusBadGateway, "DOVECOT_PROBE", "The new hash was rolled back because Dovecot could not verify it: "+probeDetail)
		return
	}
	emailed := false
	if strings.TrimSpace(body.SendTo) != "" {
		sent := s.runSMTPTransaction(r.Context(), s.defaultMailFrom(r.Context()), body.SendTo,
			"Your new Alt8Mail mailbox password",
			"The password for "+email+" has been reset.\r\n\r\nNew password: "+temporary+"\r\n\r\nSign in and change it after first use.")
		emailed = sent.Err == nil
	}
	s.actorAudit(r, sess, "mail.password_reset", email, "Reset a mailbox password after a successful Dovecot login probe",
		map[string]any{"probe": probeDetail, "emailed": emailed})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "temporaryPassword": temporary, "probe": probeDetail, "emailed": emailed,
		"message": "The new hash was accepted by Dovecot. Copy the password now.",
	})
}

func (s *Server) handleMailboxHistory(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var email string
	if err := s.db.QueryRow(r.Context(), `SELECT email::text FROM public.mailboxes WHERE id=$1`, id).Scan(&email); err != nil {
		writeFromError(w, err)
		return
	}
	retention := s.settingInt(r.Context(), "logs.retention_days", s.cfg.RetentionDays)
	if retention < 1 {
		retention = 365
	}
	limit := queryInt(r, "limit", 200, 1, 1000)
	rows, err := s.db.Query(r.Context(), `
		SELECT id, event_at, direction, status, sender::text, recipient::text, subject,
		       size_bytes, delay_seconds, queue_id, detail
		FROM control.mail_events
		WHERE (sender=$1 OR recipient=$1) AND event_at > now() - ($2::int || ' days')::interval
		ORDER BY event_at DESC LIMIT $3
	`, email, retention, limit)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var at time.Time
		var direction, status, sender, recipient, subject, queueID, detail string
		var sizeBytes int64
		var delay int
		if rows.Scan(&id, &at, &direction, &status, &sender, &recipient, &subject, &sizeBytes, &delay, &queueID, &detail) == nil {
			items = append(items, map[string]any{
				"id": id, "at": at.UTC().Format(time.RFC3339), "direction": direction,
				"status": status, "sender": sender, "recipient": recipient, "subject": subject,
				"sizeBytes": sizeBytes, "delaySeconds": delay, "queueId": queueID, "detail": detail,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": email, "retentionDays": retention, "history": items})
}
