package httpapi

import (
	"context"
	"io"
	"sort"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
	"github.com/emersion/go-imap"
)

// fetchMailboxAttachments reads attachments from the local mail server for a
// private deliverability module. It never returns credentials or connection
// objects across the enterprise bridge.
func (s *Server) fetchMailboxAttachments(ctx context.Context, email, folder string, afterUID int64, limit int) ([]enterprise.MailAttachment, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	conn, err := s.dialLocalTarget(email)
	if err != nil {
		return nil, afterUID, err
	}
	defer conn.Logout()
	if _, err := conn.Select(folder, false); err != nil {
		return nil, afterUID, err
	}
	uids, err := conn.UidSearch(nil)
	if err != nil {
		return nil, afterUID, err
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	targets := []uint32{}
	for _, uid := range uids {
		if int64(uid) > afterUID {
			targets = append(targets, uid)
		}
	}
	if len(targets) > limit {
		targets = targets[:limit]
	}
	section := &imap.BodySectionName{Peek: true}
	attachments := []enterprise.MailAttachment{}
	lastUID := afterUID
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
			parsed := parseIMAPMessage(message, raw, folder)
			for _, attachment := range parsed.Attachments {
				attachments = append(attachments, enterprise.MailAttachment{
					Name: attachment.Name, MIMEType: attachment.MIMEType,
					ContentID: attachment.ContentID, Disposition: attachment.Disposition,
					Data: attachment.Data,
				})
			}
			_ = conn.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag}, nil)
		}
		if err := <-done; err == nil && int64(uid) > lastUID {
			lastUID = int64(uid)
		}
	}
	return attachments, lastUID, nil
}
