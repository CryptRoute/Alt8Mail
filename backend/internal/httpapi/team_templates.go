package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var teamTemplateVariablePattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]{1,64})\s*\}\}`)

type teamTemplateRecord struct {
	ID        string
	TenantID  string
	TeamID    string
	Name      string
	Category  string
	Alias     string
	Subject   string
	BodyText  string
	BodyHTML  string
	Variables []string
	Version   int
	Active    bool
	UpdatedBy string
	UpdatedAt time.Time
	CreatedAt time.Time
}

func (s *Server) requireTeamTemplateEntitlement(w http.ResponseWriter) bool {
	if s.entitled("team_templates") || s.entitled("shared_inbox") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Team templates require a license with the team_templates or shared_inbox feature")
	return false
}

func (s *Server) teamCanManage(ctx context.Context, team teamMailboxRecord, user userRecord) bool {
	if user.Role == "owner" || user.Role == "admin" {
		return true
	}
	var owner bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.group_members
		WHERE group_id=$1 AND user_id=$2 AND member_role='owner')
	`, team.GroupID, user.ID).Scan(&owner)
	return owner
}

func extractTeamTemplateVariables(subject, bodyText, bodyHTML string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, source := range []string{subject, bodyText, bodyHTML} {
		for _, match := range teamTemplateVariablePattern.FindAllStringSubmatch(source, -1) {
			name := match[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
			if len(out) >= 30 {
				return out
			}
		}
	}
	return out
}

func (s *Server) teamTemplateRecord(ctx context.Context, tenantID, teamID, templateID string) (teamTemplateRecord, error) {
	var record teamTemplateRecord
	var updatedEmail string
	err := s.store.DB.QueryRow(ctx, `
		SELECT t.id, t.tenant_id::text, t.team_mailbox_id::text, t.name, t.category,
		       t.alias_email::text, t.subject, t.body_text, t.body_html, t.variables,
		       t.version, t.active, COALESCE(u.name,''), COALESCE(u.email::text,''), t.updated_at, t.created_at
		FROM public.team_templates t
		LEFT JOIN public.users u ON u.id=t.updated_by
		WHERE t.id=$1 AND t.tenant_id=$2 AND t.team_mailbox_id=$3
	`, strings.TrimSpace(templateID), tenantID, teamID).Scan(
		&record.ID, &record.TenantID, &record.TeamID, &record.Name, &record.Category,
		&record.Alias, &record.Subject, &record.BodyText, &record.BodyHTML, &record.Variables,
		&record.Version, &record.Active, &record.UpdatedBy, &updatedEmail, &record.UpdatedAt, &record.CreatedAt)
	return record, err
}

func (s *Server) teamTemplateForUser(ctx context.Context, user userRecord, templateID string) (teamTemplateRecord, teamMailboxRecord, error) {
	var teamID string
	err := s.store.DB.QueryRow(ctx, `
		SELECT team_mailbox_id::text FROM public.team_templates
		WHERE id=$1 AND tenant_id=$2
	`, strings.TrimSpace(templateID), user.TenantID).Scan(&teamID)
	if err != nil {
		return teamTemplateRecord{}, teamMailboxRecord{}, fail(http.StatusNotFound, "TEMPLATE_NOT_FOUND", "Template not found")
	}
	team, err := s.teamMailboxForUser(ctx, user, teamID)
	if err != nil {
		return teamTemplateRecord{}, team, fail(http.StatusNotFound, "TEMPLATE_NOT_FOUND", "Template not found")
	}
	record, err := s.teamTemplateRecord(ctx, user.TenantID, team.ID, templateID)
	if err != nil {
		return record, team, fail(http.StatusNotFound, "TEMPLATE_NOT_FOUND", "Template not found")
	}
	return record, team, nil
}

func teamTemplatePayload(record teamTemplateRecord) map[string]any {
	variables := make([]any, 0, len(record.Variables))
	for _, variable := range record.Variables {
		variables = append(variables, variable)
	}
	return map[string]any{
		"id": record.ID, "teamId": record.TeamID, "name": record.Name,
		"category": record.Category, "aliasEmail": record.Alias,
		"subject": record.Subject, "bodyText": record.BodyText, "bodyHtml": record.BodyHTML,
		"variables": variables, "version": record.Version, "active": record.Active,
		"updatedBy": map[string]any{"name": record.UpdatedBy},
		"updatedAt": record.UpdatedAt.UTC().Format(time.RFC3339),
		"createdAt": record.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) listTeamTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.URL.Query().Get("teamId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT t.id, t.name, t.category, t.alias_email::text, t.subject, t.variables, t.version,
		       t.active, COALESCE(u.name,''), t.updated_at
		FROM public.team_templates t
		LEFT JOIN public.users u ON u.id=t.updated_by
		WHERE t.team_mailbox_id=$1 AND t.tenant_id=$2 AND t.active
		  AND ($3 = '' OR t.category=$3)
		ORDER BY t.category, t.name
	`, team.ID, sess.User.TenantID, category)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEMPLATES", "Could not load team templates")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, cat, alias, subject, updatedBy string
		var variables []string
		var version int
		var active bool
		var updated time.Time
		if rows.Scan(&id, &name, &cat, &alias, &subject, &variables, &version, &active, &updatedBy, &updated) != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "category": cat, "aliasEmail": alias,
			"subject": subject, "variables": variables, "version": version,
			"active": active, "updatedBy": updatedBy, "updatedAt": updated.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": items, "count": len(items), "canManage": s.teamCanManage(r.Context(), team, sess.User)})
}

func (s *Server) getTeamTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.teamTemplateForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": teamTemplatePayload(record), "canManage": s.teamCanManage(r.Context(), team, sess.User)})
}

type teamTemplateBody struct {
	TeamID   string `json:"teamId"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Alias    string `json:"aliasEmail"`
	Subject  string `json:"subject"`
	BodyText string `json:"bodyText"`
	BodyHTML string `json:"bodyHtml"`
	Active   *bool  `json:"active"`
}

func (s *Server) validateTeamTemplateBody(ctx context.Context, user userRecord, team teamMailboxRecord, body teamTemplateBody) (teamTemplateBody, error) {
	name := truncate(strings.TrimSpace(body.Name), 100)
	if name == "" {
		return body, fmt.Errorf("template name is required")
	}
	if len(body.Subject) > 1000 || len(body.BodyText) > 64000 || len(body.BodyHTML) > 128000 {
		return body, fmt.Errorf("template content is too large")
	}
	alias := normalizeEmailValue(body.Alias)
	if alias != "" {
		if !looksLikeEmail(alias) {
			return body, fmt.Errorf("alias email is invalid")
		}
		var exists bool
		_ = s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM public.user_aliases WHERE tenant_id=$1 AND user_id=$2 AND lower(email::text)=$3 AND active)
		`, user.TenantID, team.OwnerID, alias).Scan(&exists)
		if !exists {
			return body, fmt.Errorf("alias must belong to the backing mailbox owner")
		}
	}
	body.Name = name
	body.Category = truncate(strings.TrimSpace(body.Category), 50)
	body.Alias = alias
	body.Subject = truncate(strings.TrimSpace(body.Subject), 1000)
	body.BodyText = truncate(body.BodyText, 64000)
	body.BodyHTML = truncate(body.BodyHTML, 128000)
	return body, nil
}

func (s *Server) createTeamTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body teamTemplateBody
	if !s.decodeBody(w, r, &body, 196*1024) {
		return
	}
	team, err := s.teamMailboxForUser(r.Context(), sess.User, body.TeamID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "TEMPLATE_FORBIDDEN", "Only a team owner or administrator can manage templates")
		return
	}
	body, err = s.validateTeamTemplateBody(r.Context(), sess.User, team, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "TEMPLATE", err.Error())
		return
	}
	variables := extractTeamTemplateVariables(body.Subject, body.BodyText, body.BodyHTML)
	id := newID("template")
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_templates
		  (id, tenant_id, team_mailbox_id, name, category, alias_email, subject, body_text, body_html,
		   variables, version, active, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,$11,$12,$12)
	`, id, sess.User.TenantID, team.ID, body.Name, body.Category, body.Alias, body.Subject,
		body.BodyText, body.BodyHTML, variables, active, sess.User.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "TEMPLATE_EXISTS", "A template with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "TEMPLATE", "Could not create the template")
		return
	}
	s.snapshotTeamTemplate(r.Context(), id, sess.User.ID)
	s.audit(r, sess.User, "team_template.created", id, "Created team template "+body.Name)
	record, _ := s.teamTemplateRecord(r.Context(), sess.User.TenantID, team.ID, id)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "template": teamTemplatePayload(record)})
}

