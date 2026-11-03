package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/threatintel"
)

func (s *Server) registerSecurityReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/security/reports", s.require("security.read", s.handleSecurityReports))
	mux.HandleFunc("GET /control/api/security/reports/{id}", s.require("security.read", s.handleSecurityReportDetail))
	mux.HandleFunc("PATCH /control/api/security/reports/{id}", s.require("security.manage", s.handleSecurityReportReview))
	mux.HandleFunc("POST /control/api/security/reports/{id}/undo", s.require("security.manage", s.handleSecurityReportUndo))
}

func (s *Server) securityReportSummary(ctx context.Context) map[string]any {
	counts := map[string]int64{"pending": 0, "confirmed": 0, "dismissed": 0, "blockedSenders": 0}
	rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM public.security_reports GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var status string
			var count int64
			if rows.Scan(&status, &count) == nil {
				counts[status] = count
			}
		}
		rows.Close()
	}
	var blockedSenders int64
	_ = s.db.QueryRow(ctx, `
		SELECT count(DISTINCT CASE WHEN block_scope='domain' THEN sender_domain ELSE sender_email::text END)
		FROM public.security_reports WHERE status IN ('pending','confirmed') AND block_scope <> 'none'
	`).Scan(&blockedSenders)
	counts["blockedSenders"] = blockedSenders
	recent := []map[string]any{}
	rows, err = s.db.Query(ctx, `
		SELECT r.id, r.sender_email::text, r.sender_name, r.subject, r.reason, r.status, r.created_at,
		       t.name, r.user_email::text
		FROM public.security_reports r
		LEFT JOIN public.tenants t ON t.id = r.tenant_id
		ORDER BY r.created_at DESC LIMIT 5
	`)
	if err == nil {
		for rows.Next() {
			var id, senderEmail, senderName, subject, reason, status, tenantName, userEmail string
			var createdAt time.Time
			if rows.Scan(&id, &senderEmail, &senderName, &subject, &reason, &status, &createdAt, &tenantName, &userEmail) == nil {
				recent = append(recent, map[string]any{
					"id": id, "senderEmail": senderEmail, "senderName": senderName,
					"subject": subject, "reason": reason, "status": status,
					"tenant": tenantName, "userEmail": userEmail,
					"createdAt": createdAt.UTC().Format(time.RFC3339),
				})
			}
		}
		rows.Close()
	}
	return map[string]any{"counts": counts, "recent": recent}
}

func (s *Server) handleSecurityReports(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.db.Query(r.Context(), `
		SELECT r.id, r.tenant_id::text, COALESCE(t.name,''), r.user_id::text, r.user_email::text,
		       r.message_id, r.sender_email::text, r.sender_name, r.sender_domain, r.subject, r.snippet,
		       r.reason, r.note, r.status, r.block_scope, r.reviewed_by, r.reviewed_at, r.resolution,
		       r.created_at, r.updated_at
		FROM public.security_reports r
		LEFT JOIN public.tenants t ON t.id = r.tenant_id
		WHERE ($1='' OR r.status=$1)
		  AND ($2='' OR r.reason=$2)
		  AND ($3='' OR r.sender_email::text ILIKE '%'||$3||'%' OR r.subject ILIKE '%'||$3||'%'
		       OR r.user_email::text ILIKE '%'||$3||'%' OR r.sender_name ILIKE '%'||$3||'%')
		ORDER BY CASE r.status WHEN 'pending' THEN 0 WHEN 'confirmed' THEN 1 ELSE 2 END, r.created_at DESC
		LIMIT 500
	`, status, reason, query)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	reports := []map[string]any{}
	for rows.Next() {
		var id, tenantID, tenantName, userID, userEmail, messageID, senderEmail, senderName string
		var senderDomain, subject, snippet, reasonValue, note, statusValue, blockScope, reviewedBy, resolution string
		var reviewedAt *time.Time
		var createdAt, updatedAt time.Time
		if rows.Scan(&id, &tenantID, &tenantName, &userID, &userEmail, &messageID, &senderEmail,
			&senderName, &senderDomain, &subject, &snippet, &reasonValue, &note, &statusValue,
			&blockScope, &reviewedBy, &reviewedAt, &resolution, &createdAt, &updatedAt) != nil {
			continue
		}
		item := map[string]any{
			"id": id, "tenantId": tenantID, "tenant": tenantName, "userId": userID, "userEmail": userEmail,
			"messageId": messageID, "senderEmail": senderEmail, "senderName": senderName,
			"senderDomain": senderDomain, "subject": subject, "snippet": snippet, "reason": reasonValue,
			"note": note, "status": statusValue, "blockScope": blockScope,
			"reviewedBy": reviewedBy, "resolution": resolution,
			"createdAt": createdAt.UTC().Format(time.RFC3339), "updatedAt": updatedAt.UTC().Format(time.RFC3339),
		}
		if reviewedAt != nil {
			item["reviewedAt"] = reviewedAt.UTC().Format(time.RFC3339)
		}
		reports = append(reports, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reports": reports, "summary": s.securityReportSummary(r.Context()),
	})
}

