package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

const importQuarantineMailbox = "Quarantine"

// scanImportedMessage scans a source message before it is appended to the
// destination mailbox. Infected messages are appended to Quarantine instead of
// their original folder; the application does not map that folder, so they are
// not exposed in webmail. Scanner transport failures abort the import item
// (fail closed) rather than silently importing unscanned mail.
func (s *Server) scanImportedMessage(ctx context.Context, target *imapclient.Client, raw []byte,
	flags []string, date time.Time, sourceEmail, messageID string) (bool, error) {
	if s == nil || !s.cfg.ImportScan || strings.TrimSpace(s.cfg.ClamAVAddr) == "" {
		return false, nil
	}
	limit := int64(s.cfg.ImportScanMaxMB)
	if limit <= 0 {
		limit = 24
	}
	if int64(len(raw)) > limit<<20 {
		// clamd's default StreamMaxLength is 25M. Do not fail an import because
		// a clean message is larger than the scanner's stream limit; the message
		// is still stored as inert data by Dovecot.
		if s.logger != nil {
			s.logger.Warn("mail import message exceeds antivirus scan limit; importing without scan",
				"source", sanitizeMailText(sourceEmail), "messageId", sanitizeMailText(messageID), "bytes", len(raw))
		}
		return false, nil
	}
	if date.IsZero() {
		date = time.Now()
	}
	if err := s.scanUpload(ctx, raw); err != nil {
		if !errors.Is(err, errUploadInfected) {
			return false, fmt.Errorf("scan imported message: %w", err)
		}
		if appendErr := target.Append(importQuarantineMailbox, importFlags(flags), date, bytes.NewReader(raw)); appendErr != nil {
			// CREATE returns "already exists" for an existing folder; try the
			// append once more after ensuring the mailbox exists.
			_ = target.Create(importQuarantineMailbox)
			if retryErr := target.Append(importQuarantineMailbox, importFlags(flags), date, bytes.NewReader(raw)); retryErr != nil {
				return false, fmt.Errorf("quarantine infected message: %w", retryErr)
			}
		}
		if s.logger != nil {
			s.logger.Warn("mail import quarantined infected message",
				"source", sanitizeMailText(sourceEmail), "messageId", sanitizeMailText(messageID))
		}
		return true, nil
	}
	return false, nil
}