func (s *Server) snapshotTeamTemplate(ctx context.Context, templateID, authorID string) {
	var version int
	var name, category, alias, subject, bodyText, bodyHTML string
	var active bool
	if err := s.store.DB.QueryRow(ctx, `
		SELECT version, name, category, alias_email::text, subject, body_text, body_html, active
		FROM public.team_templates WHERE id=$1
	`, templateID).Scan(&version, &name, &category, &alias, &subject, &bodyText, &bodyHTML, &active); err != nil {
		return
	}
	snapshot, _ := json.Marshal(map[string]any{
		"name": name, "category": category, "aliasEmail": alias, "subject": subject,
		"bodyText": bodyText, "bodyHtml": bodyHTML, "active": active,
	})
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.team_template_versions (template_id, version, snapshot, author_user_id)
		VALUES ($1,$2,$3::jsonb,$4) ON CONFLICT (template_id, version) DO NOTHING
	`, templateID, version, snapshot, authorID)
}

func (s *Server) updateTeamTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.teamTemplateForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "TEMPLATE_FORBIDDEN", "Only a team owner or administrator can manage templates")
		return
	}
	var body teamTemplateBody
	if !s.decodeBody(w, r, &body, 196*1024) {
		return
	}
	body.Name = firstNonEmpty(strings.TrimSpace(body.Name), record.Name)
	body.Category = firstNonEmpty(strings.TrimSpace(body.Category), record.Category)
	body.Alias = firstNonEmpty(strings.TrimSpace(body.Alias), record.Alias)
	body.Subject = firstNonEmpty(body.Subject, record.Subject)
	if body.BodyText == "" {
		body.BodyText = record.BodyText
	}
	if body.BodyHTML == "" {
		body.BodyHTML = record.BodyHTML
	}
	body, err = s.validateTeamTemplateBody(r.Context(), sess.User, team, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "TEMPLATE", err.Error())
		return
	}
	variables := extractTeamTemplateVariables(body.Subject, body.BodyText, body.BodyHTML)
	active := record.Active
	if body.Active != nil {
		active = *body.Active
	}
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.team_templates SET name=$2, category=$3, alias_email=$4, subject=$5,
		  body_text=$6, body_html=$7, variables=$8, version=version+1, active=$9,
		  updated_by=$10, updated_at=now()
		WHERE id=$1
	`, record.ID, body.Name, body.Category, body.Alias, body.Subject, body.BodyText,
		body.BodyHTML, variables, active, sess.User.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "TEMPLATE_EXISTS", "A template with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "TEMPLATE", "Could not update the template")
		return
	}
	s.snapshotTeamTemplate(r.Context(), record.ID, sess.User.ID)
	s.audit(r, sess.User, "team_template.updated", record.ID, "Updated team template "+body.Name)
	updated, _ := s.teamTemplateRecord(r.Context(), sess.User.TenantID, team.ID, record.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "template": teamTemplatePayload(updated)})
}

