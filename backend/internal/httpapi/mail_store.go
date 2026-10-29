package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const mailMessageColumns = `id, folder, imap_folder, uid, uid_validity,
	message_id, thread_id, COALESCE(in_reply_to,''), references_json, subject,
	from_name, from_email::text, to_json, cc_json, bcc_json, to_text, cc_text, bcc_text,
	snippet, body_text, body_html, sent_at, received_at, created_at, updated_at,
	size_bytes, unread, starred, flagged, pinned, draft, external, classification, categories,
	auth, spam_report, event, priority, receipt, delivery, save_copy, via_api, window_mode`

type mailRow struct {
	ID             string
	Folder         string
	IMAPFolder     string
	UID            int64
	UIDValidity    int64
	MessageID      string
	ThreadID       string
	InReplyTo      string
	ReferencesJSON []byte
	Subject        string
	FromName       string
	FromEmail      string
	ToJSON         []byte
	CCJSON         []byte
	BCCJSON        []byte
	ToText         string
	CCText         string
	BCCText        string
	Snippet        string
	BodyText       string
	BodyHTML       string
	SentAt         time.Time
	ReceivedAt     time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	SizeBytes      int64
	Unread         bool
	Starred        bool
	Flagged        bool
	Pinned         bool
	Draft          bool
	External       bool
	Classification string
	Categories     []string
	AuthJSON       []byte
	SpamReportJSON []byte
	EventJSON      []byte
	Priority       string
	Receipt        bool
	Delivery       bool
	SaveCopy       bool
	ViaAPI         bool
	WindowMode     string
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMailRow(scanner rowScanner) (mailRow, error) {
	var row mailRow
	err := scanner.Scan(&row.ID, &row.Folder, &row.IMAPFolder, &row.UID, &row.UIDValidity,
		&row.MessageID, &row.ThreadID, &row.InReplyTo, &row.ReferencesJSON, &row.Subject,
		&row.FromName, &row.FromEmail, &row.ToJSON, &row.CCJSON, &row.BCCJSON,
		&row.ToText, &row.CCText, &row.BCCText, &row.Snippet, &row.BodyText, &row.BodyHTML,
		&row.SentAt, &row.ReceivedAt, &row.CreatedAt, &row.UpdatedAt, &row.SizeBytes,
		&row.Unread, &row.Starred, &row.Flagged, &row.Pinned, &row.Draft, &row.External,
		&row.Classification, &row.Categories, &row.AuthJSON, &row.SpamReportJSON,
		&row.EventJSON, &row.Priority, &row.Receipt, &row.Delivery, &row.SaveCopy,
		&row.ViaAPI, &row.WindowMode)
	return row, err
}

func decodeJSONAny(raw []byte, fallback any) any {
	if len(raw) == 0 {
		return fallback
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

func decodePeople(raw []byte, state map[string]any) []any {
	value := decodeJSONAny(raw, []any{})
	list, ok := value.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(list))
	for _, rawPerson := range list {
		person, _ := rawPerson.(map[string]any)
		if person == nil {
			continue
		}
		email := stateString(person, "email")
		name := stateString(person, "name")
		normalized := personFor(state, email, name)
		if normalized == nil {
			normalized = person
		}
		out = append(out, normalized)
	}
	return out
}

func peopleText(people []any) string {
	parts := make([]string, 0, len(people))
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		if person == nil {
			continue
		}
		name := stateString(person, "name")
		email := stateString(person, "email")
		if name != "" && name != email {
			parts = append(parts, name+" "+email)
		} else if email != "" {
			parts = append(parts, email)
		}
	}
	return strings.Join(parts, " ")
}

func (row mailRow) stateMap(state map[string]any, attachments []map[string]any) map[string]any {
	to := decodePeople(row.ToJSON, state)
	cc := decodePeople(row.CCJSON, state)
	bcc := decodePeople(row.BCCJSON, state)
	from := personFor(state, row.FromEmail, row.FromName)
	if from == nil {
		from = map[string]any{"name": row.FromEmail, "email": row.FromEmail, "initials": "?", "color": "#64748b"}
	}
	auth := decodeJSONAny(row.AuthJSON, map[string]any{})
	if _, ok := auth.(map[string]any); !ok {
		auth = map[string]any{}
	}
	attachmentList := make([]any, 0, len(attachments))
	for _, attachment := range attachments {
		attachmentList = append(attachmentList, attachment)
	}
	out := map[string]any{
		"id": row.ID, "threadId": row.ThreadID, "folder": row.Folder,
		"from": from, "fromEmail": row.FromEmail, "to": to, "cc": cc, "bcc": bcc,
		"toText": row.ToText, "ccText": row.CCText, "bccText": row.BCCText,
		"subject": row.Subject, "snippet": row.Snippet,
		"bodyText": row.BodyText, "bodyHtml": row.BodyHTML,
		"date": row.SentAt.UTC().Format(time.RFC3339), "unread": row.Unread,
		"starred": row.Starred, "flagged": row.Flagged, "pinned": row.Pinned, "draft": row.Draft,
		"attachments": attachmentList, "categories": stringSliceToAny(row.Categories),
		"classification": row.Classification, "external": row.External, "auth": auth,
		"event": decodeJSONAny(row.EventJSON, nil), "sizeBytes": row.SizeBytes,
		"messageId": row.MessageID, "inReplyTo": nullableString(row.InReplyTo),
		"priority": row.Priority, "receipt": row.Receipt, "delivery": row.Delivery,
		"saveCopy": row.SaveCopy, "viaApi": row.ViaAPI, "windowMode": row.WindowMode,
		"imapUid": row.UID, "imapFolder": row.IMAPFolder, "imapUidValidity": row.UIDValidity,
		"createdAt": row.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": row.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if row.InReplyTo != "" {
		out["inReplyTo"] = row.InReplyTo
	}
	references := []any{}
	_ = json.Unmarshal(row.ReferencesJSON, &references)
	if len(references) > 0 {
		out["references"] = references
	}
	if spamReport := decodeJSONAny(row.SpamReportJSON, nil); spamReport != nil {
		out["spamReport"] = spamReport
	}
	return out
}

func stringSliceToAny(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func mailAttachmentPublic(record map[string]any) map[string]any {
	id := stateString(record, "id")
	return map[string]any{
		"id": id, "name": stateString(record, "name"), "type": stateString(record, "type"),
		"mime": stateString(record, "mime"), "size": stateString(record, "size"),
		"sizeBytes": stateInt(record, "sizeBytes"), "previewable": stateBool(record, "previewable"),
		"contentUrl":  "/api/attachments/" + id + "/content",
		"downloadUrl": "/api/attachments/" + id + "/content",
	}
}

func mailAttachmentStateRecord(attachmentID, name, mime, storedPath string, sizeBytes int64, previewable bool) map[string]any {
	return map[string]any{
		"id": attachmentID, "name": name, "mime": mime,
		"size": formatBytes(sizeBytes), "sizeBytes": sizeBytes,
		"storedName": storedPath, "previewable": previewable,
		"type": attachmentBadgeType(mime, name),
	}
}

func (s *Server) loadAttachmentRows(ctx context.Context, userID string, messageIDs []string) (map[string][]map[string]any, map[string]any, error) {
	byMessage := map[string][]map[string]any{}
	stateRecords := map[string]any{}
	if len(messageIDs) == 0 {
		return byMessage, stateRecords, nil
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT ma.message_id, a.id, a.name, a.mime, a.size_bytes, a.stored_path, a.previewable
		FROM mail_message_attachments ma
		JOIN attachments a ON a.id = ma.attachment_id AND a.user_id = $1
		WHERE ma.message_id = ANY($2)
		ORDER BY ma.message_id, ma.position
	`, userID, messageIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var messageID, id, name, mime, storedPath string
		var sizeBytes int64
		var previewable bool
		if err := rows.Scan(&messageID, &id, &name, &mime, &sizeBytes, &storedPath, &previewable); err != nil {
			continue
		}
		record := mailAttachmentStateRecord(id, name, mime, storedPath, sizeBytes, previewable)
		byMessage[messageID] = append(byMessage[messageID], mailAttachmentPublic(record))
		stateRecords[id] = record
	}
	return byMessage, stateRecords, rows.Err()
}

func (s *Server) loadMailMessagesQuery(ctx context.Context, userID, query string, args ...any) ([]map[string]any, map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	records := []mailRow{}
	ids := []string{}
	for rows.Next() {
		row, err := scanMailRow(rows)
		if err != nil {
			return nil, nil, err
		}
		records = append(records, row)
		ids = append(ids, row.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	byMessage, attachmentRecords, err := s.loadAttachmentRows(ctx, userID, ids)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]map[string]any, 0, len(records))
	for _, row := range records {
		messages = append(messages, row.stateMap(nil, byMessage[row.ID]))
	}
	return messages, attachmentRecords, nil
}

func (s *Server) loadAllMailMessages(ctx context.Context, userID string) ([]map[string]any, map[string]any, error) {
	return s.loadMailMessagesQuery(ctx, userID,
		`SELECT `+mailMessageColumns+` FROM mail_messages WHERE user_id=$1 ORDER BY sent_at ASC`, userID)
}

func (s *Server) loadMailMessagesByIDs(ctx context.Context, userID string, ids []string) ([]map[string]any, map[string]any, error) {
	if len(ids) == 0 {
		return []map[string]any{}, map[string]any{}, nil
	}
	return s.loadMailMessagesQuery(ctx, userID,
		`SELECT `+mailMessageColumns+` FROM mail_messages WHERE user_id=$1 AND id = ANY($2) ORDER BY sent_at ASC`, userID, ids)
}

func (s *Server) loadMailMessagesByFolder(ctx context.Context, userID, folder, filter string) ([]map[string]any, map[string]any, error) {
	query := `SELECT ` + mailMessageColumns + ` FROM mail_messages WHERE user_id=$1`
	args := []any{userID}
	if folder == "starred" {
		query += ` AND (starred OR flagged)`
	} else if folder != "" && folder != "all" {
		args = append(args, folder)
		query += fmt.Sprintf(` AND folder = $%d`, len(args))
	}
	switch filter {
	case "unread":
		query += ` AND unread`
	case "starred":
		query += ` AND (starred OR flagged)`
	case "attachments":
		query += ` AND EXISTS (SELECT 1 FROM mail_message_attachments ma WHERE ma.message_id = mail_messages.id)`
	}
	query += ` ORDER BY sent_at DESC`
	return s.loadMailMessagesQuery(ctx, userID, query, args...)
}

func (s *Server) loadMailMessagesByThreads(ctx context.Context, userID string, threadIDs []string) ([]map[string]any, map[string]any, error) {
	if len(threadIDs) == 0 {
		return []map[string]any{}, map[string]any{}, nil
	}
	return s.loadMailMessagesQuery(ctx, userID,
		`SELECT `+mailMessageColumns+` FROM mail_messages WHERE user_id=$1 AND thread_id = ANY($2) ORDER BY sent_at ASC`, userID, threadIDs)
}

func (s *Server) loadFolderUIDs(ctx context.Context, userID, folder string, uidValidity int64) map[uint32]bool {
	known := map[uint32]bool{}
	rows, err := s.store.DB.Query(ctx, `
		SELECT uid FROM mail_messages
		WHERE user_id=$1 AND imap_folder=$2 AND uid_validity=$3 AND uid > 0 AND uid <= 4294967295
	`, userID, folder, uidValidity)
	if err != nil {
		return known
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		if rows.Scan(&uid) == nil {
			known[uint32(uid)] = true
		}
	}
	return known
}

func (s *Server) countMailMessages(ctx context.Context, userID, folder string, uidValidity int64) int {
	var count int
	_ = s.store.DB.QueryRow(ctx, `
		SELECT count(*) FROM mail_messages
		WHERE user_id=$1 AND imap_folder=$2 AND uid_validity=$3 AND uid > 0
	`, userID, folder, uidValidity).Scan(&count)
	return count
}

func (s *Server) loadAttachmentsByIDs(ctx context.Context, userID string, ids []string) (map[string]any, error) {
	result := map[string]any{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, name, mime, size_bytes, stored_path, previewable
		FROM attachments WHERE user_id=$1 AND id = ANY($2)
	`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, mime, storedPath string
		var sizeBytes int64
		var previewable bool
		if rows.Scan(&id, &name, &mime, &sizeBytes, &storedPath, &previewable) == nil {
			result[id] = mailAttachmentStateRecord(id, name, mime, storedPath, sizeBytes, previewable)
		}
	}
	return result, rows.Err()
}

func attachmentIDsFromValue(value any) []string {
	items, _ := value.([]any)
	ids := []string{}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if id := stateString(item, "id"); strings.HasPrefix(id, "att_") {
			ids = append(ids, id)
		}
	}
	return uniqueStrings(ids, 50)
}

func (s *Server) preloadAttachments(ctx context.Context, userID string, state map[string]any, ids []string) {
	loaded, err := s.loadAttachmentsByIDs(ctx, userID, ids)
	if err != nil {
		return
	}
	known, _ := state["attachments"].(map[string]any)
	if known == nil {
		known = map[string]any{}
	}
	for id, record := range loaded {
		known[id] = record
	}
	state["attachments"] = known
}

func (s *Server) loadOutboxRows(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, draft_id, from_json, to_json, cc_json, bcc_json, subject, body_text, body_html, attachments,
		       thread_id, COALESCE(in_reply_to,''), priority, receipt, delivery, save_copy, scheduled,
		       send_at, queued_at, hold_ms, status, delivery_state, delivery_claimed_at, retry_at,
		       attempts, last_error, mime_message_id, draft_snapshot, ip, user_agent
		FROM outbox WHERE user_id=$1 ORDER BY send_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, draftID, threadID, inReplyTo, priority, status, deliveryState, lastError, mimeMessageID, ip, userAgent string
		var subject, bodyText, bodyHTML string
		var fromJSON, toJSON, ccJSON, bccJSON, attachmentsJSON, draftSnapshotJSON []byte
		var receipt, delivery, saveCopy, scheduled bool
		var sendAt, queuedAt time.Time
		var claimedAt, retryAt *time.Time
		var holdMs, attempts int64
		if err := rows.Scan(&id, &draftID, &fromJSON, &toJSON, &ccJSON, &bccJSON, &subject, &bodyText, &bodyHTML,
			&attachmentsJSON, &threadID, &inReplyTo, &priority, &receipt, &delivery, &saveCopy,
			&scheduled, &sendAt, &queuedAt, &holdMs, &status, &deliveryState, &claimedAt, &retryAt,
			&attempts, &lastError, &mimeMessageID, &draftSnapshotJSON, &ip, &userAgent); err != nil {
			continue
		}
		item := map[string]any{
			"id": id, "draftId": draftID, "from": decodeJSONAny(fromJSON, map[string]any{}),
			"to": decodeJSONAny(toJSON, []any{}), "cc": decodeJSONAny(ccJSON, []any{}),
			"bcc": decodeJSONAny(bccJSON, []any{}), "subject": subject,
			"bodyText": bodyText, "bodyHtml": bodyHTML,
			"attachments": decodeJSONAny(attachmentsJSON, []any{}), "threadId": threadID,
			"priority": priority, "receipt": receipt, "delivery": delivery, "saveCopy": saveCopy,
			"scheduled": scheduled, "sendAt": sendAt.UTC().Format(time.RFC3339),
			"queuedAt": queuedAt.UTC().Format(time.RFC3339), "holdMs": holdMs,
			"status": status, "deliveryState": deliveryState, "attempts": attempts,
			"lastError": lastError, "mimeMessageId": mimeMessageID, "ip": ip, "userAgent": userAgent,
		}
		if inReplyTo != "" {
			item["inReplyTo"] = inReplyTo
		}
		if claimedAt != nil {
			item["deliveryClaimedAt"] = claimedAt.UTC().Format(time.RFC3339)
		}
		if retryAt != nil {
			item["retryAt"] = retryAt.UTC().Format(time.RFC3339)
		}
		if snapshot := decodeJSONAny(draftSnapshotJSON, nil); snapshot != nil {
			item["draftSnapshot"] = snapshot
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Server) attachOutbox(ctx context.Context, userID string, state map[string]any) {
	items, err := s.loadOutboxRows(ctx, userID)
	if err != nil {
		return
	}
	outbox := make([]any, 0, len(items))
	for _, item := range items {
		outbox = append(outbox, item)
	}
	state["outbox"] = outbox
}

func (s *Server) loadStateWithOutbox(ctx context.Context, userID string) (map[string]any, bool) {
	state, ok := s.loadStateFor(ctx, userID)
	if !ok {
		return state, false
	}
	s.attachOutbox(ctx, userID, state)
	return state, true
}

func copyMailMapList(messages []any) map[string]map[string]any {
	result := map[string]map[string]any{}
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		id := stateString(message, "id")
		if id != "" {
			result[id] = deepCopyMap(message)
		}
	}
	return result
}

func copyAttachmentMap(state map[string]any) map[string]map[string]any {
	result := map[string]map[string]any{}
	known, _ := state["attachments"].(map[string]any)
	for id, raw := range known {
		record, _ := raw.(map[string]any)
		if record != nil {
			result[id] = deepCopyMap(record)
		}
	}
	return result
}

func (s *Server) withMailState(ctx context.Context, user userRecord, ids []string, loadAll bool, fn func(map[string]any) error) error {
	return s.withStateTimeout(user.ID, user.TenantID, 120*time.Second, func(state map[string]any) error {
		ensureStateCollections(state)
		if err := s.syncStateAliases(ctx, user, state); err != nil {
			return fmt.Errorf("load sending identities: %w", err)
		}
		var messages []map[string]any
		var attachmentRecords map[string]any
		var err error
		if loadAll {
			messages, attachmentRecords, err = s.loadAllMailMessages(ctx, user.ID)
		} else {
			messages, attachmentRecords, err = s.loadMailMessagesByIDs(ctx, user.ID, ids)
		}
		if err != nil {
			return err
		}
		outbox, err := s.loadOutboxRows(ctx, user.ID)
		if err != nil {
			return err
		}
		stateMessagesOut := make([]any, 0, len(messages))
		for _, message := range messages {
			stateMessagesOut = append(stateMessagesOut, message)
		}
		outboxAny := make([]any, 0, len(outbox))
		for _, item := range outbox {
			outboxAny = append(outboxAny, item)
		}
		state["messages"] = stateMessagesOut
		state["attachments"] = attachmentRecords
		state["outbox"] = outboxAny
		beforeMessages := copyMailMapList(stateMessagesOut)
		beforeAttachments := copyAttachmentMap(state)
		beforeOutbox := copyMailMapList(outboxAny)
		if err := fn(state); err != nil {
			return err
		}
		return s.persistMailStateDelta(ctx, user, state, beforeMessages, beforeAttachments, beforeOutbox)
	})
}

func (s *Server) persistMailStateDelta(ctx context.Context, user userRecord, state map[string]any,
	beforeMessages map[string]map[string]any, beforeAttachments map[string]map[string]any,
	beforeOutbox map[string]map[string]any) error {
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// New attachment bytes/metadata (for example an IMAP import) are inserted
	// before message rows so the link inserts can resolve them.
	known, _ := state["attachments"].(map[string]any)
	for id, raw := range known {
		if _, existed := beforeAttachments[id]; existed {
			continue
		}
		record, _ := raw.(map[string]any)
		if record == nil {
			continue
		}
		storedName := stateString(record, "storedName")
		if storedName == "" {
			storedName = stateString(record, "storedPath")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (id) DO NOTHING
		`, id, user.TenantID, user.ID, truncate(firstNonEmpty(stateString(record, "name"), "attachment"), 240),
			firstNonEmpty(stateString(record, "mime"), "application/octet-stream"),
			stateInt(record, "sizeBytes"), stateString(record, "checksum"), storedName,
			stateBool(record, "previewable"))
		if err != nil {
			return err
		}
	}

	after := map[string]map[string]any{}
	for _, raw := range stateMessages(state) {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		id := stateString(message, "id")
		if id == "" {
			continue
		}
		after[id] = message
		previous, existed := beforeMessages[id]
		if existed && reflect.DeepEqual(previous, message) {
			continue
		}
		if err := s.persistMailMessageTx(ctx, tx, user, message); err != nil {
			return err
		}
	}
	for id := range beforeMessages {
		if _, exists := after[id]; !exists {
			if _, err := tx.Exec(ctx, `DELETE FROM mail_messages WHERE id=$1 AND user_id=$2`, id, user.ID); err != nil {
				return err
			}
		}
	}
	afterOutbox := map[string]map[string]any{}
	for _, raw := range stateSlice(state, "outbox") {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		id := stateString(item, "id")
		if id == "" {
			continue
		}
		afterOutbox[id] = item
		previous, existed := beforeOutbox[id]
		if existed && reflect.DeepEqual(previous, item) {
			continue
		}
		if err := s.persistOutboxItemTx(ctx, tx, user, item); err != nil {
			return err
		}
	}
	for id := range beforeOutbox {
		if _, exists := afterOutbox[id]; !exists {
			if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE id=$1 AND user_id=$2`, id, user.ID); err != nil {
				return err
			}
		}
	}
	// The per-user document must never retain mail, attachment or outbox data.
	// These maps existed only to let the existing handlers mutate normalized
	// rows in place; the tables are the sole source of truth once the
	// transaction commits.
	delete(state, "messages")
	delete(state, "attachments")
	delete(state, "outbox")
	return tx.Commit(ctx)
}

