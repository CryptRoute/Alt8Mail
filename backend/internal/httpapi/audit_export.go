package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type auditExportEvent struct {
	Source     string          `json:"source"`
	ID         string          `json:"id"`
	Actor      string          `json:"actor"`
	ActorEmail string          `json:"actorEmail"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Detail     string          `json:"detail"`
	IP         string          `json:"ip"`
	UserAgent  string          `json:"userAgent"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  string          `json:"createdAt"`
}

func (s *Server) adminExportAuditBundle(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var from, to time.Time
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		from, err = parseDiscoveryTime(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "AUDIT_EXPORT", "Choose a valid start date")
			return
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		to, err = parseDiscoveryTime(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "AUDIT_EXPORT", "Choose a valid end date")
			return
		}
	}
	events := []auditExportEvent{}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT COALESCE(id::text,''), COALESCE(user_id::text,''), actor, action, target, detail,
		       COALESCE(ip::text,''), user_agent, created_at
		FROM public.audit_log
		WHERE tenant_id=$1 AND ($2::timestamptz IS NULL OR created_at >= $2)
		  AND ($3::timestamptz IS NULL OR created_at < $3)
		ORDER BY created_at ASC, id ASC LIMIT 10000
	`, sess.ActiveTenantID, auditRangeValue(from), auditRangeValue(to))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_EXPORT", "Could not read the audit log")
		return
	}
	for rows.Next() {
		var event auditExportEvent
		var actorID string
		var created time.Time
		if rows.Scan(&event.ID, &actorID, &event.Actor, &event.Action, &event.Target, &event.Detail,
			&event.IP, &event.UserAgent, &created) == nil {
			event.Source = "user"
			event.CreatedAt = created.UTC().Format(time.RFC3339Nano)
			events = append(events, event)
		}
	}
	rows.Close()
	rows, err = s.store.DB.Query(r.Context(), `
		SELECT COALESCE(id::text,''), COALESCE(actor_id::text,''), actor_email::text, action, target, detail,
		       COALESCE(ip::text,''), user_agent, metadata, created_at
		FROM public.admin_audit_log
		WHERE tenant_id=$1 AND ($2::timestamptz IS NULL OR created_at >= $2)
		  AND ($3::timestamptz IS NULL OR created_at < $3)
		ORDER BY created_at ASC, id ASC LIMIT 10000
	`, sess.ActiveTenantID, auditRangeValue(from), auditRangeValue(to))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_EXPORT", "Could not read the admin audit log")
		return
	}
	for rows.Next() {
		var event auditExportEvent
		var actorID string
		var metadata []byte
		var created time.Time
		if rows.Scan(&event.ID, &actorID, &event.ActorEmail, &event.Action, &event.Target, &event.Detail,
			&event.IP, &event.UserAgent, &metadata, &created) == nil {
			event.Source = "admin"
			event.Actor = actorID
			if len(metadata) > 0 {
				event.Metadata = json.RawMessage(metadata)
			}
			event.CreatedAt = created.UTC().Format(time.RFC3339Nano)
			events = append(events, event)
		}
	}
	rows.Close()
	content, _ := json.Marshal(events)
	sum := sha256.Sum256(content)
	contentHash := hex.EncodeToString(sum[:])
	exportID := newID("auditbundle")
	manifest := map[string]any{
		"exportId": exportID, "tenantId": sess.ActiveTenantID, "eventCount": len(events),
		"contentSha256": contentHash, "generatedAt": time.Now().UTC().Format(time.RFC3339),
		"generatedBy": sess.User.Email, "schema": "a8m-audit-export-v1",
		"from": auditRangeString(from), "to": auditRangeString(to),
	}
	manifestJSON, _ := json.Marshal(manifest)
	mac := hmac.New(sha256.New, s.workspaceKey())
	mac.Write(manifestJSON)
	signature := hex.EncodeToString(mac.Sum(nil))
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.audit_exports
		  (id, tenant_id, range_from, range_to, event_count, content_sha256, signature, created_by)
		VALUES ($1,$2,NULLIF($3,'')::timestamptz,NULLIF($4,'')::timestamptz,$5,$6,$7,$8)
	`, exportID, sess.ActiveTenantID, auditRangeString(from), auditRangeString(to), len(events), contentHash, signature, sess.User.ID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.audit_bundle_exported", exportID,
		"Exported a signed audit bundle", map[string]any{"events": len(events), "contentSha256": contentHash})
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="a8m-audit-`+exportID+`.json"`)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exportId": exportID, "manifest": manifest, "signature": signature, "events": events,
	})
}

func auditRangeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func auditRangeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
