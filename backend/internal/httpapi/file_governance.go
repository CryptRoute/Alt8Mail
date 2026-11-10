package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func fileClassification(file contentFile) string {
	var metadata map[string]any
	if len(file.Metadata) > 0 {
		_ = json.Unmarshal(file.Metadata, &metadata)
	}
	return canonicalDLPClassification(stateString(metadata, "dlpClassification"))
}

func (s *Server) fileWatermark(r *http.Request, file contentFile, tenantID string) string {
	policy := s.tenantPolicyFor(r.Context(), tenantID).Files
	if !policy.WatermarkDownloads || dlpClassificationRank(fileClassification(file)) < dlpClassificationRank("Confidential") {
		return ""
	}
	actor := "shared link"
	if sess := s.requireSessionFromContext(r); sess != nil {
		actor = sess.User.Email
	}
	return fmt.Sprintf("%s · %s · %s · Alt8Mail", fileClassification(file), actor, time.Now().UTC().Format(time.RFC3339))
}

func (s *Server) fileOnLegalHold(ctx context.Context, tenantID, fileID, ownerID string) (bool, error) {
	var held bool
	err := s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.file_legal_holds
			WHERE tenant_id=$1 AND status='active'
			  AND (scope='tenant'
			       OR (scope='user' AND user_id=$2)
			       OR (scope='file' AND file_id=$3))
		)
	`, tenantID, ownerID, fileID).Scan(&held)
	return held, err
}

func (s *Server) adminListFileHolds(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT h.id, h.scope, COALESCE(h.user_id::text,''), COALESCE(u.email::text,''),
		       COALESCE(h.file_id,''), COALESCE(f.name,''), h.name, h.reason, h.status,
		       h.created_by_email::text, h.created_at, h.released_at
		FROM public.file_legal_holds h
		LEFT JOIN public.users u ON u.id=h.user_id
		LEFT JOIN public.files f ON f.id=h.file_id
		WHERE h.tenant_id=$1
		ORDER BY CASE WHEN h.status='active' THEN 0 ELSE 1 END, h.created_at DESC LIMIT 500
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_HOLD", "Could not load file holds")
		return
	}
	defer rows.Close()
	holds := []map[string]any{}
	for rows.Next() {
		var id, scope, userID, userEmail, fileID, fileName, name, reason, status, createdBy string
		var created time.Time
		var released *time.Time
		if rows.Scan(&id, &scope, &userID, &userEmail, &fileID, &fileName, &name, &reason, &status, &createdBy, &created, &released) == nil {
			item := map[string]any{
				"id": id, "scope": scope, "userId": userID, "userEmail": userEmail,
				"fileId": fileID, "fileName": fileName, "name": name, "reason": reason,
				"status": status, "createdBy": createdBy, "createdAt": created.UTC().Format(time.RFC3339),
			}
			if released != nil {
				item["releasedAt"] = released.UTC().Format(time.RFC3339)
			}
			holds = append(holds, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"holds": holds, "count": len(holds)})
}

func (s *Server) adminCreateFileHold(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Scope  string `json:"scope"`
		UserID string `json:"userId"`
		Email  string `json:"email"`
		FileID string `json:"fileId"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if scope != "tenant" && scope != "user" && scope != "file" {
		writeError(w, http.StatusBadRequest, "FILE_HOLD", "Choose a tenant, user or file hold")
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	if name == "" {
		writeError(w, http.StatusBadRequest, "FILE_HOLD", "Give the hold a matter name")
		return
	}
	userID, fileID := "", strings.TrimSpace(body.FileID)
	if scope == "user" {
		userID = strings.TrimSpace(body.UserID)
		if userID == "" && strings.TrimSpace(body.Email) != "" {
			_ = s.store.DB.QueryRow(r.Context(), `
				SELECT id::text FROM public.users WHERE tenant_id=$1 AND lower(email::text)=lower($2)
			`, sess.ActiveTenantID, strings.TrimSpace(body.Email)).Scan(&userID)
		}
		if userID == "" {
			writeError(w, http.StatusBadRequest, "FILE_HOLD", "Choose the user whose files must be preserved")
			return
		}
		var belongs bool
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT EXISTS(SELECT 1 FROM public.users WHERE id=$1 AND tenant_id=$2)
		`, userID, sess.ActiveTenantID).Scan(&belongs)
		if !belongs {
			writeError(w, http.StatusBadRequest, "FILE_HOLD", "The selected user does not belong to this organization")
			return
		}
	}
	if scope == "file" {
		if fileID == "" {
			writeError(w, http.StatusBadRequest, "FILE_HOLD", "Choose the file to preserve")
			return
		}
		var ownerID string
		if err := s.store.DB.QueryRow(r.Context(), `SELECT user_id::text FROM public.files WHERE id=$1 AND tenant_id=$2`, fileID, sess.ActiveTenantID).Scan(&ownerID); err != nil {
			writeError(w, http.StatusNotFound, "FILE_HOLD", "File not found")
			return
		}
	}
	holdID := newID("filehold")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.file_legal_holds (id, tenant_id, scope, user_id, file_id, name, reason, created_by_email)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,''),$6,$7,$8)
	`, holdID, sess.ActiveTenantID, scope, userID, fileID, name, truncate(strings.TrimSpace(body.Reason), 500), sess.User.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_HOLD", "Could not create the file hold")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.file_hold_created", name, "Created a file legal hold",
		map[string]any{"holdId": holdID, "scope": scope, "userId": userID, "fileId": fileID})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": holdID})
}

