package httpapi

import (
	"context"
	"fmt"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"
)

const idleReconnectDelay = 5 * time.Second

type idleEvent int

const (
	idleEventNone idleEvent = iota
	idleEventNewMail
	idleEventFlags
)

// idleINBOXOnce keeps an INBOX IDLE connection open until the server reports an
// update. It returns active=false when the server does not support IDLE (the
// normal polling worker remains the fallback).
func (s *Server) idleINBOXOnce(ctx context.Context, user userRecord) (bool, idleEvent, error) {
	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return false, idleEventNone, err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return false, idleEventNone, fmt.Errorf("connect to IMAP for IDLE: %w", err)
	}
	defer conn.Logout()
	// IDLE stays open across command timeouts; go-imap restarts the command on
	// its own schedule and the worker reconnects after each event.
	conn.Timeout = 0
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return false, idleEventNone, err
	}
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return false, idleEventNone, fmt.Errorf("IMAP IDLE login: %w", err)
	}
	if supported, err := conn.Support("IDLE"); err != nil || !supported {
		return false, idleEventNone, nil
	}
	if _, err := conn.Select("INBOX", true); err != nil {
		return false, idleEventNone, fmt.Errorf("select INBOX for IDLE: %w", err)
	}
	updates := make(chan imapclient.Update, 16)
	conn.Updates = updates
	s.logger.Info("IMAP IDLE listening", "user", user.Email)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- conn.Idle(stop, &imapclient.IdleOptions{
			LogoutTimeout: 25 * time.Minute,
			PollInterval:  -1,
		})
	}()
	for {
		select {
		case <-ctx.Done():
			close(stop)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			return true, idleEventNone, ctx.Err()
		case err := <-done:
			return true, idleEventNone, err
		case update := <-updates:
			// Dovecot emits an untagged OK keepalive every two minutes while
			// idling. Treating that as mail activity would reconnect every
			// mailbox on the keepalive interval and defeat IDLE at scale.
			if ignoreIDLEUpdate(update) {
				continue
			}
			event := idleEventForUpdate(update)
			close(stop)
			select {
			case err := <-done:
				if err != nil {
					return false, event, err
				}
			case <-time.After(2 * time.Second):
			}
			return true, event, nil
		}
	}
}

// ignoreIDLEUpdate reports whether an IDLE update is a server keepalive/warning
// rather than real mailbox activity. Untagged OK/NO/BAD statuses (notably
// Dovecot's "* OK Still here") must not tear down and rebuild the connection.
func ignoreIDLEUpdate(update imapclient.Update) bool {
	status, ok := update.(*imapclient.StatusUpdate)
	return ok && status.Status != nil && status.Status.Type != imap.StatusRespBye
}

// idleEventForUpdate tells the worker whether it needs a full flag/deletion
// reconcile. EXISTS means new mail (the UID check covers it); expunge and FETCH
// updates mean existing rows changed. Unknown activity reconciles conservatively.
func idleEventForUpdate(update imapclient.Update) idleEvent {
	switch update.(type) {
	case *imapclient.MailboxUpdate:
		return idleEventNewMail
	case *imapclient.ExpungeUpdate, *imapclient.MessageUpdate:
		return idleEventFlags
	default:
		return idleEventFlags
	}
}

// refreshInboxFlagsForUser applies flags and expunges from INBOX without
// re-downloading message bodies. New mail is handled by checkNewInboxForUser;
// this pass keeps read/starred/delete changes made by other clients current.
func (s *Server) refreshInboxFlagsForUser(ctx context.Context, user userRecord) error {
	lock := s.userMailLock(user.ID)
	lock.Lock()
	defer lock.Unlock()

	tlsConfig, err := s.mailTLSConfig(s.cfg.IMAPTLSName)
	if err != nil {
		return err
	}
	conn, err := imapclient.DialTLS(s.cfg.IMAPAddr, tlsConfig)
	if err != nil {
		return fmt.Errorf("connect to IMAP for flag sync: %w", err)
	}
	defer conn.Logout()
	conn.Timeout = 30 * time.Second
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return err
	}
	if err := conn.Login(loginUser, loginPassword); err != nil {
		return fmt.Errorf("IMAP flag sync login: %w", err)
	}
	status, err := conn.Select("INBOX", true)
	if err != nil {
		return fmt.Errorf("select INBOX for flag sync: %w", err)
	}
	uids, err := imapUIDs(conn, 0)
	if err != nil {
		return fmt.Errorf("list INBOX UIDs for flag sync: %w", err)
	}
	known := s.loadFolderUIDs(ctx, user.ID, "inbox", int64(status.UidValidity))
	flagUIDs := make([]uint32, 0, len(known))
	for uid := range known {
		flagUIDs = append(flagUIDs, uid)
	}
	if len(flagUIDs) > maxFlagReconcilePerFolder {
		flagUIDs = flagUIDs[len(flagUIDs)-maxFlagReconcilePerFolder:]
	}
	updates, err := fetchIMAPFlagUpdates(conn, flagUIDs, "inbox", status.UidValidity)
	if err != nil {
		return fmt.Errorf("fetch INBOX flags: %w", err)
	}
	live := make([]int64, 0, len(uids))
	for _, uid := range uids {
		live = append(live, int64(uid))
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(updates) > 0 {
		batch := &pgx.Batch{}
		for _, update := range updates {
			unread := !hasIMAPFlag(update.Flags, `\Seen`)
			starred := hasIMAPFlag(update.Flags, `\Flagged`)
			batch.Queue(`
				UPDATE mail_messages SET unread=$4, starred=$5, flagged=$5, updated_at=now()
				WHERE user_id=$1 AND imap_folder='inbox' AND uid_validity=$2 AND uid=$3
			`, user.ID, int64(status.UidValidity), int64(update.UID), unread, starred)
		}
		results := tx.SendBatch(ctx, batch)
		for range updates {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return fmt.Errorf("apply INBOX flags: %w", err)
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
	}
	if len(live) == 0 {
		_, err = tx.Exec(ctx, `
			DELETE FROM mail_messages WHERE user_id=$1 AND imap_folder='inbox' AND uid > 0
		`, user.ID)
	} else {
		_, err = tx.Exec(ctx, `
			DELETE FROM mail_messages
			WHERE user_id=$1 AND imap_folder='inbox' AND uid > 0
			  AND (uid_validity <> $2 OR NOT (uid = ANY($3)))
		`, user.ID, int64(status.UidValidity), live)
	}
	if err != nil {
		return fmt.Errorf("reconcile INBOX deletions: %w", err)
	}
	return tx.Commit(ctx)
}
