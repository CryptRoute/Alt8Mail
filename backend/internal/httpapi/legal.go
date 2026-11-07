package httpapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type discoveryFilter struct {
	Query          string
	From           time.Time
	To             time.Time
	Folder         string
	UserID         string
	UserEmail      string
	Classification string
	Limit          int
	Offset         int
}

func escapeLikePattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func parseDiscoveryTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date")
}

func discoveryWhere(tenantID string, filter discoveryFilter) (string, []any) {
	where := []string{"m.tenant_id=$1"}
	args := []any{tenantID}
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+escapeLikePattern(truncate(query, 200))+"%")
		position := len(args)
		where = append(where, fmt.Sprintf(
			`(m.subject ILIKE $%d ESCAPE '\' OR m.from_email::text ILIKE $%d ESCAPE '\' OR m.from_name ILIKE $%d ESCAPE '\' OR m.to_text ILIKE $%d ESCAPE '\' OR m.cc_text ILIKE $%d ESCAPE '\' OR m.body_text ILIKE $%d ESCAPE '\')`,
			position, position, position, position, position, position))
	}
	if !filter.From.IsZero() {
		add("m.sent_at >= $%d", filter.From)
	}
	if !filter.To.IsZero() {
		add("m.sent_at < $%d", filter.To.Add(24*time.Hour))
	}
	if filter.Folder != "" {
		args = append(args, truncate(filter.Folder, 120))
		position := len(args)
		where = append(where, fmt.Sprintf("(m.folder = $%d OR m.imap_folder = $%d)", position, position))
	}
	if filter.UserID != "" {
		add("m.user_id = $%d::uuid", filter.UserID)
	}
	if filter.UserEmail != "" {
		add("m.user_id IN (SELECT id FROM public.users WHERE tenant_id=$1 AND lower(email::text)=lower($%d))", truncate(filter.UserEmail, 320))
	}
	if filter.Classification != "" {
		add("m.classification = $%d", filter.Classification)
	}
	return strings.Join(where, " AND "), args
}

func discoveryFilterFromRequest(r *http.Request) (discoveryFilter, error) {
	query := r.URL.Query()
	filter := discoveryFilter{
		Query:          truncate(strings.TrimSpace(query.Get("q")), 200),
		Folder:         truncate(strings.TrimSpace(query.Get("folder")), 120),
		UserID:         strings.TrimSpace(query.Get("userId")),
		UserEmail:      truncate(strings.TrimSpace(query.Get("userEmail")), 320),
		Classification: strings.TrimSpace(query.Get("classification")),
	}
	var err error
	if filter.From, err = parseDiscoveryTime(query.Get("from")); err != nil {
		return filter, fmt.Errorf("Choose a valid start date")
	}
	if filter.To, err = parseDiscoveryTime(query.Get("to")); err != nil {
		return filter, fmt.Errorf("Choose a valid end date")
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		return filter, fmt.Errorf("The end date must be after the start date")
	}
	if filter.UserID != "" {
		if _, parseErr := uuid.Parse(filter.UserID); parseErr != nil {
			return filter, fmt.Errorf("Choose a valid user filter")
		}
	}
	if filter.Classification != "" && !validDLPClassification(filter.Classification) {
		return filter, fmt.Errorf("Choose a valid classification filter")
	}
	filter.Classification = canonicalDLPClassification(filter.Classification)
	filter.Limit = 50
	if raw := query.Get("limit"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil {
			filter.Limit = parsed
		}
	}
	if filter.Limit < 1 {
		filter.Limit = 50
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	if raw := query.Get("offset"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			filter.Offset = parsed
		}
	}
	return filter, nil
}

func (s *Server) userOnLegalHold(ctx context.Context, tenantID, userID string) (bool, error) {
	var held bool
	err := s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.mail_legal_holds
			WHERE tenant_id=$1 AND status='active'
			  AND (scope='tenant' OR (scope='user' AND user_id=$2))
		)
	`, tenantID, userID).Scan(&held)
	return held, err
}

// tenantLegalHoldSnapshot avoids one query per mailbox during retention runs.
func (s *Server) tenantLegalHoldSnapshot(ctx context.Context, tenantID string) (bool, map[string]bool, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT scope, COALESCE(user_id::text, '') FROM public.mail_legal_holds
		WHERE tenant_id=$1 AND status='active'
	`, tenantID)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	tenantWide := false
	users := map[string]bool{}
	for rows.Next() {
		var scope, userID string
		if rows.Scan(&scope, &userID) != nil {
			continue
		}
		if scope == "tenant" {
			tenantWide = true
		} else if userID != "" {
			users[userID] = true
		}
	}
	return tenantWide, users, rows.Err()
}