func (s *Server) adminReleaseFileHold(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_legal_holds SET status='released', released_at=now()
		WHERE id=$1 AND tenant_id=$2 AND status='active'
	`, r.PathValue("id"), sess.ActiveTenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FILE_HOLD", "Active file hold not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.file_hold_released", r.PathValue("id"), "Released a file legal hold", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// runFileRetention removes files and Office versions older than the tenant's
// file policy, skipping active legal holds. Each deletion is audited.
func (s *Server) runFileRetention(ctx context.Context, tenantID string) (map[string]int, error) {
	stats := map[string]int{"files": 0, "versions": 0, "skippedHolds": 0, "errors": 0}
	policy := s.tenantPolicyFor(ctx, tenantID).Files
	maxDays := policy.RetentionDays
	for _, days := range policy.RetentionByClassification {
		if days > maxDays {
			maxDays = days
		}
	}
	if maxDays <= 0 && policy.VersionRetentionDays <= 0 {
		return stats, nil
	}
	if maxDays > 0 {
		maxCutoff := time.Now().UTC().AddDate(0, 0, -maxDays)
		rows, err := s.store.DB.Query(ctx, `
			SELECT id, user_id::text, COALESCE(metadata->>'dlpClassification',''), updated_at
			FROM public.files
			WHERE tenant_id=$1 AND updated_at < $2
			ORDER BY updated_at LIMIT 1000
		`, tenantID, maxCutoff)
		if err != nil {
			return stats, err
		}
		type candidate struct {
			id, owner, class string
			updated          time.Time
		}
		candidates := []candidate{}
		for rows.Next() {
			var item candidate
			if rows.Scan(&item.id, &item.owner, &item.class, &item.updated) == nil {
				candidates = append(candidates, item)
			}
		}
		rows.Close()
		now := time.Now().UTC()
		for _, item := range candidates {
			days := policy.RetentionDays
			if classDays, ok := policy.RetentionByClassification[canonicalDLPClassification(item.class)]; ok {
				days = classDays
			}
			if days <= 0 || item.updated.After(now.AddDate(0, 0, -days)) {
				continue
			}
			held, err := s.fileOnLegalHold(ctx, tenantID, item.id, item.owner)
			if err != nil || held {
				stats["skippedHolds"]++
				continue
			}
			if err := s.purgeFileRecord(ctx, item.id, item.owner, tenantID); err != nil {
				stats["errors"]++
				continue
			}
			stats["files"]++
		}
	}
	if policy.VersionRetentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -policy.VersionRetentionDays)
		rows, err := s.store.DB.Query(ctx, `
			SELECT v.id, v.storage_key, d.file_id, d.user_id::text
			FROM public.office_document_versions v
			JOIN public.office_documents d ON d.id=v.document_id
			WHERE d.tenant_id=$1 AND v.created_at < $2
			  AND v.version < d.current_version
			ORDER BY v.created_at LIMIT 2000
		`, tenantID, cutoff)
		if err != nil {
			return stats, err
		}
		type versionCandidate struct{ id, key, fileID, owner string }
		versions := []versionCandidate{}
		for rows.Next() {
			var item versionCandidate
			if rows.Scan(&item.id, &item.key, &item.fileID, &item.owner) == nil {
				versions = append(versions, item)
			}
		}
		rows.Close()
		for _, item := range versions {
			held, err := s.fileOnLegalHold(ctx, tenantID, item.fileID, item.owner)
			if err != nil || held {
				stats["skippedHolds"]++
				continue
			}
			if _, err := s.store.DB.Exec(ctx, `DELETE FROM public.office_document_versions WHERE id=$1`, item.id); err != nil {
				stats["errors"]++
				continue
			}
			if strings.HasPrefix(item.key, "ofv_") {
				s.officeRemoveStorage(item.key)
			}
			stats["versions"]++
		}
	}
	return stats, nil
}

func (s *Server) purgeFileRecord(ctx context.Context, fileID, ownerID, tenantID string) error {
	var provider, storageKey string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT provider_id, storage_key FROM public.files WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, fileID, ownerID, tenantID).Scan(&provider, &storageKey); err != nil {
		return err
	}
	officeKeys := []string{}
	rows, err := s.store.DB.Query(ctx, `
		SELECT v.storage_key FROM public.office_document_versions v
		JOIN public.office_documents d ON d.id=v.document_id
		WHERE d.file_id=$1
	`, fileID)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil && strings.HasPrefix(key, "ofv_") {
				officeKeys = append(officeKeys, key)
			}
		}
		rows.Close()
	}
	if provider == "s3" && storageKey != "" {
		if err := s.s3DeleteObject(ctx, tenantID, storageKey); err != nil {
			return err
		}
	}
	result, err := s.store.DB.Exec(ctx, `DELETE FROM public.files WHERE id=$1 AND user_id=$2 AND tenant_id=$3`, fileID, ownerID, tenantID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("file not found")
	}
	for _, key := range officeKeys {
		s.officeRemoveStorage(key)
	}
	return nil
}

func (s *Server) adminRunFileRetention(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	stats, err := s.runFileRetention(r.Context(), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_RETENTION", err.Error())
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.file_retention_run", "File retention",
		"Ran file and version retention", map[string]any{"tenantId": sess.ActiveTenantID, "stats": stats})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": stats})
}

func (s *Server) adminListFileShareRequests(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT r.id, r.file_id, f.name, r.requested_by::text, u.email::text,
		       r.access, r.recipient_email::text, r.expires_days, r.state, r.created_at
		FROM public.file_share_requests r
		JOIN public.files f ON f.id=r.file_id
		JOIN public.users u ON u.id=r.requested_by
		WHERE r.tenant_id=$1
		ORDER BY CASE WHEN r.state='pending' THEN 0 ELSE 1 END, r.created_at DESC LIMIT 500
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SHARE_REQUEST", "Could not load share requests")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, fileID, fileName, requestedBy, requesterEmail, access, recipient, state string
		var expiresDays int
		var created time.Time
		if rows.Scan(&id, &fileID, &fileName, &requestedBy, &requesterEmail, &access, &recipient, &expiresDays, &state, &created) == nil {
			items = append(items, map[string]any{
				"id": id, "fileId": fileID, "fileName": fileName, "requestedBy": requestedBy,
				"requesterEmail": requesterEmail, "access": access, "recipientEmail": recipient,
				"expiresDays": expiresDays, "state": state, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": items, "count": len(items)})
}

func (s *Server) adminDecideFileShareRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision != "approve" && decision != "deny" {
		writeError(w, http.StatusBadRequest, "SHARE_REQUEST", "Decision must be approve or deny")
		return
	}
	state := "approved"
	if decision == "deny" {
		state = "denied"
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.file_share_requests
		SET state=$3, decided_by=$4, decided_at=now(), decision_note=$5
		WHERE id=$1 AND tenant_id=$2 AND state='pending'
	`, r.PathValue("id"), sess.ActiveTenantID, state, sess.User.ID, truncate(strings.TrimSpace(body.Note), 500))
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "SHARE_REQUEST", "Pending share request not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.file_share_request_decided", r.PathValue("id"),
		"Decided a file share request", map[string]any{"state": state})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}
