package httpapi

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"
)

func (s *Server) adminListPhishingCampaigns(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT c.id, c.name, c.subject, c.status, c.created_at, c.launched_at,
		       count(t.id), count(t.id) FILTER (WHERE t.status IN ('opened','clicked')),
		       count(t.id) FILTER (WHERE t.status='reported')
		FROM public.phishing_campaigns c
		LEFT JOIN public.phishing_targets t ON t.campaign_id=c.id
		WHERE c.tenant_id=$1
		GROUP BY c.id ORDER BY c.created_at DESC LIMIT 200
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PHISHING", "Could not load campaigns")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, subject, status string
		var created time.Time
		var launched *time.Time
		var targets, engaged, reported int
		if rows.Scan(&id, &name, &subject, &status, &created, &launched, &targets, &engaged, &reported) == nil {
			item := map[string]any{
				"id": id, "name": name, "subject": subject, "status": status,
				"targets": targets, "engaged": engaged, "reported": reported,
				"createdAt": created.UTC().Format(time.RFC3339),
			}
			if launched != nil {
				item["launchedAt"] = launched.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": items})
}

func (s *Server) adminCreatePhishingCampaign(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Name        string   `json:"name"`
		Subject     string   `json:"subject"`
		BodyText    string   `json:"bodyText"`
		LandingNote string   `json:"landingNote"`
		Targets     []string `json:"targets"`
	}
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 120)
	subject := truncate(strings.TrimSpace(body.Subject), 200)
	bodyText := truncate(strings.TrimSpace(body.BodyText), 10000)
	if name == "" || subject == "" || bodyText == "" {
		writeError(w, http.StatusBadRequest, "PHISHING", "Name, subject and body are required")
		return
	}
	emails := []string{}
	for _, raw := range body.Targets {
		email := normalizeEmailValue(raw)
		if canMarkPhishingTarget(email) {
			emails = append(emails, email)
		}
		if len(emails) >= 500 {
			break
		}
	}
	emails = apiUniqueRecipients(emails)
	if len(emails) == 0 {
		writeError(w, http.StatusBadRequest, "PHISHING", "Add at least one valid target email")
		return
	}
	campaignID := newID("phish")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PHISHING", "Could not create the campaign")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO public.phishing_campaigns (id, tenant_id, name, subject, body_text, landing_note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, campaignID, sess.ActiveTenantID, name, subject, bodyText, truncate(strings.TrimSpace(body.LandingNote), 2000), sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "PHISHING", "Could not create the campaign")
		return
	}
	for _, email := range emails {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO public.phishing_targets (id, campaign_id, tenant_id, email, token)
			VALUES ($1,$2,$3,$4,$5)
		`, newID("ptarget"), campaignID, sess.ActiveTenantID, email, randomToken(32)); err != nil {
			writeError(w, http.StatusInternalServerError, "PHISHING", "Could not add campaign targets")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "PHISHING", "Could not save the campaign")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.phishing_campaign_created", campaignID,
		"Created a phishing simulation campaign", map[string]any{"targets": len(emails)})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": campaignID, "targets": len(emails)})
}

func canMarkPhishingTarget(email string) bool {
	return looksLikeEmail(email) && strings.Contains(email, "@")
}

func (s *Server) adminLaunchPhishingCampaign(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var subject, bodyText, name, status string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT subject, body_text, name, status FROM public.phishing_campaigns
		WHERE id=$1 AND tenant_id=$2
	`, r.PathValue("id"), sess.ActiveTenantID).Scan(&subject, &bodyText, &name, &status)
	if err != nil {
		writeError(w, http.StatusNotFound, "PHISHING", "Campaign not found")
		return
	}
	if status == "running" {
		writeError(w, http.StatusConflict, "PHISHING", "Campaign is already running")
		return
	}
	var accountID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native' LIMIT 1
	`, sess.User.ID).Scan(&accountID); err != nil {
		writeError(w, http.StatusConflict, "PHISHING", "An administrator native mailbox is required to launch the simulation")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, email::text, token FROM public.phishing_targets
		WHERE campaign_id=$1 AND status='queued'
	`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PHISHING", "Could not load campaign targets")
		return
	}
	type target struct{ id, email, token string }
	targets := []target{}
	for rows.Next() {
		var item target
		if rows.Scan(&item.id, &item.email, &item.token) == nil {
			targets = append(targets, item)
		}
	}
	rows.Close()
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		base = "https://alt8mail.local"
	}
	launched := 0
	for _, item := range targets {
		link := base + "/phish/" + item.token
		text := strings.ReplaceAll(bodyText, "{{link}}", link)
		text = strings.ReplaceAll(text, "{{email}}", item.email)
		fromJSON, _ := json.Marshal(map[string]any{"name": sess.User.Name, "email": sess.User.Email})
		toJSON, _ := json.Marshal([]map[string]any{{"email": item.email}})
		if _, err := s.store.DB.Exec(r.Context(), `
			INSERT INTO public.outbox
			  (id, tenant_id, user_id, from_json, to_json, subject, body_text, body_html, status, scheduled, send_at, account_id, priority, classification)
			VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,'queued',true,now(),$9,'normal','Internal')
		`, newID("phishsend"), sess.ActiveTenantID, sess.User.ID, fromJSON, toJSON,
			subject, text, textToHTML(text), accountID); err == nil {
			_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.phishing_targets SET status='sent', sent_at=now() WHERE id=$1`, item.id)
			launched++
		}
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE public.phishing_campaigns SET status='running', launched_at=coalesce(launched_at, now()) WHERE id=$1
	`, r.PathValue("id"))
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.phishing_campaign_launched", r.PathValue("id"),
		"Launched a phishing simulation campaign", map[string]any{"sent": launched})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": launched})
}

