package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type discoveryExportMessage struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	UserEmail      string `json:"userEmail"`
	Folder         string `json:"folder"`
	Subject        string `json:"subject"`
	FromName       string `json:"fromName"`
	FromEmail      string `json:"fromEmail"`
	To             string `json:"to"`
	Cc             string `json:"cc"`
	SentAt         string `json:"sentAt"`
	Classification string `json:"classification"`
	MessageID      string `json:"messageId"`
	BodyText       string `json:"bodyText"`
}

type discoveryExportFile struct {
	ID             string `json:"id"`
	OwnerEmail     string `json:"ownerEmail"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	MIME           string `json:"mime"`
	SizeBytes      int64  `json:"sizeBytes"`
	Classification string `json:"classification"`
	UpdatedAt      string `json:"updatedAt"`
}

type discoveryExportData struct {
	Messages  []discoveryExportMessage `json:"messages"`
	Files     []discoveryExportFile    `json:"files"`
	Documents []map[string]any         `json:"documents"`
}

func (s *Server) collectDiscoveryExport(ctx context.Context, tenantID string, filter discoveryFilter, limit int) (discoveryExportData, error) {
	data := discoveryExportData{Messages: []discoveryExportMessage{}, Files: []discoveryExportFile{}, Documents: []map[string]any{}}
	where, args := discoveryWhere(tenantID, filter)
	args = append(args, limit)
	rows, err := s.store.DB.Query(ctx, `
		SELECT m.id, m.user_id::text, u.email::text, m.folder, m.subject, m.from_name, m.from_email::text,
		       m.to_text, m.cc_text, m.sent_at, m.classification, COALESCE(m.message_id,''), m.body_text
		FROM public.mail_messages m
		JOIN public.users u ON u.id=m.user_id
		WHERE `+where+`
		ORDER BY m.sent_at ASC, m.id ASC
		LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var message discoveryExportMessage
		var sent time.Time
		if rows.Scan(&message.ID, &message.UserID, &message.UserEmail, &message.Folder, &message.Subject,
			&message.FromName, &message.FromEmail, &message.To, &message.Cc, &sent,
			&message.Classification, &message.MessageID, &message.BodyText) == nil {
			message.SentAt = sent.UTC().Format(time.RFC3339)
			data.Messages = append(data.Messages, message)
		}
	}
	rows.Close()

	fileWhere := []string{"f.tenant_id=$1"}
	fileArgs := []any{tenantID}
	addFile := func(condition string, value any) {
		fileArgs = append(fileArgs, value)
		fileWhere = append(fileWhere, fmt.Sprintf(condition, len(fileArgs)))
	}
	if filter.Query != "" {
		fileArgs = append(fileArgs, "%"+escapeLikePattern(truncate(filter.Query, 200))+"%")
		position := len(fileArgs)
		fileWhere = append(fileWhere, fmt.Sprintf(`(f.name ILIKE $%d ESCAPE '\' OR f.path ILIKE $%d ESCAPE '\')`, position, position))
	}
	if !filter.From.IsZero() {
		addFile("f.updated_at >= $%d", filter.From)
	}
	if !filter.To.IsZero() {
		addFile("f.updated_at < $%d", filter.To.Add(24*time.Hour))
	}
	if filter.UserID != "" {
		addFile("f.user_id = $%d::uuid", filter.UserID)
	}
	if filter.UserEmail != "" {
		addFile("lower(u.email::text) = lower($%d)", truncate(filter.UserEmail, 320))
	}
	fileArgs = append(fileArgs, limit)
	rows, err = s.store.DB.Query(ctx, `
		SELECT f.id, u.email::text, f.name, f.path, f.mime, f.size_bytes,
		       COALESCE(f.metadata->>'dlpClassification',''), f.updated_at
		FROM public.files f JOIN public.users u ON u.id=f.user_id
		WHERE `+strings.Join(fileWhere, " AND ")+`
		ORDER BY f.updated_at ASC, f.id ASC LIMIT $`+strconv.Itoa(len(fileArgs)), fileArgs...)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var file discoveryExportFile
		var updated time.Time
		if rows.Scan(&file.ID, &file.OwnerEmail, &file.Name, &file.Path, &file.MIME, &file.SizeBytes,
			&file.Classification, &updated) == nil {
			file.UpdatedAt = updated.UTC().Format(time.RFC3339)
			data.Files = append(data.Files, file)
		}
	}
	rows.Close()

	rows, err = s.store.DB.Query(ctx, `
		SELECT d.id, f.name, u.email::text, d.format, d.current_version,
		       COALESCE(f.metadata->>'dlpClassification',''), d.updated_at
		FROM public.office_documents d
		JOIN public.files f ON f.id=d.file_id
		JOIN public.users u ON u.id=d.user_id
		WHERE d.tenant_id=$1
		ORDER BY d.updated_at ASC LIMIT $2
	`, tenantID, limit)
	if err == nil {
		for rows.Next() {
			var id, name, owner, format, classification string
			var version int
			var updated time.Time
			if rows.Scan(&id, &name, &owner, &format, &version, &classification, &updated) == nil {
				data.Documents = append(data.Documents, map[string]any{
					"id": id, "file": name, "ownerEmail": owner, "format": format,
					"currentVersion": version, "classification": classification,
					"updatedAt": updated.UTC().Format(time.RFC3339),
				})
			}
		}
		rows.Close()
	}
	return data, nil
}