func parseOptionalTime(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	stamp, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return stamp
}

func (s *Server) persistOutboxItemTx(ctx context.Context, tx pgx.Tx, user userRecord, item map[string]any) error {
	id := stateString(item, "id")
	if id == "" {
		return fmt.Errorf("outbox item has no id")
	}
	from, _ := item["from"].(map[string]any)
	fromJSON, _ := json.Marshal(from)
	to := stateSlice(item, "to")
	cc := stateSlice(item, "cc")
	bcc := stateSlice(item, "bcc")
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	bccJSON, _ := json.Marshal(bcc)
	attachments := stateSlice(item, "attachments")
	attachmentsJSON, _ := json.Marshal(attachments)
	draftSnapshotJSON := []byte(nil)
	if snapshot, ok := item["draftSnapshot"].(map[string]any); ok && snapshot != nil {
		draftSnapshotJSON, _ = json.Marshal(snapshot)
	}
	sendAt, err := time.Parse(time.RFC3339, stateString(item, "sendAt"))
	if err != nil {
		sendAt = time.Now().UTC()
	}
	queuedAt, err := time.Parse(time.RFC3339, stateString(item, "queuedAt"))
	if err != nil {
		queuedAt = sendAt
	}
	status := firstNonEmpty(stateString(item, "status"), "queued")
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (
			id, tenant_id, user_id, draft_id, from_json, to_json, cc_json, bcc_json, subject,
			body_text, body_html, attachments, thread_id, in_reply_to, priority, receipt, delivery,
			save_copy, scheduled, send_at, queued_at, hold_ms, status, delivery_state, delivery_claimed_at,
			retry_at, attempts, last_error, mime_message_id, draft_snapshot, ip, user_agent,
			created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,now()
		)
		ON CONFLICT (id) DO UPDATE SET
			draft_id=EXCLUDED.draft_id, from_json=EXCLUDED.from_json, to_json=EXCLUDED.to_json,
			cc_json=EXCLUDED.cc_json, bcc_json=EXCLUDED.bcc_json, subject=EXCLUDED.subject,
			body_text=EXCLUDED.body_text, body_html=EXCLUDED.body_html,
			attachments=EXCLUDED.attachments, thread_id=EXCLUDED.thread_id,
			in_reply_to=EXCLUDED.in_reply_to, priority=EXCLUDED.priority, receipt=EXCLUDED.receipt,
			delivery=EXCLUDED.delivery, save_copy=EXCLUDED.save_copy, scheduled=EXCLUDED.scheduled,
			send_at=EXCLUDED.send_at, queued_at=EXCLUDED.queued_at, hold_ms=EXCLUDED.hold_ms,
			status=EXCLUDED.status, delivery_state=EXCLUDED.delivery_state,
			delivery_claimed_at=EXCLUDED.delivery_claimed_at, retry_at=EXCLUDED.retry_at,
			attempts=EXCLUDED.attempts, last_error=EXCLUDED.last_error,
			mime_message_id=EXCLUDED.mime_message_id, draft_snapshot=EXCLUDED.draft_snapshot,
			ip=EXCLUDED.ip, user_agent=EXCLUDED.user_agent, updated_at=now()
	`, id, user.TenantID, user.ID, stateString(item, "draftId"), fromJSON, toJSON, ccJSON, bccJSON,
		truncate(stateString(item, "subject"), 500), stateString(item, "bodyText"),
		stateString(item, "bodyHtml"), attachmentsJSON,
		stateString(item, "threadId"), stateString(item, "inReplyTo"),
		firstNonEmpty(stateString(item, "priority"), "normal"), stateBool(item, "receipt"),
		stateBool(item, "delivery"), item["saveCopy"] == nil || stateBool(item, "saveCopy"),
		stateBool(item, "scheduled"), sendAt, queuedAt, stateInt(item, "holdMs"), status,
		stateString(item, "deliveryState"), parseOptionalTime(stateString(item, "deliveryClaimedAt")),
		parseOptionalTime(stateString(item, "retryAt")), stateInt(item, "attempts"),
		truncate(stateString(item, "lastError"), 500), stateString(item, "mimeMessageId"),
		draftSnapshotJSON, truncate(stateString(item, "ip"), 64),
		truncate(stateString(item, "userAgent"), 300), queuedAt)
	return err
}

func (s *Server) persistMailMessageTx(ctx context.Context, tx pgx.Tx, user userRecord, message map[string]any) error {
	id := stateString(message, "id")
	if id == "" {
		return fmt.Errorf("message has no id")
	}
	from, _ := message["from"].(map[string]any)
	fromEmail := firstNonEmpty(stateString(message, "fromEmail"), stateString(from, "email"))
	fromName := stateString(from, "name")
	to := stateSlice(message, "to")
	cc := stateSlice(message, "cc")
	bcc := stateSlice(message, "bcc")
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	bccJSON, _ := json.Marshal(bcc)
	auth, _ := message["auth"].(map[string]any)
	authJSON, _ := json.Marshal(auth)
	spamJSON := []byte(nil)
	if report, ok := message["spamReport"].(map[string]any); ok && report != nil {
		spamJSON, _ = json.Marshal(report)
	}
	eventJSON := []byte(nil)
	if event, ok := message["event"].(map[string]any); ok && event != nil {
		eventJSON, _ = json.Marshal(event)
	}
	references := []any{}
	if raw, ok := message["references"].([]any); ok {
		references = raw
	}
	referencesJSON, _ := json.Marshal(references)
	categories := []string{}
	for _, raw := range stateSlice(message, "categories") {
		if value, ok := raw.(string); ok && value != "" {
			categories = append(categories, value)
		}
	}
	sentAt, err := time.Parse(time.RFC3339, stateString(message, "date"))
	if err != nil {
		sentAt = time.Now().UTC()
	}
	createdAt, err := time.Parse(time.RFC3339, stateString(message, "createdAt"))
	if err != nil {
		createdAt = sentAt
	}
	updatedAt, err := time.Parse(time.RFC3339, stateString(message, "updatedAt"))
	if err != nil {
		updatedAt = time.Now().UTC()
	}
	threadID := firstNonEmpty(stateString(message, "threadId"), id)
	windowMode := firstNonEmpty(stateString(message, "windowMode"), "floating")
	priority := firstNonEmpty(stateString(message, "priority"), "normal")
	_, err = tx.Exec(ctx, `
		INSERT INTO mail_messages (
			id, tenant_id, user_id, mailbox_id, folder, imap_folder, uid, uid_validity,
			message_id, thread_id, in_reply_to, references_json, subject, from_name, from_email,
			to_json, cc_json, bcc_json, to_text, cc_text, bcc_text, snippet, body_text, body_html,
			sent_at, received_at, created_at, updated_at, size_bytes, unread, starred, flagged,
			pinned, draft, external, classification, categories, auth, spam_report, event, priority,
			receipt, delivery, save_copy, via_api, window_mode
		) VALUES (
			$1,$2,$3,(SELECT id FROM mailboxes WHERE user_id=$3 LIMIT 1),$4,$5,$6,$7,
			$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,
			$24,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44
		)
		ON CONFLICT (id) DO UPDATE SET
			folder=EXCLUDED.folder, imap_folder=EXCLUDED.imap_folder, uid=EXCLUDED.uid,
			uid_validity=EXCLUDED.uid_validity, message_id=EXCLUDED.message_id,
			thread_id=EXCLUDED.thread_id, in_reply_to=EXCLUDED.in_reply_to,
			references_json=EXCLUDED.references_json, subject=EXCLUDED.subject,
			from_name=EXCLUDED.from_name, from_email=EXCLUDED.from_email,
			to_json=EXCLUDED.to_json, cc_json=EXCLUDED.cc_json, bcc_json=EXCLUDED.bcc_json,
			to_text=EXCLUDED.to_text, cc_text=EXCLUDED.cc_text, bcc_text=EXCLUDED.bcc_text,
			snippet=EXCLUDED.snippet, body_text=EXCLUDED.body_text, body_html=EXCLUDED.body_html,
			sent_at=EXCLUDED.sent_at, received_at=EXCLUDED.received_at, updated_at=EXCLUDED.updated_at,
			size_bytes=EXCLUDED.size_bytes, unread=EXCLUDED.unread, starred=EXCLUDED.starred,
			flagged=EXCLUDED.flagged, pinned=EXCLUDED.pinned, draft=EXCLUDED.draft, external=EXCLUDED.external,
			classification=EXCLUDED.classification, categories=EXCLUDED.categories,
			auth=EXCLUDED.auth, spam_report=EXCLUDED.spam_report, event=EXCLUDED.event,
			priority=EXCLUDED.priority, receipt=EXCLUDED.receipt, delivery=EXCLUDED.delivery,
			save_copy=EXCLUDED.save_copy, via_api=EXCLUDED.via_api, window_mode=EXCLUDED.window_mode
	`, id, user.TenantID, user.ID, firstNonEmpty(stateString(message, "folder"), "inbox"),
		firstNonEmpty(stateString(message, "imapFolder"), stateString(message, "folder"), "inbox"),
		stateInt(message, "imapUid"), stateInt(message, "imapUidValidity"),
		stateString(message, "messageId"), threadID, stateString(message, "inReplyTo"),
		referencesJSON, truncate(stateString(message, "subject"), 1000), truncate(fromName, 240), fromEmail,
		toJSON, ccJSON, bccJSON, truncate(firstNonEmpty(stateString(message, "toText"), peopleText(to)), 4000),
		truncate(firstNonEmpty(stateString(message, "ccText"), peopleText(cc)), 4000),
		truncate(firstNonEmpty(stateString(message, "bccText"), peopleText(bcc)), 4000),
		truncate(stateString(message, "snippet"), 500), stateString(message, "bodyText"),
		stateString(message, "bodyHtml"), sentAt, createdAt, updatedAt, stateInt(message, "sizeBytes"),
		stateBool(message, "unread"), stateBool(message, "starred"), stateBool(message, "flagged"),
		stateBool(message, "pinned"), stateBool(message, "draft"), stateBool(message, "external"),
		firstNonEmpty(stateString(message, "classification"), "Internal"), categories, authJSON,
		spamJSON, eventJSON, priority, stateBool(message, "receipt"), stateBool(message, "delivery"),
		message["saveCopy"] == nil || stateBool(message, "saveCopy"), stateBool(message, "viaApi"), windowMode)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mail_message_attachments WHERE message_id=$1`, id); err != nil {
		return err
	}
	position := 0
	for _, raw := range stateSlice(message, "attachments") {
		attachment, _ := raw.(map[string]any)
		if attachment == nil {
			continue
		}
		attachmentID := stateString(attachment, "id")
		if attachmentID == "" {
			continue
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO mail_message_attachments (message_id, attachment_id, position, content_id, disposition)
			SELECT $1, $2, $3, $4, $5 FROM attachments
			WHERE id=$2 AND user_id=$6
			ON CONFLICT (message_id, attachment_id) DO NOTHING
		`, id, attachmentID, position, stateString(attachment, "contentId"),
			stateString(attachment, "disposition"), user.ID)
		if err != nil {
			return err
		}
		position++
	}
	return nil
}

func (s *Server) mailQuota(ctx context.Context, userID string, state map[string]any) map[string]any {
	user := stateMap(state, "user")
	quota := stateMap(user, "quota")
	limit := stateInt(quota, "limitBytes")
	if limit <= 0 {
		limit = 16 * 1024 * 1024 * 1024
	}
	baseline := stateInt(quota, "baselineUsedBytes")
	perFolder := map[string]int64{}
	rows, err := s.store.DB.Query(ctx, `
		SELECT folder, COALESCE(sum(size_bytes),0)::bigint
		FROM mail_messages WHERE user_id=$1 GROUP BY folder
	`, userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var folder string
			var bytes int64
			if rows.Scan(&folder, &bytes) == nil {
				perFolder[folder] = bytes
			}
		}
	}
	dynamic := int64(0)
	for _, bytes := range perFolder {
		dynamic += bytes
	}
	used := baseline + dynamic
	if used > limit {
		used = limit
	}
	folders := []any{map[string]any{"id": "_baseline", "label": "Provider baseline", "bytes": baseline}}
	for _, raw := range stateSlice(state, "folders") {
		folder, _ := raw.(map[string]any)
		if folder == nil || stateBool(folder, "virtual") {
			continue
		}
		id := stateString(folder, "id")
		folders = append(folders, map[string]any{"id": id, "label": stateString(folder, "label"), "bytes": perFolder[id]})
	}
	percentage := 0.0
	if limit > 0 {
		percentage = roundOneDecimal(float64(used) / float64(limit) * 100)
	}
	return map[string]any{
		"source": "dovecot", "limitBytes": limit, "usedBytes": used,
		"dynamicBytes": dynamic, "percent": percentage,
		"label":   fmt.Sprintf("%s of %s", formatBytes(used), formatBytes(limit)),
		"folders": folders, "refreshedAt": nowISO(),
	}
}

func roundOneDecimal(value float64) float64 {
	return float64(int64(value*10+0.5)) / 10
}

type mailSearchQuery struct {
	Filters map[string][]string
	Free    []string
}

func parseMailSearch(raw string) mailSearchQuery {
	query := strings.TrimSpace(raw)
	parsed := mailSearchQuery{Filters: map[string][]string{}, Free: []string{}}
	remaining := searchOperatorPattern.ReplaceAllStringFunc(query, func(match string) string {
		parts := searchOperatorPattern.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		operator := strings.ToLower(parts[1])
		value := strings.Trim(parts[2], `"`)
		switch operator {
		case "from", "to", "cc", "subject", "body", "filename", "attachment", "has", "before", "after", "is", "in", "larger", "smaller":
			parsed.Filters[operator] = append(parsed.Filters[operator], strings.ToLower(value))
			return ""
		}
		return match
	})
	for _, term := range strings.Fields(remaining) {
		term = strings.Trim(term, `"`)
		if term != "" {
			parsed.Free = append(parsed.Free, strings.ToLower(term))
		}
	}
	return parsed
}