func (s *Server) adminPhishingReport(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT status, count(*) FROM public.phishing_targets
		WHERE campaign_id=$1 AND tenant_id=$2 GROUP BY status
	`, r.PathValue("id"), sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "PHISHING", "Campaign not found")
		return
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if rows.Scan(&status, &count) == nil {
			counts[status] = count
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaignId": r.PathValue("id"), "counts": counts})
}

func (s *Server) publicPhishingPage(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(strings.TrimSpace(r.PathValue("token")), "/report")
	report := strings.HasSuffix(r.URL.Path, "/report")
	var targetID, campaignName, landing, status string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT t.id, c.name, c.landing_note, t.status
		FROM public.phishing_targets t JOIN public.phishing_campaigns c ON c.id=t.campaign_id
		WHERE t.token=$1
	`, token).Scan(&targetID, &campaignName, &landing, &status)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if report {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE public.phishing_targets SET status='reported', reported_at=coalesce(reported_at, now()) WHERE id=$1
		`, targetID)
	} else if status != "reported" {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE public.phishing_targets SET status='clicked', clicked_at=coalesce(clicked_at, now()) WHERE id=$1 AND status <> 'reported'
		`, targetID)
	}
	message := "This was an authorized security-awareness simulation from your organization."
	if landing != "" {
		message = landing
	}
	if report {
		message = "Thank you for reporting the simulated message. Your security team uses this signal to improve awareness."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Security awareness</title><style>body{font-family:system-ui,sans-serif;background:#f3f6fb;color:#182436;display:grid;place-items:center;min-height:100vh;margin:0;padding:24px}main{max-width:560px;background:#fff;border:1px solid #dce4ef;border-radius:16px;padding:28px}h1{font-size:22px;margin:0 0 8px}p{line-height:1.55;color:#4d5f75}strong{color:#182436}</style></head><body><main><p style="font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:#6b7c93">Alt8Mail security awareness</p><h1>%s</h1><p>%s</p><p><strong>%s</strong> · If you did not expect this message, report it to your security team.</p></main></body></html>`,
		html.EscapeString("Security awareness simulation"), html.EscapeString(message), html.EscapeString(campaignName))
}

func (s *Server) registerPhishingPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /phish/{token}", s.publicPhishingPage)
	mux.HandleFunc("GET /phish/{token}/report", s.publicPhishingPage)
}

func (s *Server) registerPhishingAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /phishing/campaigns", s.adminFeature("phishing.read", FeatureSecurityAwareness, s.adminListPhishingCampaigns))
	mux.HandleFunc("POST /phishing/campaigns", s.adminFeature("phishing.manage", FeatureSecurityAwareness, s.adminCreatePhishingCampaign))
	mux.HandleFunc("POST /phishing/campaigns/{id}/launch", s.adminFeature("phishing.manage", FeatureSecurityAwareness, s.adminLaunchPhishingCampaign))
	mux.HandleFunc("GET /phishing/campaigns/{id}/report", s.adminFeature("phishing.read", FeatureSecurityAwareness, s.adminPhishingReport))
}
