package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) recordWorkspaceAudit(ctx context.Context, sess *sessionRecord, action, target, detail string) {
	if sess == nil {
		return
	}
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO workspace_audit (tenant_id, user_id, actor, action, target, detail)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sess.User.TenantID, sess.User.ID, sess.User.Name, action, truncate(target, 240), truncate(detail, 500))
}

func (s *Server) getWorkspaceAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 1 && parsed <= 500 {
			limit = parsed
		}
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, actor, action, target, detail, created_at
		FROM workspace_audit WHERE user_id = $1 ORDER BY id DESC LIMIT $2
	`, sess.User.ID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT", "Could not load workspace audit")
		return
	}
	defer rows.Close()
	entries := []map[string]any{}
	for rows.Next() {
		var id int64
		var actor, action, target, detail string
		var created time.Time
		if rows.Scan(&id, &actor, &action, &target, &detail, &created) == nil {
			entries = append(entries, map[string]any{
				"id": fmt.Sprint(id), "actor": actor, "action": action, "target": target,
				"detail": detail, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": entries, "count": len(entries)})
}

func (s *Server) directoryPeople(ctx context.Context, sess *sessionRecord, includeSelf bool) ([]map[string]any, error) {
	policy := s.tenantPolicyFor(ctx, sess.User.TenantID)
	people := []map[string]any{}
	showDirectory := policy.Directory.ContactSharing || webmailAdminRole(sess.User.Role)
	seen := map[string]bool{}
	if showDirectory {
		query := `
			SELECT u.name, u.email::text, u.role, u.initials, u.color, COALESCE(u.title,''),
			       COALESCE(u.department,''), COALESCE(t.name,'')
			FROM users u JOIN tenants t ON t.id = u.tenant_id
			WHERE u.tenant_id = $1 AND u.status = 'active' AND u.deleted_at IS NULL`
		args := []any{sess.User.TenantID}
		if !includeSelf {
			query += ` AND u.id <> $2`
			args = append(args, sess.User.ID)
		}
		query += ` ORDER BY u.name`
		rows, err := s.store.DB.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, email, role, initials, color, title, department, company string
			if rows.Scan(&name, &email, &role, &initials, &color, &title, &department, &company) == nil {
				seen[strings.ToLower(email)] = true
				people = append(people, map[string]any{
					"name": name, "email": strings.ToLower(email), "role": role, "company": company,
					"team": department, "title": title, "phone": "", "location": "", "reportsTo": "",
					"initials": initials, "color": color, "source": "directory",
				})
			}
		}
		rows.Close()
	}
	// Personal contacts always belong to the signed-in user, independent of the
	// organization directory policy. Auto-saved recipients must remain available
	// for compose autocomplete and the People page.
	rows, err := s.store.DB.Query(ctx, `
		SELECT email::text, name, company, title, phone, location, source
		FROM contacts WHERE user_id = $1 ORDER BY name, email
	`, sess.User.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var email, name, company, title, phone, location, source string
		if rows.Scan(&email, &name, &company, &title, &phone, &location, &source) == nil {
			email = strings.ToLower(email)
			if seen[email] {
				continue
			}
			seen[email] = true
			if source == "" {
				source = "contact"
			}
			people = append(people, map[string]any{
				"name": firstNonEmpty(name, email), "email": email, "role": "Contact", "company": company,
				"team": "", "title": title, "phone": phone, "location": location, "reportsTo": "",
				"initials": initialsForName(firstNonEmpty(name, email)), "color": colorForString(email),
				"source": source,
			})
		}
	}
	return people, rows.Err()
}

// maybeAutoAddExternalContacts implements the directory.autoAddExternal
// policy: external recipients of a message become personal contacts.
func (s *Server) maybeAutoAddExternalContacts(ctx context.Context, user userRecord, people []any) {
	policy := s.tenantPolicyFor(ctx, user.TenantID)
	if !policy.Directory.AutoAddExternal || len(people) == 0 {
		return
	}
	domains := s.tenantMailDomains(ctx, user.TenantID)
	for _, raw := range people {
		person, _ := raw.(map[string]any)
		if person == nil {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(stateString(person, "email")))
		if !calendarEmailPattern.MatchString(email) || domains[domainOf(email)] {
			continue
		}
		name := truncate(strings.TrimSpace(stateString(person, "name")), 240)
		if name == "" {
			name = email
		}
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO contacts (id, tenant_id, user_id, email, name, source, uid, resource_name, etag)
			VALUES ($1, $2, $3, $4, $5, 'auto', $1, $1 || '.vcf', md5($1 || ':' || now()::text))
			ON CONFLICT (user_id, email) DO NOTHING
		`, newID("contact"), user.TenantID, user.ID, email, name)
	}
}

func parseJSONObject(value string) map[string]any {
	object := map[string]any{}
	_ = json.Unmarshal([]byte(value), &object)
	return object
}
