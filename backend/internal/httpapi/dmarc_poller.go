package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
)

type dmarcPollerConfig struct {
	Enabled      bool       `json:"enabled"`
	MailboxEmail string     `json:"mailboxEmail"`
	Folder       string     `json:"folder"`
	LastUID      int64      `json:"lastUid"`
	LastPollAt   *time.Time `json:"-"`
	LastError    string     `json:"lastError"`
}

func (s *Server) dmarcPollerConfig(ctx context.Context, tenantID string) (dmarcPollerConfig, error) {
	var config dmarcPollerConfig
	err := s.store.DB.QueryRow(ctx, `
		SELECT enabled, mailbox_email::text, folder, last_uid, last_poll_at, last_error
		FROM public.dmarc_pollers WHERE tenant_id=$1
	`, tenantID).Scan(&config.Enabled, &config.MailboxEmail, &config.Folder, &config.LastUID,
		&config.LastPollAt, &config.LastError)
	if err != nil {
		return dmarcPollerConfig{Folder: "INBOX"}, nil
	}
	if strings.TrimSpace(config.Folder) == "" {
		config.Folder = "INBOX"
	}
	return config, nil
}

func (s *Server) adminDMARCPoller(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	config, err := s.dmarcPollerConfig(r.Context(), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DMARC", "Could not load the DMARC poller")
		return
	}
	payload := map[string]any{
		"enabled": config.Enabled, "mailboxEmail": config.MailboxEmail,
		"folder": config.Folder, "lastUid": config.LastUID, "lastError": config.LastError,
	}
	if config.LastPollAt != nil {
		payload["lastPollAt"] = config.LastPollAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) adminSaveDMARCPoller(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Enabled      bool   `json:"enabled"`
		MailboxEmail string `json:"mailboxEmail"`
		Folder       string `json:"folder"`
		ResetCursor  bool   `json:"resetCursor"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	email := normalizeEmailValue(body.MailboxEmail)
	folder := strings.TrimSpace(body.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	if len(folder) > 120 || strings.ContainsAny(folder, "\r\n") {
		writeError(w, http.StatusBadRequest, "DMARC", "Choose a valid mailbox folder")
		return
	}
	if body.Enabled {
		if _, err := s.workspaceUserByEmail(r.Context(), sess.ActiveTenantID, email); err != nil {
			writeError(w, http.StatusBadRequest, "DMARC", "Choose an active mailbox in this organization")
			return
		}
	}
	current, _ := s.dmarcPollerConfig(r.Context(), sess.ActiveTenantID)
	lastUID := current.LastUID
	if body.ResetCursor || !strings.EqualFold(current.MailboxEmail, email) || current.Folder != folder {
		lastUID = 0
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.dmarc_pollers (tenant_id, enabled, mailbox_email, folder, last_uid, updated_at)
		VALUES ($1,$2,$3,$4,$5,now())
		ON CONFLICT (tenant_id) DO UPDATE SET enabled=EXCLUDED.enabled, mailbox_email=EXCLUDED.mailbox_email,
		  folder=EXCLUDED.folder, last_uid=EXCLUDED.last_uid, updated_at=now()
	`, sess.ActiveTenantID, body.Enabled, email, folder, lastUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DMARC", "Could not save the DMARC poller")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "dmarc.poller_saved", email, "Saved DMARC poller settings",
		map[string]any{"enabled": body.Enabled, "folder": folder})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled, "mailboxEmail": email, "folder": folder, "lastUid": lastUID})
}

func (s *Server) adminRunDMARCPoller(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	ingested, err := s.runDMARCPoller(r.Context(), sess.ActiveTenantID)
	if err != nil {
		s.recordAdminAudit(r.Context(), sess, sess.User, "dmarc.poller_failed", "", "DMARC poller failed", map[string]any{"error": truncate(err.Error(), 300)})
		writeError(w, http.StatusBadGateway, "DMARC", err.Error())
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "dmarc.poller_run", "", "Polled DMARC reports",
		map[string]any{"ingested": ingested})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ingested": ingested})
}