func (s *Server) handleSecurityReportDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var tenantID, userID, messageID, senderEmail, senderName, senderDomain, subject, snippet string
	var reason, note, statusValue, blockScope, reviewedBy, resolution string
	var reviewedAt *time.Time
	var createdAt, updatedAt time.Time
	err := s.db.QueryRow(r.Context(), `
		SELECT r.tenant_id::text, r.user_id::text, r.message_id, r.sender_email::text, r.sender_name,
		       r.sender_domain, r.subject, r.snippet, r.reason, r.note, r.status, r.block_scope,
		       r.reviewed_by, r.reviewed_at, r.resolution, r.created_at, r.updated_at
		FROM public.security_reports r WHERE r.id=$1
	`, id).Scan(&tenantID, &userID, &messageID, &senderEmail, &senderName, &senderDomain, &subject,
		&snippet, &reason, &note, &statusValue, &blockScope, &reviewedBy, &reviewedAt, &resolution,
		&createdAt, &updatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND", "Security report not found")
		return
	}
	report := map[string]any{
		"id": id, "tenantId": tenantID, "userId": userID, "messageId": messageID,
		"senderEmail": senderEmail, "senderName": senderName, "senderDomain": senderDomain,
		"subject": subject, "snippet": snippet, "reason": reason, "note": note, "status": statusValue,
		"blockScope": blockScope, "reviewedBy": reviewedBy, "resolution": resolution,
		"createdAt": createdAt.UTC().Format(time.RFC3339), "updatedAt": updatedAt.UTC().Format(time.RFC3339),
	}
	if reviewedAt != nil {
		report["reviewedAt"] = reviewedAt.UTC().Format(time.RFC3339)
	}
	message := map[string]any{}
	var bodyText, bodyHTML, toJSON, ccJSON, authJSON, fromName, fromEmail, messageSubject string
	var sentAt time.Time
	err = s.db.QueryRow(r.Context(), `
		SELECT subject, body_text, body_html, to_json::text, cc_json::text, auth::text,
		       from_name, from_email::text, sent_at
		FROM public.mail_messages WHERE id=$1 AND user_id=$2
	`, messageID, userID).Scan(&messageSubject, &bodyText, &bodyHTML, &toJSON, &ccJSON, &authJSON, &fromName, &fromEmail, &sentAt)
	if err == nil {
		message = map[string]any{
			"subject": messageSubject, "bodyText": bodyText, "bodyHtml": bodyHTML,
			"from": map[string]any{"name": fromName, "email": fromEmail},
			"to":   decodeJSONArray(toJSON), "cc": decodeJSONArray(ccJSON),
			"auth": decodeJSONObject(authJSON), "date": sentAt.UTC().Format(time.RFC3339),
		}
	}
	report["urlHosts"] = s.loadSecurityReportURLHosts(r.Context(), id, bodyHTML, bodyText)
	report["threatSharingEnabled"] = s.settingBool(r.Context(), threatShareSetting, false)
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "message": message})
}

