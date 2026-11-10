package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) adminUsageAnalytics(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tenantID := sess.ActiveTenantID
	response := map[string]any{}
	var users, messages, files, tasks, events, triage, obligations int
	var storage int64
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.users WHERE tenant_id=$1 AND deleted_at IS NULL`, tenantID).Scan(&users)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT COALESCE(sum(used_bytes),0) FROM public.users WHERE tenant_id=$1`, tenantID).Scan(&storage)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.mail_messages WHERE tenant_id=$1 AND NOT draft`, tenantID).Scan(&messages)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.files WHERE tenant_id=$1`, tenantID).Scan(&files)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.tasks WHERE tenant_id=$1`, tenantID).Scan(&tasks)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.calendar_events WHERE tenant_id=$1`, tenantID).Scan(&events)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.ai_triage WHERE tenant_id=$1`, tenantID).Scan(&triage)
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.ai_obligations WHERE tenant_id=$1`, tenantID).Scan(&obligations)
	response["tenant"] = map[string]any{"id": tenantID, "name": sess.ActiveTenant.Name}
	response["users"] = users
	response["storageBytes"] = storage
	response["mailMessages"] = messages
	response["files"] = files
	response["tasks"] = tasks
	response["calendarEvents"] = events
	response["aiTriage"] = triage
	response["aiObligations"] = obligations
	var sent int
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT count(*) FROM public.outbound_events WHERE tenant_id=$1 AND sent_at > now()-interval '30 days'
	`, tenantID).Scan(&sent)
	response["sentLast30Days"] = sent
	var aiRequests, aiCost int64
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT requests, cost_micros FROM public.ai_usage
		WHERE tenant_id=$1 AND month=date_trunc('month', now())::date
	`, tenantID).Scan(&aiRequests, &aiCost)
	response["aiRequestsThisMonth"] = aiRequests
	response["aiCostMicrosThisMonth"] = aiCost
	series := []map[string]any{}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT to_char(date_trunc('day', sent_at), 'YYYY-MM-DD') AS day, count(*)
		FROM public.outbound_events WHERE tenant_id=$1 AND sent_at > now()-interval '30 days'
		GROUP BY day ORDER BY day
	`, tenantID)
	if err == nil {
		for rows.Next() {
			var day string
			var count int
			if rows.Scan(&day, &count) == nil {
				series = append(series, map[string]any{"day": day, "sent": count})
			}
		}
		rows.Close()
	}
	response["sendSeries"] = series
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) adminSupportBundle(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tenantID := sess.ActiveTenantID
	response := map[string]any{
		"generatedAt": time.Now().UTC().Format(time.RFC3339),
		"version":     s.version,
		"tenant": map[string]any{
			"id": tenantID, "name": sess.ActiveTenant.Name, "slug": sess.ActiveTenant.Slug,
			"plan": sess.ActiveTenant.Plan, "region": sess.ActiveTenant.Region,
		},
		"deployment": map[string]any{
			"publicUrl": s.cfg.PublicURL, "officeReady": s.cfg.PublicURL != "",
		},
	}
	counts := map[string]int{}
	counts["users"] = countTenantRows(r.Context(), s, tenantID, "users", "deleted_at IS NULL")
	counts["mailMessages"] = countTenantRows(r.Context(), s, tenantID, "mail_messages", "NOT draft")
	counts["files"] = countTenantRows(r.Context(), s, tenantID, "files", "")
	counts["tasks"] = countTenantRows(r.Context(), s, tenantID, "tasks", "")
	counts["calendarEvents"] = countTenantRows(r.Context(), s, tenantID, "calendar_events", "")
	response["counts"] = counts
	migrations := []map[string]any{}
	rows, err := s.store.DB.Query(r.Context(), `SELECT version, applied_at FROM schema_migrations ORDER BY version DESC LIMIT 10`)
	if err == nil {
		for rows.Next() {
			var version string
			var applied time.Time
			if rows.Scan(&version, &applied) == nil {
				migrations = append(migrations, map[string]any{"version": version, "appliedAt": applied.UTC().Format(time.RFC3339)})
			}
		}
		rows.Close()
	}
	response["migrations"] = migrations
	response["checks"] = map[string]any{
		"licenseLoaded": s.license != nil,
		"aiConfigured": func() bool {
			settings, err := s.aiSettingsFor(r.Context(), tenantID)
			return err == nil && settings.Enabled && settings.Model != ""
		}(),
	}
	w.Header().Set("Content-Disposition", `attachment; filename="a8m-support-`+tenantID+`.json"`)
	writeJSON(w, http.StatusOK, response)
}

func countTenantRows(ctx context.Context, s *Server, tenantID, table, filter string) int {
	query := `SELECT count(*) FROM public.` + table + ` WHERE tenant_id=$1`
	if filter != "" {
		query += " AND " + filter
	}
	var count int
	_ = s.store.DB.QueryRow(ctx, query, tenantID).Scan(&count)
	return count
}

func (s *Server) adminGetTelemetry(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var enabled bool
	var endpoint, lastError string
	var lastSent *time.Time
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT enabled, endpoint, last_sent_at, last_error FROM public.tenant_telemetry WHERE tenant_id=$1
	`, sess.ActiveTenantID).Scan(&enabled, &endpoint, &lastSent, &lastError)
	response := map[string]any{"enabled": enabled, "endpoint": endpoint, "lastError": lastError}
	if lastSent != nil {
		response["lastSentAt"] = lastSent.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) adminUpdateTelemetry(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	endpoint := strings.TrimSpace(body.Endpoint)
	if body.Enabled {
		if _, err := s.validateWebhookURL(endpoint); err != nil {
			writeError(w, http.StatusBadRequest, "TELEMETRY", err.Error())
			return
		}
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.tenant_telemetry (tenant_id, enabled, endpoint, updated_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (tenant_id) DO UPDATE SET enabled=EXCLUDED.enabled, endpoint=EXCLUDED.endpoint, updated_at=now()
	`, sess.ActiveTenantID, body.Enabled, endpoint)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TELEMETRY", "Could not save telemetry settings")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.telemetry_updated", endpoint, "Updated optional telemetry", map[string]any{"enabled": body.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
}

func (s *Server) adminSendTelemetry(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var enabled bool
	var endpoint string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT enabled, endpoint FROM public.tenant_telemetry WHERE tenant_id=$1
	`, sess.ActiveTenantID).Scan(&enabled, &endpoint); err != nil || !enabled || endpoint == "" {
		writeError(w, http.StatusConflict, "TELEMETRY", "Optional telemetry is not enabled")
		return
	}
	payload := map[string]any{
		"schema": "a8m-telemetry-v1", "version": s.version,
		"tenantId": sess.ActiveTenantID, "generatedAt": time.Now().UTC().Format(time.RFC3339),
		"users":    countTenantRows(r.Context(), s, sess.ActiveTenantID, "users", "deleted_at IS NULL"),
		"messages": countTenantRows(r.Context(), s, sess.ActiveTenantID, "mail_messages", "NOT draft"),
	}
	raw, _ := json.Marshal(payload)
	status, err := s.postWebhook(r.Context(), endpoint, map[string]string{"X-A8M-Telemetry": "1"}, raw)
	if err != nil || status < 200 || status >= 300 {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.tenant_telemetry SET last_error=$2, last_sent_at=now() WHERE tenant_id=$1`, sess.ActiveTenantID, truncate(firstNonEmpty(errString(err), "HTTP"), 500))
		writeError(w, http.StatusBadGateway, "TELEMETRY", "The telemetry endpoint did not accept the bundle")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.tenant_telemetry SET last_error='', last_sent_at=now() WHERE tenant_id=$1`, sess.ActiveTenantID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
