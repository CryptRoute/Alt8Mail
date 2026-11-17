package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) recordWorkspaceAudit(ctx context.Context, sess *sessionRecord, action, target, detail string) {
	if sess == nil {
		return
	}
	target = truncate(target, 240)
	detail = truncate(detail, 500)
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO workspace_audit (tenant_id, user_id, actor, action, target, detail)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, sess.User.TenantID, sess.User.ID, sess.User.Name, action, target, detail)
	// Office and internal-sharing events also go to the tenant audit stream so
	// SIEM receivers see them alongside mailbox events. Other workspace events
	// remain in workspace_audit only, avoiding duplicate audit_log entries.
	if strings.HasPrefix(action, "office.") || strings.HasPrefix(action, "file.permission_") {
		s.insertAudit(sess.User, action, target, detail, "", "")
	}
}

func workspaceAuditWhere(userID, category, query string) (string, []any) {
	where := []string{"user_id = $1"}
	args := []any{userID}
	if category == "office" {
		where = append(where, "(action LIKE 'office.%' OR action LIKE 'file.permission_%')")
	} else if category == "files" {
		where = append(where, "(action LIKE 'file.%' OR action LIKE 'office.%' OR action LIKE 'storage.%')")
	}
	if query != "" {
		args = append(args, "%"+escapeLikePattern(truncate(query, 200))+"%")
		position := len(args)
		where = append(where, fmt.Sprintf(
			`(action ILIKE $%d ESCAPE '\' OR target ILIKE $%d ESCAPE '\' OR detail ILIKE $%d ESCAPE '\' OR actor ILIKE $%d ESCAPE '\')`,
			position, position, position, position))
	}
	return strings.Join(where, " AND "), args
}

func (s *Server) workspaceAuditEntries(ctx context.Context, userID, category, query string, limit, offset int) ([]map[string]any, int, error) {
	where, args := workspaceAuditWhere(userID, category, query)
	var total int
	if err := s.store.DB.QueryRow(ctx, `SELECT count(*) FROM workspace_audit WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, actor, action, target, detail, created_at
		FROM workspace_audit WHERE `+where+`
		ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	entries := []map[string]any{}
	for rows.Next() {
		var id int64
		var actor, action, target, detail string
		var created time.Time
		if rows.Scan(&id, &actor, &action, &target, &detail, &created) == nil {
			entries = append(entries, map[string]any{
				"id": fmt.Sprint(id), "ts": created.UTC().Format(time.RFC3339),
				"createdAt": created.UTC().Format(time.RFC3339),
				"actor":     actor, "action": action, "target": target, "detail": detail, "ip": "",
			})
		}
	}
	return entries, total, rows.Err()
}

// mergeWorkspaceAudit augments the app-state audit stream with the durable
// workspace_audit stream for categories that contain Office or Cabinet work.
func (s *Server) mergeWorkspaceAudit(ctx context.Context, userID, category string, entries []any) []any {
	if category != "" && category != "office" && category != "files" {
		return entries
	}
	workspaceEntries, _, err := s.workspaceAuditEntries(ctx, userID, category, "", 500, 0)
	if err != nil {
		return entries
	}
	for _, entry := range workspaceEntries {
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, _ := entries[i].(map[string]any)
		right, _ := entries[j].(map[string]any)
		return stateString(left, "ts") > stateString(right, "ts")
	})
	return entries
}

func (s *Server) getWorkspaceAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 1 && parsed <= 500 {
			limit = parsed
		}
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	entries, total, err := s.workspaceAuditEntries(r.Context(), sess.User.ID,
		strings.TrimSpace(r.URL.Query().Get("category")), strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT", "Could not load workspace audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries, "audit": entries, "count": len(entries),
		"total": total, "limit": limit, "offset": offset,
	})
}

func (s *Server) getWorkspaceAuditCSV(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	entries, _, err := s.workspaceAuditEntries(r.Context(), sess.User.ID,
		strings.TrimSpace(r.URL.Query().Get("category")), strings.TrimSpace(r.URL.Query().Get("q")), 5000, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT", "Could not export workspace audit")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="a8m-office-audit-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"time", "action", "target", "actor", "detail"})
	for _, entry := range entries {
		_ = writer.Write([]string{
			csvSafe(stateString(entry, "ts")), csvSafe(stateString(entry, "action")),
			csvSafe(stateString(entry, "target")), csvSafe(stateString(entry, "actor")),
			csvSafe(stateString(entry, "detail")),
		})
	}
	writer.Flush()
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
		SELECT id, email::text, name, company, title, phone, location, source,
		       address_book_id, birthday
		FROM contacts WHERE user_id = $1 ORDER BY name, email
	`, sess.User.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, email, name, company, title, phone, location, source, bookID string
		var birthday *time.Time
		if rows.Scan(&id, &email, &name, &company, &title, &phone, &location, &source,
			&bookID, &birthday) == nil {
			email = strings.ToLower(email)
			if seen[email] {
				continue
			}
			seen[email] = true
			if source == "" {
				source = "contact"
			}
			people = append(people, map[string]any{
				"id": id, "name": firstNonEmpty(name, email), "email": email, "role": "Contact", "company": company,
				"team": "", "title": title, "phone": phone, "location": location, "reportsTo": "",
				"initials": initialsForName(firstNonEmpty(name, email)), "color": colorForString(email),
				"source": source, "addressBookId": firstNonEmpty(bookID, "default"),
				"birthday": func() string {
					if birthday == nil {
						return ""
					}
					return birthday.Format("2006-01-02")
				}(),
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
			name = displayNameFromEmail(email)
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
