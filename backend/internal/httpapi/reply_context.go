package httpapi

import (
	"net/http"
	"strings"
	"time"
)

// messageReplyContext warns when the sender already sent another message with
// the same subject in the last three days. The comparison ignores reply/forward
// prefixes and only considers separate conversations, so a normal back-and-forth
// in one thread does not trigger the reminder.
func (s *Server) messageReplyContext(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	lookupUser := sess.User
	messageID := r.PathValue("id")
	if ownerID, originalID, delegated := parseDelegatedMessageID(messageID); delegated {
		if _, allowed := s.delegationFor(r.Context(), sess.User.TenantID, ownerID, sess.User.ID); !allowed {
			writeError(w, http.StatusForbidden, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
			return
		}
		owner, err := s.userByID(r.Context(), ownerID)
		if err != nil || owner.TenantID != sess.User.TenantID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Delegated mailbox not found")
			return
		}
		lookupUser = owner
		messageID = originalID
	}
	var fromEmail, subject, threadID string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT from_email::text, subject, thread_id
		FROM mail_messages WHERE id=$1 AND user_id=$2
	`, messageID, lookupUser.ID).Scan(&fromEmail, &subject, &threadID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": nil})
		return
	}
	subjectKey := normalizedReplySubject(subject)
	if subjectKey == "" || strings.TrimSpace(fromEmail) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": nil})
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, thread_id, subject, sent_at, from_name, from_email::text, folder
		FROM mail_messages
		WHERE user_id=$1
		  AND id <> $2
		  AND thread_id <> $3
		  AND lower(from_email::text)=lower($4)
		  AND sent_at >= now() - interval '3 days'
		  AND folder NOT IN ('sent', 'drafts', 'spam', 'trash')
		ORDER BY sent_at DESC
		LIMIT 200
	`, lookupUser.ID, messageID, threadID, fromEmail)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": nil})
		return
	}
	defer rows.Close()
	var duplicate map[string]any
	count := 0
	for rows.Next() {
		var id, candidateThread, candidateSubject, senderName, senderEmail, folder string
		var sentAt time.Time
		if rows.Scan(&id, &candidateThread, &candidateSubject, &sentAt, &senderName, &senderEmail, &folder) != nil {
			continue
		}
		if normalizedReplySubject(candidateSubject) != subjectKey {
			continue
		}
		count++
		if duplicate == nil {
			duplicate = map[string]any{
				"id": id, "threadId": candidateThread, "subject": candidateSubject,
				"date":       sentAt.UTC().Format(time.RFC3339),
				"senderName": senderName, "senderEmail": senderEmail, "folder": folder,
			}
		}
	}
	if duplicate != nil {
		duplicate["count"] = count
	}
	writeJSON(w, http.StatusOK, map[string]any{"duplicate": duplicate})
}

// normalizedReplySubject removes reply/forward prefixes and compares without
// case so "Re: Project update" and "Project update" count as the same subject.
func normalizedReplySubject(subject string) string {
	value := strings.TrimSpace(subject)
	for {
		lower := strings.ToLower(value)
		matched := false
		for _, prefix := range []string{"re:", "fwd:", "fw:"} {
			if strings.HasPrefix(lower, prefix) {
				value = strings.TrimSpace(value[len(prefix):])
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	return strings.ToLower(value)
}