func (s *Server) adminDiscoveryAll(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	filter, err := discoveryFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DISCOVERY_FILTER", err.Error())
		return
	}
	data, err := s.collectDiscoveryExport(r.Context(), sess.ActiveTenantID, filter, filter.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DISCOVERY", "Could not search the archive")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": data.Messages, "files": data.Files, "documents": data.Documents,
		"counts": map[string]any{"messages": len(data.Messages), "files": len(data.Files), "documents": len(data.Documents)},
	})
}

func mboxEscape(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "From ") {
			lines[index] = ">" + line
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Server) adminLegalExport(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "mbox" {
		writeError(w, http.StatusBadRequest, "EXPORT_FORMAT", "Choose json or mbox")
		return
	}
	caseName := truncate(strings.TrimSpace(r.URL.Query().Get("case")), 120)
	if caseName == "" {
		caseName = "Case " + time.Now().UTC().Format("2006-01-02")
	}
	filter, err := discoveryFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DISCOVERY_FILTER", err.Error())
		return
	}
	data, err := s.collectDiscoveryExport(r.Context(), sess.ActiveTenantID, filter, 5000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EXPORT", "Could not build the export")
		return
	}
	hashPayload, _ := json.Marshal(data)
	sum := sha256.Sum256(hashPayload)
	contentHash := hex.EncodeToString(sum[:])
	exportID := newID("export")
	manifest := map[string]any{
		"exportId": exportID, "case": caseName, "tenantId": sess.ActiveTenantID,
		"generatedAt": time.Now().UTC().Format(time.RFC3339), "generatedBy": sess.User.Email,
		"format": format, "contentSha256": contentHash,
		"counts": map[string]int{"messages": len(data.Messages), "files": len(data.Files), "documents": len(data.Documents)},
		"filters": map[string]any{
			"q": filter.Query, "from": filter.From, "to": filter.To, "folder": filter.Folder,
			"userId": filter.UserID, "userEmail": filter.UserEmail, "classification": filter.Classification,
		},
	}
	manifestJSON, _ := json.Marshal(manifest)
	mac := hmac.New(sha256.New, s.workspaceKey())
	mac.Write(manifestJSON)
	signature := hex.EncodeToString(mac.Sum(nil))
	queryJSON, _ := json.Marshal(manifest["filters"])
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.discovery_exports
		  (id, tenant_id, case_name, format, query, item_count, manifest_sha256, signature, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, exportID, sess.ActiveTenantID, caseName, format, queryJSON,
		len(data.Messages)+len(data.Files)+len(data.Documents), contentHash, signature, sess.User.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.ediscovery_exported", caseName,
		"Produced a signed eDiscovery export", map[string]any{
			"exportId": exportID, "format": format, "contentSha256": contentHash,
			"messages": len(data.Messages), "files": len(data.Files), "documents": len(data.Documents),
		})
	if format == "mbox" {
		w.Header().Set("Content-Type", "application/mbox; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="a8m-`+exportID+`.mbox"`)
		w.Header().Set("X-A8M-Manifest", base64.StdEncoding.EncodeToString(manifestJSON))
		w.Header().Set("X-A8M-Signature", signature)
		for _, message := range data.Messages {
			fmt.Fprintf(w, "From %s %s\n", message.FromEmail, message.SentAt)
			fmt.Fprintf(w, "From: %s <%s>\n", message.FromName, message.FromEmail)
			fmt.Fprintf(w, "To: %s\n", message.To)
			if message.Cc != "" {
				fmt.Fprintf(w, "Cc: %s\n", message.Cc)
			}
			fmt.Fprintf(w, "Subject: %s\n", message.Subject)
			fmt.Fprintf(w, "Date: %s\n", message.SentAt)
			fmt.Fprintf(w, "Message-ID: <%s@a8m.local>\n", message.ID)
			fmt.Fprintf(w, "X-A8M-Classification: %s\n", message.Classification)
			fmt.Fprintf(w, "X-A8M-Mailbox: %s\n", message.UserEmail)
			fmt.Fprint(w, "Content-Type: text/plain; charset=utf-8\n\n")
			fmt.Fprint(w, mboxEscape(message.BodyText))
			fmt.Fprint(w, "\n\n")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exportId": exportID, "manifest": manifest, "signature": signature,
		"messages": data.Messages, "files": data.Files, "documents": data.Documents,
	})
}