// loadSecurityReportURLHosts returns the stored hosts for a report. Reports
// created before the threat-intelligence migration have no rows yet, so their
// stored message body is extracted lazily and persisted for the reviewer.
func (s *Server) loadSecurityReportURLHosts(ctx context.Context, reportID, bodyHTML, bodyText string) []map[string]any {
	hosts := s.querySecurityReportURLHosts(ctx, reportID)
	if len(hosts) > 0 || (strings.TrimSpace(bodyHTML) == "" && strings.TrimSpace(bodyText) == "") {
		return hosts
	}
	extracted := threatintel.ExtractURLHosts(bodyHTML, bodyText)
	if len(extracted) == 0 {
		return hosts
	}
	for _, host := range extracted {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO public.security_report_url_hosts (report_id, host, shared, approved)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (report_id, host) DO NOTHING
		`, reportID, host.Host, host.Shared, !host.Shared)
	}
	return s.querySecurityReportURLHosts(ctx, reportID)
}

func (s *Server) querySecurityReportURLHosts(ctx context.Context, reportID string) []map[string]any {
	rows, err := s.db.Query(ctx, `
		SELECT host, shared, approved FROM public.security_report_url_hosts
		WHERE report_id=$1 ORDER BY host
	`, reportID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	hosts := []map[string]any{}
	for rows.Next() {
		var host string
		var shared, approved bool
		if rows.Scan(&host, &shared, &approved) == nil {
			hosts = append(hosts, map[string]any{"host": host, "shared": shared, "approved": approved})
		}
	}
	return hosts
}

func (s *Server) handleSecurityReportReview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Status     string    `json:"status"`
		BlockScope string    `json:"blockScope"`
		Resolution string    `json:"resolution"`
		URLHosts   *[]string `json:"urlHosts"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status == "reopen" {
		status = "pending"
	}
	if status != "pending" && status != "confirmed" && status != "dismissed" {
		writeError(w, http.StatusBadRequest, "STATUS", "Status must be pending, confirmed or dismissed")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(body.BlockScope))
	if scope != "address" && scope != "domain" && scope != "none" {
		scope = "address"
	}
	if status == "dismissed" {
		scope = "none"
	}
	if status == "confirmed" && scope == "none" {
		scope = "address"
	}
	resolution := truncateText(strings.TrimSpace(body.Resolution), 500)
	approvedHosts := []string{}
	if status == "confirmed" && body.URLHosts != nil {
		approvedHosts = sanitizeURLHosts(*body.URLHosts)
	}

	s.threatMu.Lock()
	defer s.threatMu.Unlock()
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `
		UPDATE public.security_reports
		SET status=$2, block_scope=$3, resolution=$4, reviewed_by=$5,
		    reviewed_at=CASE WHEN $2='pending' THEN NULL ELSE now() END, updated_at=now()
		WHERE id=$1
	`, id, status, scope, resolution, sess.User.Email)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND", "Security report not found")
		return
	}
	if status == "confirmed" && body.URLHosts != nil {
		if _, err := tx.Exec(r.Context(), `
			UPDATE public.security_report_url_hosts
			SET approved = (host = ANY($2::text[]))
			WHERE report_id=$1
		`, id, approvedHosts); err != nil {
			writeFromError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	var sender, subject string
	_ = s.db.QueryRow(r.Context(), `SELECT sender_email::text, subject FROM public.security_reports WHERE id=$1`, id).Scan(&sender, &subject)
	syncErr := s.refreshThreatIndicatorsLocked(r.Context())
	if syncErr != nil {
		s.logger.Warn("could not synchronize threat indicators after review", "report", id, "err", syncErr)
	}
	s.actorAudit(r, sess, "security.report_reviewed", sender, "Reviewed a report as "+status,
		map[string]any{"reportId": id, "status": status, "blockScope": scope, "urlHosts": len(approvedHosts)})
	s.notifyEvent(r.Context(), "security.report_reviewed", map[string]any{
		"reportId": id, "sender": sender, "subject": subject, "status": status, "blockScope": scope,
	})
	s.hub.Broadcast("security.report_reviewed", map[string]any{"id": id, "status": status})
	response := map[string]any{"ok": true, "status": status, "blockScope": scope}
	if syncErr != nil {
		response["warning"] = "The review was saved, but Redis threat indicators could not be updated: " + syncErr.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

func sanitizeURLHosts(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, raw := range values {
		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if host == "" {
			continue
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
		if len(out) >= 200 {
			break
		}
	}
	return out
}

func (s *Server) handleSecurityReportUndo(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "UNDO") {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type UNDO to reverse a confirmed threat")
		return
	}
	id := pathID(r)
	s.threatMu.Lock()
	defer s.threatMu.Unlock()
	var sender, subject, statusValue string
	if err := s.db.QueryRow(r.Context(), `
		SELECT sender_email::text, subject, status FROM public.security_reports WHERE id=$1
	`, id).Scan(&sender, &subject, &statusValue); err != nil {
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND", "Security report not found")
		return
	}
	if statusValue != "confirmed" {
		writeError(w, http.StatusConflict, "NOT_CONFIRMED", "Only a confirmed threat can be undone")
		return
	}
	resolution := "Threat confirmation undone by reviewer; sender unblocked"
	tag, err := s.db.Exec(r.Context(), `
		UPDATE public.security_reports
		SET status='dismissed', block_scope='none', resolution=$2, reviewed_by=$3,
		    reviewed_at=now(), updated_at=now()
		WHERE id=$1 AND status='confirmed'
	`, id, resolution, sess.User.Email)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "NOT_CONFIRMED", "The report is no longer confirmed")
		return
	}
	syncErr := s.refreshThreatIndicatorsLocked(r.Context())
	if syncErr != nil {
		s.logger.Warn("could not synchronize threat indicators after undo", "report", id, "err", syncErr)
	}
	s.actorAudit(r, sess, "security.report_confirmation_undone", sender,
		"Undid a confirmed threat and removed the sender block",
		map[string]any{"reportId": id, "status": "dismissed", "blockScope": "none", "subject": subject})
	s.notifyEvent(r.Context(), "security.report_reviewed", map[string]any{
		"reportId": id, "sender": sender, "subject": subject, "status": "dismissed",
		"blockScope": "none", "undone": true,
	})
	s.hub.Broadcast("security.report_reviewed", map[string]any{"id": id, "status": "dismissed", "undone": true})
	response := map[string]any{"ok": true, "status": "dismissed", "blockScope": "none"}
	if syncErr != nil {
		response["warning"] = "The confirmation was undone, but Redis threat indicators could not be updated: " + syncErr.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) runSecurityReportMonitor(ctx context.Context) {
	check := func() {
		rows, err := s.db.Query(ctx, `
			UPDATE public.security_reports SET notified_at=now()
			WHERE notified_at IS NULL AND status IN ('pending','confirmed')
			RETURNING id, sender_email::text, sender_name, subject, reason, status, tenant_id::text, user_email::text
		`)
		if err != nil {
			return
		}
		changed := false
		for rows.Next() {
			var id, sender, senderName, subject, reason, status, tenantID, userEmail string
			if rows.Scan(&id, &sender, &senderName, &subject, &reason, &status, &tenantID, &userEmail) != nil {
				continue
			}
			changed = true
			data := map[string]any{
				"reportId": id, "sender": sender, "senderName": senderName, "subject": subject,
				"reason": reason, "status": status, "tenantId": tenantID, "userEmail": userEmail,
			}
			_, _ = s.db.Exec(ctx, `
				INSERT INTO control.notification_deliveries (handle_id,event,status,detail)
				VALUES (NULL,'security.report','notice',$1)
			`, truncateText(sender+" · "+subject+" · reported by "+userEmail, 1000))
			s.notifyEvent(ctx, "security.report", data)
			s.hub.Broadcast("security.report", data)
		}
		rows.Close()
		if changed {
			// A re-report resets an existing report to pending. Rebuild here so
			// confirmed indicators are retracted promptly, not only on the
			// five-minute reconciler.
			s.reconcileThreatIndicators(ctx)
		}
	}
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		check()
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func decodeJSONArray(raw string) []any {
	value := []any{}
	_ = json.Unmarshal([]byte(raw), &value)
	if value == nil {
		return []any{}
	}
	return value
}

func decodeJSONObject(raw string) map[string]any {
	value := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &value)
	if value == nil {
		return map[string]any{}
	}
	return value
}