func (s *Server) adminListLegalHolds(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT h.id, h.scope, COALESCE(h.user_id::text, ''), COALESCE(u.email::text, ''), COALESCE(u.name, ''),
		       h.name, h.reason, h.status, h.created_by_email::text,
		       h.created_at, h.released_at
		FROM public.mail_legal_holds h
		LEFT JOIN public.users u ON u.id = h.user_id
		WHERE h.tenant_id=$1
		ORDER BY CASE WHEN h.status='active' THEN 0 ELSE 1 END, h.created_at DESC
		LIMIT 500
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "HOLDS", "Could not load legal holds")
		return
	}
	defer rows.Close()
	holds := []map[string]any{}
	active := 0
	heldUsers := map[string]bool{}
	tenantWide := false
	for rows.Next() {
		var id, scope, userID, userEmail, userName, name, reason, status, createdBy string
		var createdAt time.Time
		var releasedAt *time.Time
		if rows.Scan(&id, &scope, &userID, &userEmail, &userName, &name, &reason, &status, &createdBy, &createdAt, &releasedAt) != nil {
			continue
		}
		if status == "active" {
			active++
			if scope == "tenant" {
				tenantWide = true
			} else if userID != "" {
				heldUsers[userID] = true
			}
		}
		var released any
		if releasedAt != nil {
			released = releasedAt.UTC().Format(time.RFC3339)
		}
		holds = append(holds, map[string]any{
			"id": id, "scope": scope, "userId": userID, "userEmail": userEmail, "userName": userName,
			"name": name, "reason": reason, "status": status, "createdBy": createdBy,
			"createdAt": createdAt.UTC().Format(time.RFC3339), "releasedAt": released,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"holds": holds,
		"stats": map[string]any{
			"active": active, "tenantWide": tenantWide, "heldUsers": len(heldUsers),
		},
	})
}

func (s *Server) adminCreateLegalHold(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Scope  string `json:"scope"`
		UserID string `json:"userId"`
		Email  string `json:"email"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	body.Scope = strings.ToLower(strings.TrimSpace(body.Scope))
	if body.Scope != "tenant" && body.Scope != "user" {
		writeError(w, http.StatusBadRequest, "HOLD_SCOPE", "Choose a tenant-wide or user hold")
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	if name == "" {
		writeError(w, http.StatusBadRequest, "HOLD_NAME", "Give the hold a matter name")
		return
	}
	reason := truncate(strings.TrimSpace(body.Reason), 500)
	userID, userEmail := "", ""
	if body.Scope == "user" {
		userID = strings.TrimSpace(body.UserID)
		if userID == "" && strings.TrimSpace(body.Email) != "" {
			_ = s.store.DB.QueryRow(r.Context(), `
				SELECT id::text, email::text FROM public.users
				WHERE tenant_id=$1 AND lower(email::text)=lower($2) AND deleted_at IS NULL
			`, sess.ActiveTenantID, strings.TrimSpace(body.Email)).Scan(&userID, &userEmail)
		}
		if userID == "" {
			writeError(w, http.StatusBadRequest, "HOLD_USER", "Choose the user whose mailbox must be preserved")
			return
		}
		var tenantID string
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT tenant_id::text, email::text FROM public.users WHERE id=$1 AND deleted_at IS NULL
		`, userID).Scan(&tenantID, &userEmail)
		if err != nil || tenantID != sess.ActiveTenantID {
			writeError(w, http.StatusNotFound, "HOLD_USER", "User not found in this organization")
			return
		}
	}
	holdID := newID("hold")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_legal_holds
		  (id, tenant_id, scope, user_id, name, reason, status, created_by, created_by_email)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,'active',$7,$8)
	`, holdID, sess.ActiveTenantID, body.Scope, userID, name, reason, sess.User.ID, sess.User.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "HOLD", "Could not place the legal hold")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.legal_hold_created", firstNonEmpty(userEmail, "Tenant-wide"), name,
		map[string]any{"holdId": holdID, "scope": body.Scope, "userId": userID, "reason": reason})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "hold": map[string]any{
		"id": holdID, "scope": body.Scope, "userId": userID, "userEmail": userEmail,
		"name": name, "reason": reason, "status": "active",
		"createdBy": sess.User.Email, "createdAt": time.Now().UTC().Format(time.RFC3339),
	}})
}

func (s *Server) adminReleaseLegalHold(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	holdID := r.PathValue("id")
	var scope, userEmail, name string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE public.mail_legal_holds h
		SET status='released', released_by=$3, released_at=now(), updated_at=now()
		WHERE h.id=$1 AND h.tenant_id=$2 AND h.status='active'
		RETURNING h.scope, h.name, COALESCE((SELECT u.email::text FROM public.users u WHERE u.id=h.user_id), '')
	`, holdID, sess.ActiveTenantID, sess.User.ID).Scan(&scope, &name, &userEmail)
	if err != nil {
		writeError(w, http.StatusNotFound, "HOLD_NOT_FOUND", "Active legal hold not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.legal_hold_released", firstNonEmpty(userEmail, "Tenant-wide"), name,
		map[string]any{"holdId": holdID, "scope": scope})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "released": true})
}