func mailSearchParsed(raw string, parsed mailSearchQuery) map[string]any {
	out := map[string]any{"raw": raw, "textTerms": parsed.Free}
	for key, values := range parsed.Filters {
		out[key] = values
	}
	return out
}

func likePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "%", `\%`)
	value = strings.ReplaceAll(value, "_", `\_`)
	return "%" + value + "%"
}

func (s *Server) searchMailMessages(ctx context.Context, userID, raw string) ([]map[string]any, mailSearchQuery, error) {
	parsed := parseMailSearch(raw)
	conditions := []string{"user_id=$1"}
	args := []any{userID}
	appendClause := func(format string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(format, len(args)))
	}
	for _, value := range parsed.Filters["from"] {
		args = append(args, likePattern(value))
		index := len(args)
		conditions = append(conditions, fmt.Sprintf(
			"(from_name ILIKE $%d ESCAPE '\\' OR from_email::text ILIKE $%d ESCAPE '\\')", index, index))
	}
	for _, value := range parsed.Filters["to"] {
		appendClause("to_text ILIKE $%d ESCAPE '\\'", likePattern(value))
	}
	for _, value := range parsed.Filters["cc"] {
		appendClause("cc_text ILIKE $%d ESCAPE '\\'", likePattern(value))
	}
	for _, value := range parsed.Filters["subject"] {
		appendClause("subject ILIKE $%d ESCAPE '\\'", likePattern(value))
	}
	for _, value := range parsed.Filters["body"] {
		appendClause("body_text ILIKE $%d ESCAPE '\\'", likePattern(value))
	}
	for _, value := range append(append([]string{}, parsed.Filters["filename"]...), parsed.Filters["attachment"]...) {
		args = append(args, likePattern(value))
		index := len(args)
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM mail_message_attachments ma
			JOIN attachments a ON a.id=ma.attachment_id
			WHERE ma.message_id=mail_messages.id AND a.name ILIKE $%d ESCAPE '\')`, index))
	}
	for _, value := range parsed.Filters["has"] {
		switch value {
		case "attachment", "attachments", "file", "files":
			conditions = append(conditions, `EXISTS (SELECT 1 FROM mail_message_attachments ma WHERE ma.message_id=mail_messages.id)`)
		}
	}
	for _, value := range parsed.Filters["is"] {
		switch value {
		case "unread":
			conditions = append(conditions, "unread")
		case "read":
			conditions = append(conditions, "NOT unread")
		case "starred", "flagged":
			conditions = append(conditions, "(starred OR flagged)")
		}
	}
	if folder := firstValue(parsed.Filters["in"]); folder != "" && folder != "all" && folder != "anywhere" {
		appendClause("folder=$%d", folder)
	}
	if cutoff := firstValue(parsed.Filters["before"]); cutoff != "" {
		if stamp, ok := parseSearchDate(cutoff); ok {
			appendClause("sent_at < $%d", stamp)
		}
	}
	if cutoff := firstValue(parsed.Filters["after"]); cutoff != "" {
		if stamp, ok := parseSearchDate(cutoff); ok {
			appendClause("sent_at >= $%d", stamp)
		}
	}
	if value := firstValue(parsed.Filters["larger"]); value != "" {
		if limit, ok := parseSearchSize(value); ok {
			appendClause("size_bytes > $%d", limit)
		}
	}
	if value := firstValue(parsed.Filters["smaller"]); value != "" {
		if limit, ok := parseSearchSize(value); ok {
			appendClause("size_bytes < $%d", limit)
		}
	}
	if len(parsed.Free) > 0 {
		// Subject/participants are indexed with the simple configuration and
		// bodies with english. Match both so a stemmed body query cannot miss
		// an unstemmed subject word (and vice versa); the OR still uses the GIN
		// index on each side.
		args = append(args, strings.Join(parsed.Free, " "))
		index := len(args)
		conditions = append(conditions, fmt.Sprintf(
			`(search_vector @@ websearch_to_tsquery('simple', $%d) OR search_vector @@ websearch_to_tsquery('english', $%d))`,
			index, index))
	}
	query := `SELECT ` + mailMessageColumns + ` FROM mail_messages WHERE ` +
		strings.Join(conditions, " AND ") + ` ORDER BY sent_at DESC LIMIT 500`
	messages, _, err := s.loadMailMessagesQuery(ctx, userID, query, args...)
	return messages, parsed, err
}

func (s *Server) loadMailMessage(ctx context.Context, userID, id string) (map[string]any, error) {
	messages, _, err := s.loadMailMessagesByIDs(ctx, userID, []string{id})
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("message not found")
	}
	return messages[0], nil
}

func (s *Server) mailQuotaForUser(ctx context.Context, userID string) map[string]any {
	state, err := s.loadState(ctx, userID)
	if err != nil {
		state = map[string]any{}
	}
	return s.mailQuota(ctx, userID, state)
}
