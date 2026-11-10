package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) listTemplateGallery(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, name, category, description, subject, variables, sort_order, version
		FROM public.template_gallery WHERE active ORDER BY sort_order, name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GALLERY", "Could not load the template gallery")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, category, description, subject string
		var variables []string
		var sortOrder, version int
		if rows.Scan(&id, &name, &category, &description, &subject, &variables, &sortOrder, &version) == nil {
			items = append(items, map[string]any{
				"id": id, "name": name, "category": category, "description": description,
				"subject": subject, "variables": variables, "version": version,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": items, "count": len(items)})
}

func (s *Server) installGalleryTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body struct {
		TeamID string `json:"teamId"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	team, err := s.teamMailboxForUser(r.Context(), sess.User, body.TeamID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "GALLERY_FORBIDDEN", "Only a team owner or administrator can install templates")
		return
	}
	var name, category, subject, bodyText, bodyHTML string
	var variables []string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, category, subject, body_text, body_html, variables
		FROM public.template_gallery WHERE id=$1 AND active
	`, r.PathValue("id")).Scan(&name, &category, &subject, &bodyText, &bodyHTML, &variables); err != nil {
		writeError(w, http.StatusNotFound, "GALLERY_NOT_FOUND", "Gallery template not found")
		return
	}
	id := newID("template")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_templates
		  (id, tenant_id, team_mailbox_id, name, category, subject, body_text, body_html, variables, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
	`, id, sess.User.TenantID, team.ID, name, category, subject, bodyText, bodyHTML, variables, sess.User.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "GALLERY_EXISTS", "That template is already installed in this team")
			return
		}
		writeError(w, http.StatusInternalServerError, "GALLERY", "Could not install the template")
		return
	}
	s.snapshotTeamTemplate(r.Context(), id, sess.User.ID)
	s.audit(r, sess.User, "template_gallery.installed", id, "Installed gallery template "+name)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "templateId": id})
}

func (s *Server) validatePaymentLink(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		URL string `json:"url"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	if !policy.Mail.PaymentLinksEnabled {
		writeError(w, http.StatusForbidden, "PAYMENT_LINKS_DISABLED", "Payment links are disabled by your organization")
		return
	}
	raw := strings.TrimSpace(body.URL)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		writeError(w, http.StatusBadRequest, "PAYMENT_LINK", "Payment links must be HTTPS URLs")
		return
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := false
	for _, domain := range policy.Mail.PaymentLinkDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusBadRequest, "PAYMENT_LINK", "That payment host is not allowed by policy")
		return
	}
	s.audit(r, sess.User, "payment_link.validated", host, "Inserted a payment link")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": parsed.String(), "host": host})
}

func (s *Server) registerGalleryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/template-gallery", s.requireUser(s.listTemplateGallery))
	mux.HandleFunc("POST /api/template-gallery/{id}/install", s.requireUser(s.installGalleryTemplate))
	mux.HandleFunc("POST /api/payment-links/validate", s.requireUser(s.validatePaymentLink))
}