func (s *Server) adminDiscovery(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	filter, err := discoveryFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DISCOVERY_FILTER", err.Error())
		return
	}
	where, args := discoveryWhere(sess.ActiveTenantID, filter)
	var total int
	if err := s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.mail_messages m WHERE `+where, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "DISCOVERY", "Could not search the mail archive")
		return
	}
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT m.id, m.user_id::text, u.email::text, u.name, m.folder, m.subject,
		       m.from_name, m.from_email::text, m.to_text, m.cc_text, m.sent_at,
		       left(m.body_text, 500), m.classification, m.size_bytes
		FROM public.mail_messages m
		JOIN public.users u ON u.id=m.user_id
		WHERE `+where+`
		ORDER BY m.sent_at DESC, m.id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DISCOVERY", "Could not search the mail archive")
		return
	}
	defer rows.Close()
	messages := []map[string]any{}
	for rows.Next() {
		var id, userID, userEmail, userName, folder, subject, fromName, fromEmail, toText, ccText, snippet, classification string
		var sentAt time.Time
		var size int64
		if rows.Scan(&id, &userID, &userEmail, &userName, &folder, &subject, &fromName, &fromEmail,
			&toText, &ccText, &sentAt, &snippet, &classification, &size) != nil {
			continue
		}
		messages = append(messages, map[string]any{
			"id": id, "userId": userID, "userEmail": userEmail, "userName": userName,
			"folder": folder, "subject": subject, "fromName": fromName, "fromEmail": fromEmail,
			"to": toText, "cc": ccText, "sentAt": sentAt.UTC().Format(time.RFC3339),
			"snippet": snippet, "classification": classification, "sizeBytes": size,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": messages, "total": total, "limit": filter.Limit, "offset": filter.Offset,
	})
}

func (s *Server) adminOfficeDiscoveryCSV(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	filter, err := discoveryFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DISCOVERY_FILTER", err.Error())
		return
	}
	where := []string{"f.tenant_id=$1"}
	args := []any{sess.ActiveTenantID}
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if filter.Query != "" {
		args = append(args, "%"+escapeLikePattern(truncate(filter.Query, 200))+"%")
		position := len(args)
		where = append(where, fmt.Sprintf(
			`(f.name ILIKE $%d ESCAPE '\' OR f.path ILIKE $%d ESCAPE '\' OR u.email::text ILIKE $%d ESCAPE '\' OR u.name ILIKE $%d ESCAPE '\' OR d.format ILIKE $%d ESCAPE '\')`,
			position, position, position, position, position))
	}
	if !filter.From.IsZero() {
		add("f.updated_at >= $%d", filter.From)
	}
	if !filter.To.IsZero() {
		add("f.updated_at < $%d", filter.To.Add(24*time.Hour))
	}
	if filter.UserID != "" {
		add("f.user_id = $%d::uuid", filter.UserID)
	}
	if filter.UserEmail != "" {
		add("lower(u.email::text) = lower($%d)", truncate(filter.UserEmail, 320))
	}
	if filter.Classification != "" {
		add("COALESCE(f.metadata->>'dlpClassification','') = $%d", filter.Classification)
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT f.id, u.email::text, u.name, f.name, d.format, f.path, d.current_version,
		       f.size_bytes, COALESCE(f.metadata->>'dlpClassification',''),
		       COALESCE(f.metadata->'dlpWarnings','[]'::jsonb)::text,
		       f.created_at, f.updated_at, d.last_saved_at
		FROM files f
		JOIN users u ON u.id=f.user_id
		JOIN office_documents d ON d.file_id=f.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY f.updated_at DESC
		LIMIT 5000
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DISCOVERY", "Could not export the Office archive")
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="a8m-office-ediscovery-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"File ID", "Mailbox", "User", "File name", "Format", "Path", "Current version", "Size bytes", "Classification", "DLP warnings", "Created", "Updated", "Last saved"})
	exported := 0
	for rows.Next() {
		var id, email, name, fileName, format, path, classification, warnings string
		var version int
		var size int64
		var createdAt, updatedAt time.Time
		var lastSavedAt *time.Time
		if rows.Scan(&id, &email, &name, &fileName, &format, &path, &version,
			&size, &classification, &warnings, &createdAt, &updatedAt, &lastSavedAt) != nil {
			continue
		}
		saved := ""
		if lastSavedAt != nil {
			saved = lastSavedAt.UTC().Format(time.RFC3339)
		}
		_ = writer.Write([]string{
			id, email, name, fileName, format, path, strconv.Itoa(version), strconv.FormatInt(size, 10),
			classification, warnings, createdAt.UTC().Format(time.RFC3339),
			updatedAt.UTC().Format(time.RFC3339), saved,
		})
		exported++
	}
	writer.Flush()
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.office_ediscovery_exported", "Office eDiscovery export",
		"Exported Office document metadata and DLP classification",
		map[string]any{"query": filter.Query, "userId": filter.UserID, "userEmail": filter.UserEmail,
			"classification": filter.Classification, "from": filter.From, "to": filter.To, "documents": exported})
}

