package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const retentionBatchSize = 500

type retentionStats struct {
	Users    int      `json:"users"`
	Folders  int      `json:"folders"`
	Messages int      `json:"messages"`
	Failures int      `json:"failures"`
	Errors   []string `json:"errors,omitempty"`
}

// runTenantRetention permanently removes messages older than the tenant's
// retention window from the synchronized mail folders. Drafts, Junk and Trash
// are intentionally excluded: they have their own lifecycle (work in progress,
// quarantine and deletion). The method is safe to call while users are online
// because each user's mailbox is serialized through its own worker lock.
func (s *Server) runTenantRetention(ctx context.Context, tenantID string) (retentionStats, error) {
	stats := retentionStats{Errors: []string{}}
	policy := s.tenantPolicyFor(ctx, tenantID)
	if policy.Mail.RetentionDays <= 0 {
		return stats, nil
	}
	if !s.masterMailConfigured() {
		return stats, fmt.Errorf("IMAP master credentials are not configured")
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.email::text, u.tenant_id::text, u.name, u.initials, u.color, u.role, u.status,
		       u.preferences, u.security, u.quota_bytes, u.used_bytes,
		       u.enrollment_required, u.force_password_change, u.password_expires_at
		FROM users u JOIN mailboxes m ON m.user_id = u.id
		WHERE u.tenant_id = $1 AND u.status = 'active' AND u.deleted_at IS NULL AND m.active = true
		ORDER BY u.email
	`, tenantID)
	if err != nil {
		return stats, err
	}
	users := []userRecord{}
	for rows.Next() {
		var user userRecord
		if rows.Scan(&user.ID, &user.Email, &user.TenantID, &user.Name, &user.Initials, &user.Color,
			&user.Role, &user.Status, &user.Preferences, &user.Security, &user.QuotaBytes, &user.UsedBytes,
			&user.EnrollmentRequired, &user.ForcePasswordChange, &user.PasswordExpiresAt) == nil {
			users = append(users, user)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -policy.Mail.RetentionDays)
	for _, user := range users {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		stats.Users++
		removed, folders, err := s.expungeRetainedMessages(ctx, user, cutoff, policy.Mail.RetentionDays)
		stats.Folders += folders
		stats.Messages += removed
		if err != nil {
			stats.Failures++
			if len(stats.Errors) < 20 {
				stats.Errors = append(stats.Errors, user.Email+": "+err.Error())
			}
			s.logger.Warn("mail retention failed", "user", user.Email, "err", err)
		}
	}
	return stats, nil
}

func (s *Server) masterMailConfigured() bool {
	return s.cfg.IMAPMasterUser != "" && s.cfg.IMAPMasterPassword != ""
}

// uidExpunge uses the UIDPLUS extension so a retention pass never removes a
// message another client marked \Deleted but has not yet expunged.
func uidExpunge(conn *imapclient.Client, set *imap.SeqSet) error {
	status, err := conn.Execute(&imap.Command{
		Name:      "UID",
		Arguments: []interface{}{imap.RawString("EXPUNGE"), set},
	}, nil)
	if err != nil {
		return err
	}
	if status == nil {
		return fmt.Errorf("UID EXPUNGE returned no status")
	}
	return status.Err()
}

func (s *Server) expungeRetainedMessages(ctx context.Context, user userRecord, cutoff time.Time, retentionDays int) (int, int, error) {
	lock := s.userMailLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return 0, 0, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return 0, 0, fmt.Errorf("connect to IMAP: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 60 * time.Second
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return 0, 0, err
	}
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return 0, 0, fmt.Errorf("IMAP login: %w", err)
	}
	if ok, err := conn.Support("UIDPLUS"); err != nil || !ok {
		return 0, 0, fmt.Errorf("IMAP server does not support UIDPLUS; retention was not applied")
	}
	mailboxes, err := listIMAPMailboxes(conn)
	if err != nil {
		return 0, 0, err
	}
	deletedByFolder := map[string]map[uint32]bool{}
	validityByFolder := map[string]uint32{}
	folders := 0
	for _, mailbox := range mailboxes {
		if ctx.Err() != nil {
			return 0, folders, ctx.Err()
		}
		appFolder, ok := mapIMAPFolder(mailbox)
		if !ok || appFolder == "drafts" || appFolder == "spam" || appFolder == "trash" {
			continue
		}
		status, err := conn.Select(mailbox, false)
		if err != nil {
			s.logger.Warn("retention select mailbox", "mailbox", mailbox, "err", err)
			continue
		}
		validityByFolder[appFolder] = status.UidValidity
		criteria := imap.NewSearchCriteria()
		criteria.Before = cutoff
		uids, err := conn.UidSearch(criteria)
		if err != nil {
			s.logger.Warn("retention search mailbox", "mailbox", mailbox, "err", err)
			continue
		}
		if len(uids) == 0 {
			continue
		}
		folders++
		if deletedByFolder[appFolder] == nil {
			deletedByFolder[appFolder] = map[uint32]bool{}
		}
		for start := 0; start < len(uids); start += retentionBatchSize {
			end := start + retentionBatchSize
			if end > len(uids) {
				end = len(uids)
			}
			set := new(imap.SeqSet)
			set.AddNum(uids[start:end]...)
			if _, err := conn.Select(mailbox, false); err != nil {
				return lenUIDs(deletedByFolder), folders, err
			}
			values := []interface{}{imap.DeletedFlag}
			if err := conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), values, nil); err != nil {
				s.logger.Warn("retention flag failed", "mailbox", mailbox, "err", err)
				continue
			}
			if err := uidExpunge(conn, set); err != nil {
				// Do not fall back to EXPUNGE: that could purge other clients'
				// pending deletions. Clear only the flags this pass added.
				_ = conn.UidStore(set, imap.FormatFlagsOp(imap.RemoveFlags, true), values, nil)
				return lenUIDs(deletedByFolder), folders, fmt.Errorf("UID EXPUNGE failed in %s: %w", mailbox, err)
			}
			for _, uid := range uids[start:end] {
				deletedByFolder[appFolder][uid] = true
			}
		}
	}
	removed := lenUIDs(deletedByFolder)
	if removed == 0 {
		return 0, folders, nil
	}
	now := nowISO()
	removedState := 0
	for folder, uids := range deletedByFolder {
		if len(uids) == 0 {
			continue
		}
		uidList := make([]int64, 0, len(uids))
		for uid := range uids {
			uidList = append(uidList, int64(uid))
		}
		result, err := s.store.DB.Exec(ctx, `
			DELETE FROM mail_messages
			WHERE user_id=$1 AND imap_folder=$2 AND uid_validity=$3 AND uid = ANY($4)
		`, user.ID, folder, int64(validityByFolder[folder]), uidList)
		if err != nil {
			return removed, folders, fmt.Errorf("mail removed from Dovecot but database cleanup failed: %w", err)
		}
		removedState += int(result.RowsAffected())
	}
	stateErr := s.withState(user.ID, user.TenantID, func(state map[string]any) error {
		meta := stateMap(state, "mailRetention")
		meta["lastRunAt"] = now
		meta["lastRemoved"] = removed
		meta["retentionDays"] = retentionDays
		meta["lastError"] = nil
		state["mailRetention"] = meta
		if removedState > 0 {
			appendStateAudit(state, "mail.retention", fmt.Sprintf("%d message", removedState), user.Name,
				fmt.Sprintf("Retention policy removed %d message(s) older than %d days", removedState, retentionDays),
				"127.0.0.1", "meridian-retention-worker")
		}
		return nil
	})
	if stateErr != nil {
		return removed, folders, fmt.Errorf("mail removed from Dovecot but state cleanup failed: %w", stateErr)
	}
	return removed, folders, nil
}

func (s *Server) adminRunRetention(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if s.tenantPolicyFor(r.Context(), sess.ActiveTenantID).Mail.RetentionDays <= 0 {
		writeError(w, http.StatusConflict, "RETENTION_DISABLED", "Set a retention period before running retention")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	stats, err := s.runTenantRetention(ctx, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "RETENTION_FAILED", err.Error())
		return
	}
	s.recordAdminAudit(ctx, sess, sess.User, "admin.retention_run", "Mail retention", "Ran retention maintenance",
		map[string]any{"tenantId": sess.ActiveTenantID, "removed": stats.Messages, "users": stats.Users})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": stats})
}

func lenUIDs(values map[string]map[uint32]bool) int {
	total := 0
	for _, uids := range values {
		total += len(uids)
	}
	return total
}

// retentionWorker runs once per day. The first pass is delayed so a restart
// does not immediately compete with mailbox synchronization.
func (s *Server) retentionWorker(ctx context.Context) {
	if !s.masterMailConfigured() {
		return
	}
	delay := 15 * time.Minute
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runScheduledRetention(ctx)
			timer.Reset(24 * time.Hour)
		}
	}
}

func (s *Server) runScheduledRetention(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id::text FROM tenants
		WHERE status = 'active' AND COALESCE((settings->'mail'->>'retentionDays')::int, 0) > 0
		ORDER BY id
	`)
	if err != nil {
		s.logger.Warn("retention tenant list failed", "err", err)
		return
	}
	tenantIDs := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			tenantIDs = append(tenantIDs, id)
		}
	}
	rows.Close()
	for _, tenantID := range tenantIDs {
		if ctx.Err() != nil {
			return
		}
		tenantCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		stats, err := s.runTenantRetention(tenantCtx, tenantID)
		cancel()
		if err != nil {
			s.logger.Warn("scheduled retention failed", "tenant", tenantID, "err", err)
			continue
		}
		if stats.Messages > 0 {
			s.logger.Info("scheduled retention complete", "tenant", tenantID, "users", stats.Users, "removed", stats.Messages)
		}
	}
}