func (s *Server) adminListDiscoveryReviews(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, COALESCE(export_id,''), item_type, item_id, subject, status,
		       reviewer_email::text, note, created_at, updated_at
		FROM public.discovery_review_items
		WHERE tenant_id=$1 AND ($2='' OR status=$2)
		ORDER BY CASE WHEN status='pending' THEN 0 ELSE 1 END, created_at DESC LIMIT 1000
	`, sess.ActiveTenantID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REVIEW_QUEUE", "Could not load the review queue")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, exportID, itemType, itemID, subject, itemStatus, reviewer, note string
		var created, updated time.Time
		if rows.Scan(&id, &exportID, &itemType, &itemID, &subject, &itemStatus, &reviewer, &note, &created, &updated) == nil {
			items = append(items, map[string]any{
				"id": id, "exportId": exportID, "itemType": itemType, "itemId": itemID,
				"subject": subject, "status": itemStatus, "reviewerEmail": reviewer, "note": note,
				"createdAt": created.UTC().Format(time.RFC3339), "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) adminCreateDiscoveryReviews(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		ExportID string `json:"exportId"`
		Items    []struct {
			ItemType string `json:"itemType"`
			ItemID   string `json:"itemId"`
			Subject  string `json:"subject"`
		} `json:"items"`
	}
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	created := 0
	for _, item := range body.Items {
		itemType := strings.ToLower(strings.TrimSpace(item.ItemType))
		if itemType != "mail" && itemType != "file" && itemType != "document" {
			continue
		}
		itemID := truncate(strings.TrimSpace(item.ItemID), 120)
		if itemID == "" {
			continue
		}
		result, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO public.discovery_review_items (id, tenant_id, export_id, item_type, item_id, subject)
			VALUES ($1,$2,NULLIF($3,''),$4,$5,$6)
			ON CONFLICT (tenant_id, item_type, item_id) DO NOTHING
		`, newID("review"), sess.ActiveTenantID, strings.TrimSpace(body.ExportID), itemType, itemID,
			truncate(strings.TrimSpace(item.Subject), 300))
		if err == nil {
			if affected := result.RowsAffected(); affected > 0 {
				created++
			}
		}
		if created >= 500 {
			break
		}
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.review_queue_created", "eDiscovery review",
		"Queued items for legal review", map[string]any{"created": created})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "created": created})
}

func (s *Server) adminDecideDiscoveryReview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "approved" && status != "rejected" && status != "pending" {
		writeError(w, http.StatusBadRequest, "REVIEW_QUEUE", "Status must be pending, approved or rejected")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.discovery_review_items
		SET status=$3, reviewer_id=$4, reviewer_email=$5, note=$6, updated_at=now()
		WHERE id=$1 AND tenant_id=$2
	`, r.PathValue("id"), sess.ActiveTenantID, status, sess.User.ID, sess.User.Email,
		truncate(strings.TrimSpace(body.Note), 1000))
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "REVIEW_QUEUE", "Review item not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.review_queue_decided", r.PathValue("id"),
		"Decided a legal review item", map[string]any{"status": status})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}
