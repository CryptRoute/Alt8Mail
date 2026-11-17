package httpapi

import (
	"context"
	"net/http"
	"time"
)

type mailAccountUnreadSummary struct {
	UnreadCount  int
	LatestUnread map[string]any
}

// mailAccountUnreadSummaries returns the inbox unread count and newest unread
// message for every mailbox that has unread mail. The query mirrors the folder
// count rules: ignored, snoozed and closed-thread messages are excluded.
func (s *Server) mailAccountUnreadSummaries(ctx context.Context, userID string) (map[string]mailAccountUnreadSummary, error) {
	summaries := map[string]mailAccountUnreadSummary{}
	rows, err := s.store.DB.Query(ctx, `
		WITH unread AS (
			SELECT m.account_id, m.id, m.from_name, m.from_email::text AS from_email,
			       m.subject, m.sent_at
			FROM public.mail_messages m
			WHERE m.user_id=$1 AND m.account_id IS NOT NULL
			  AND m.folder='inbox' AND m.unread AND NOT m.draft AND NOT m.ignored
			  AND (m.snoozed_until IS NULL OR m.snoozed_until <= now())
			  AND NOT EXISTS (
				SELECT 1 FROM public.mail_thread_meta mt
				WHERE mt.owner_user_id=m.user_id AND mt.thread_id=m.thread_id
				  AND mt.closed_at IS NOT NULL
			  )
		), counts AS (
			SELECT account_id, count(*)::int AS unread_count
			FROM unread
			GROUP BY account_id
		), latest AS (
			SELECT DISTINCT ON (account_id) account_id, id, from_name, from_email, subject, sent_at
			FROM unread
			ORDER BY account_id, sent_at DESC
		)
		SELECT counts.account_id::text, counts.unread_count,
		       latest.id, latest.from_name, latest.from_email, latest.subject, latest.sent_at
		FROM counts
		JOIN latest USING (account_id)
	`, userID)
	if err != nil {
		return summaries, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, messageID, fromName, fromEmail, subject string
		var unreadCount int
		var sentAt time.Time
		if err := rows.Scan(&accountID, &unreadCount, &messageID, &fromName, &fromEmail, &subject, &sentAt); err != nil {
			return summaries, err
		}
		summaries[accountID] = mailAccountUnreadSummary{
			UnreadCount: unreadCount,
			LatestUnread: map[string]any{
				"id":        messageID,
				"fromName":  fromName,
				"fromEmail": fromEmail,
				"subject":   subject,
				"date":      sentAt.UTC().Format(time.RFC3339),
			},
		}
	}
	return summaries, rows.Err()
}

func (s *Server) getMailAccountSummaries(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	accounts, err := s.mailAccountsForSession(r.Context(), sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_ACCOUNTS", "Could not load mail accounts")
		return
	}
	summaries, err := s.mailAccountUnreadSummaries(r.Context(), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MAIL_UNREAD", "Could not load unread message counts")
		return
	}
	views := make([]map[string]any, 0, len(accounts))
	total := 0
	for _, account := range accounts {
		summary := summaries[account.ID]
		view := map[string]any{
			"id":          account.ID,
			"unreadCount": summary.UnreadCount,
			"status":      account.Status,
		}
		if account.LastError != "" {
			view["lastError"] = account.LastError
		}
		if summary.LatestUnread != nil {
			view["latestUnread"] = summary.LatestUnread
		}
		views = append(views, view)
		total += summary.UnreadCount
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":    views,
		"totalUnread": total,
		"updatedAt":   time.Now().UTC().Format(time.RFC3339),
	})
}