func (s *Server) deleteTeamTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.teamTemplateForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "TEMPLATE_FORBIDDEN", "Only a team owner or administrator can manage templates")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.team_templates SET active=false, updated_at=now() WHERE id=$1`, record.ID)
	s.audit(r, sess.User, "team_template.deleted", record.ID, "Archived team template "+record.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) teamTemplateVersions(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, _, err := s.teamTemplateForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT v.version, v.snapshot, COALESCE(u.name,''), v.created_at
		FROM public.team_template_versions v
		LEFT JOIN public.users u ON u.id=v.author_user_id
		WHERE v.template_id=$1 ORDER BY v.version DESC
	`, record.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEMPLATE_VERSIONS", "Could not load template versions")
		return
	}
	defer rows.Close()
	versions := []map[string]any{}
	for rows.Next() {
		var version int
		var snapshot []byte
		var author string
		var created time.Time
		if rows.Scan(&version, &snapshot, &author, &created) == nil {
			versions = append(versions, map[string]any{
				"version": version, "snapshot": json.RawMessage(snapshot),
				"author": author, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "currentVersion": record.Version})
}

func (s *Server) renderTeamTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.requireTeamTemplateEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, _, err := s.teamTemplateForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		Variables map[string]string `json:"variables"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	renderText := func(source string) string {
		return teamTemplateVariablePattern.ReplaceAllStringFunc(source, func(match string) string {
			parts := teamTemplateVariablePattern.FindStringSubmatch(match)
			if len(parts) != 2 {
				return match
			}
			if value, ok := body.Variables[parts[1]]; ok {
				return value
			}
			return match
		})
	}
	renderHTML := func(source string) string {
		return teamTemplateVariablePattern.ReplaceAllStringFunc(source, func(match string) string {
			parts := teamTemplateVariablePattern.FindStringSubmatch(match)
			if len(parts) != 2 {
				return match
			}
			if value, ok := body.Variables[parts[1]]; ok {
				return htmlEscapeTemplateValue(value)
			}
			return match
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": renderText(record.Subject), "bodyText": renderText(record.BodyText),
		"bodyHtml": renderHTML(record.BodyHTML), "aliasEmail": record.Alias,
		"version": record.Version, "teamId": record.TeamID,
	})
}

func htmlEscapeTemplateValue(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(value)
}

func (s *Server) registerTeamTemplateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/team-templates", s.requireUser(s.listTeamTemplates))
	mux.HandleFunc("POST /api/team-templates", s.requireUser(s.createTeamTemplate))
	mux.HandleFunc("GET /api/team-templates/{id}", s.requireUser(s.getTeamTemplate))
	mux.HandleFunc("PATCH /api/team-templates/{id}", s.requireUser(s.updateTeamTemplate))
	mux.HandleFunc("DELETE /api/team-templates/{id}", s.requireUser(s.deleteTeamTemplate))
	mux.HandleFunc("GET /api/team-templates/{id}/versions", s.requireUser(s.teamTemplateVersions))
	mux.HandleFunc("POST /api/team-templates/{id}/render", s.requireUser(s.renderTeamTemplate))
}