func (s *Server) adminDiscoveryCSV(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	filter, err := discoveryFilterFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DISCOVERY_FILTER", err.Error())
		return
	}
	filter.Limit = 5000
	filter.Offset = 0
	where, args := discoveryWhere(sess.ActiveTenantID, filter)
	args = append(args, filter.Limit)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT m.id, u.email::text, u.name, m.folder, m.sent_at, m.from_name, m.from_email::text,
		       m.to_text, m.cc_text, m.subject, m.classification, left(m.body_text, 2000)
		FROM public.mail_messages m
		JOIN public.users u ON u.id=m.user_id
		WHERE `+where+`
		ORDER BY m.sent_at DESC, m.id DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DISCOVERY", "Could not export the mail archive")
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="a8m-ediscovery-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Message ID", "Mailbox", "User", "Folder", "Sent at", "From name", "From", "To", "Cc", "Subject", "Classification", "Body excerpt"})
	exported := 0
	for rows.Next() {
		var id, email, name, folder, fromName, fromEmail, toText, ccText, subject, classification, excerpt string
		var sentAt time.Time
		if rows.Scan(&id, &email, &name, &folder, &sentAt, &fromName, &fromEmail,
			&toText, &ccText, &subject, &classification, &excerpt) != nil {
			continue
		}
		_ = writer.Write([]string{
			id, email, name, folder, sentAt.UTC().Format(time.RFC3339), fromName, fromEmail,
			toText, ccText, subject, classification, excerpt,
		})
		exported++
	}
	writer.Flush()
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.ediscovery_exported", "eDiscovery export", "Exported mail archive search results",
		map[string]any{"query": filter.Query, "folder": filter.Folder, "userId": filter.UserID, "userEmail": filter.UserEmail, "from": filter.From, "to": filter.To, "messages": exported})
}