func (s *Server) runDMARCPoller(ctx context.Context, tenantID string) (int, error) {
	if !s.entitled(FeatureDeliverability) {
		return 0, errors.New("the deliverability license feature is not active")
	}
	config, err := s.dmarcPollerConfig(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if !config.Enabled {
		return 0, errors.New("the DMARC poller is disabled")
	}
	if strings.TrimSpace(config.MailboxEmail) == "" {
		return 0, errors.New("choose a mailbox before polling DMARC reports")
	}
	if !s.mailSyncConfigured() {
		return 0, errors.New("the mail server connection is not configured")
	}
	user, err := s.workspaceUserByEmail(ctx, tenantID, config.MailboxEmail)
	if err != nil {
		return 0, errors.New("the configured DMARC mailbox is unavailable")
	}
	conn, err := s.dialLocalTarget(user.Email)
	if err != nil {
		return 0, err
	}
	defer conn.Logout()
	if _, err := conn.Select(config.Folder, false); err != nil {
		return 0, fmt.Errorf("could not open the DMARC folder: %w", err)
	}
	uids, err := conn.UidSearch(nil)
	if err != nil {
		return 0, fmt.Errorf("could not search DMARC mail: %w", err)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	targets := []uint32{}
	for _, uid := range uids {
		if int64(uid) > config.LastUID {
			targets = append(targets, uid)
		}
	}
	if len(targets) > 200 {
		targets = targets[:200]
	}
	section := &imap.BodySectionName{Peek: true}
	ingested := 0
	lastUID := config.LastUID
	for _, uid := range targets {
		if ctx.Err() != nil {
			break
		}
		set := new(imap.SeqSet)
		set.AddNum(uid)
		messages := make(chan *imap.Message, 1)
		done := make(chan error, 1)
		go func() {
			done <- conn.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchFlags, imap.FetchInternalDate, section.FetchItem()}, messages)
		}()
		for message := range messages {
			if message == nil {
				continue
			}
			raw, readErr := io.ReadAll(io.LimitReader(message.GetBody(section), 12*1024*1024+1))
			if readErr != nil || len(raw) > 12*1024*1024 {
				continue
			}
			parsed := parseIMAPMessage(message, raw, config.Folder)
			for _, attachment := range parsed.Attachments {
				if !dmarcAttachmentCandidate(attachment.Name, attachment.MIMEType) {
					continue
				}
				if _, err := s.ingestDMARCReport(ctx, tenantID, attachment.Data); err == nil {
					ingested++
				}
			}
			_ = conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag}, nil)
		}
		if err := <-done; err == nil && int64(uid) > lastUID {
			lastUID = int64(uid)
		}
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.dmarc_pollers SET last_uid=$2, last_poll_at=now(), last_error='' WHERE tenant_id=$1
	`, tenantID, lastUID)
	return ingested, nil
}

func dmarcAttachmentCandidate(name, mimeType string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".xml.gz") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".gz") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/xml", "text/xml", "application/gzip", "application/zip", "application/x-gzip":
		return true
	}
	return false
}

func (s *Server) dmarcPollerWorker(ctx context.Context) {
	run := func() {
		rows, err := s.store.DB.Query(ctx, `SELECT tenant_id::text FROM public.dmarc_pollers WHERE enabled`)
		if err != nil {
			return
		}
		tenants := []string{}
		for rows.Next() {
			var tenantID string
			if rows.Scan(&tenantID) == nil {
				tenants = append(tenants, tenantID)
			}
		}
		rows.Close()
		for _, tenantID := range tenants {
			if _, err := s.runDMARCPoller(ctx, tenantID); err != nil {
				_, _ = s.store.DB.Exec(ctx, `UPDATE public.dmarc_pollers SET last_poll_at=now(), last_error=$2 WHERE tenant_id=$1`,
					tenantID, truncate(err.Error(), 500))
			}
		}
	}
	run()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
