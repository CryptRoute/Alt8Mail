package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const maxStickyNoteLinks = 12

type stickyNoteLinkRef struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	EventID  string `json:"eventId"`
	FileID   string `json:"fileId"`
}

// UnmarshalJSON accepts both the original array-of-thread-ID shape and the
// typed attachment shape used by the Notes desk.
func (r *stickyNoteLinkRef) UnmarshalJSON(data []byte) error {
	var rawID string
	if err := json.Unmarshal(data, &rawID); err == nil {
		r.Type = "thread"
		r.ID = rawID
		return nil
	}
	type plain stickyNoteLinkRef
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = stickyNoteLinkRef(value)
	return nil
}

func normalizeStickyNoteLinkRef(ref stickyNoteLinkRef) (string, string, bool) {
	typ := strings.ToLower(strings.TrimSpace(ref.Type))
	id := strings.TrimSpace(ref.ID)
	if typ == "" {
		switch {
		case strings.TrimSpace(ref.ThreadID) != "":
			typ = "thread"
		case strings.TrimSpace(ref.EventID) != "":
			typ = "event"
		case strings.TrimSpace(ref.FileID) != "":
			typ = "file"
		case id != "":
			typ = "thread"
		}
	}
	if id == "" {
		switch typ {
		case "thread":
			id = strings.TrimSpace(ref.ThreadID)
		case "event":
			id = strings.TrimSpace(ref.EventID)
		case "file":
			id = strings.TrimSpace(ref.FileID)
		}
	}
	switch typ {
	case "thread", "event", "file":
	default:
		return "", "", false
	}
	if len([]rune(id)) > 200 {
		return "", "", false
	}
	return typ, id, true
}

func normalizeStickyNoteLinks(raw []byte) []any {
	var stored []any
	if len(raw) == 0 || json.Unmarshal(raw, &stored) != nil {
		return []any{}
	}
	links := make([]any, 0, len(stored))
	for _, item := range stored {
		link, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(stateString(link, "type")))
		id := strings.TrimSpace(stateString(link, "id"))
		if typ == "" {
			switch {
			case strings.TrimSpace(stateString(link, "threadId")) != "":
				typ = "thread"
			case strings.TrimSpace(stateString(link, "eventId")) != "":
				typ = "event"
			case strings.TrimSpace(stateString(link, "fileId")) != "":
				typ = "file"
			case id != "":
				typ = "thread"
			}
		}
		if id == "" {
			switch typ {
			case "thread":
				id = stateString(link, "threadId")
			case "event":
				id = stateString(link, "eventId")
			case "file":
				id = stateString(link, "fileId")
			}
		}
		switch typ {
		case "thread", "event", "file":
		default:
			continue
		}
		if id == "" {
			continue
		}
		link["type"] = typ
		link["id"] = id
		if typ == "thread" && stateString(link, "threadId") == "" {
			link["threadId"] = id
		}
		links = append(links, link)
	}
	return links
}

func threadLinkRefs(ids []string) []stickyNoteLinkRef {
	refs := make([]stickyNoteLinkRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, stickyNoteLinkRef{Type: "thread", ID: id})
	}
	return refs
}

func (s *Server) buildStickyNoteLinks(ctx context.Context, owner userRecord, refs []stickyNoteLinkRef) ([]any, error) {
	links := []any{}
	seen := map[string]bool{}
	requested := 0
	for _, ref := range refs {
		typ, id, valid := normalizeStickyNoteLinkRef(ref)
		if !valid {
			return nil, fail(400, "STICKY_LINK_TYPE", "Choose a valid note attachment type")
		}
		if id == "" || seen[typ+":"+id] {
			continue
		}
		requested++
		if len(links) >= maxStickyNoteLinks {
			continue
		}
		key := typ + ":" + id
		switch typ {
		case "thread":
			var subject, fromName, fromEmail string
			var sentAt time.Time
			err := s.store.DB.QueryRow(ctx, `
				SELECT COALESCE(NULLIF(subject,''), '(no subject)'),
				       COALESCE(NULLIF(from_name,''), from_email::text), from_email::text, max(sent_at)
				FROM public.mail_messages
				WHERE user_id=$1 AND thread_id=$2
				GROUP BY subject, from_name, from_email
				ORDER BY max(sent_at) DESC LIMIT 1
			`, owner.ID, id).Scan(&subject, &fromName, &fromEmail, &sentAt)
			if err != nil {
				continue
			}
			links = append(links, map[string]any{
				"type": "thread", "id": id, "threadId": id,
				"subject": truncate(subject, 240), "fromName": truncate(fromName, 120),
				"fromEmail": truncate(fromEmail, 320), "date": sentAt.UTC().Format(time.RFC3339),
			})
		case "event":
			var title, location, calendarName, status string
			var startsAt, endsAt time.Time
			var allDay bool
			err := s.store.DB.QueryRow(ctx, `
				SELECT e.title, e.location, e.starts_at, e.ends_at, e.all_day, e.status, c.name
				FROM public.calendar_events e
				JOIN public.calendars c ON c.id=e.calendar_id AND c.user_id=e.user_id
				WHERE e.id=$1 AND (
				  e.user_id=$2
				  OR EXISTS (SELECT 1 FROM public.calendar_shares sh
				             WHERE sh.calendar_id=e.calendar_id AND sh.owner_user_id=e.user_id AND sh.grantee_user_id=$2)
				  OR EXISTS (SELECT 1 FROM public.calendar_attendees a
				             WHERE a.event_id=e.id AND lower(a.email)=lower($3))
				)
			`, id, owner.ID, owner.Email).Scan(&title, &location, &startsAt, &endsAt, &allDay, &status, &calendarName)
			if err != nil {
				continue
			}
			links = append(links, map[string]any{
				"type": "event", "id": id, "title": truncate(title, 240),
				"startAt": startsAt.UTC().Format(time.RFC3339), "endAt": endsAt.UTC().Format(time.RFC3339),
				"allDay": allDay, "location": truncate(location, 200),
				"calendarName": truncate(calendarName, 120), "status": status,
				"date": startsAt.UTC().Format("2006-01-02"),
			})
		case "file":
			var name, mime, folderID string
			var sizeBytes int64
			var updatedAt time.Time
			err := s.store.DB.QueryRow(ctx, `
				SELECT name, mime, size_bytes, COALESCE(folder_id,''), updated_at
				FROM public.files WHERE id=$1 AND user_id=$2
			`, id, owner.ID).Scan(&name, &mime, &sizeBytes, &folderID, &updatedAt)
			if err != nil {
				continue
			}
			links = append(links, map[string]any{
				"type": "file", "id": id, "name": truncate(name, 240), "mime": mime,
				"sizeBytes": sizeBytes, "size": formatBytes(sizeBytes), "folderId": folderID,
				"date": updatedAt.UTC().Format(time.RFC3339),
			})
		}
		seen[key] = true
	}
	if requested > 0 && len(links) == 0 {
		return nil, fail(400, "STICKY_LINK", "That attached item is no longer available")
	}
	return links, nil
}
