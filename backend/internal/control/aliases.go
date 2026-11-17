package control

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (s *Server) registerAliasRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/aliases", s.require("aliases.read", s.handleAliases))
	mux.HandleFunc("POST /control/api/aliases", s.require("mail.manage", s.handleAliasCreate))
	mux.HandleFunc("PATCH /control/api/aliases/{id}", s.require("mail.manage", s.handleAliasUpdate))
	mux.HandleFunc("DELETE /control/api/aliases/{id}", s.require("mail.manage", s.handleAliasDelete))
	mux.HandleFunc("POST /control/api/aliases/preview", s.require("aliases.read", s.handleAliasPreview))
}

type aliasInput struct {
	Address     string   `json:"address"`
	Kind        string   `json:"kind"`
	Targets     []string `json:"targets"`
	Target      string   `json:"target"`
	Description string   `json:"description"`
	Active      *bool    `json:"active"`
}

type aliasRecord struct {
	ID          string
	Domain      string
	Address     string
	Kind        string
	Targets     []string
	Description string
	Active      bool
	CreatedAt   time.Time
}

func normalizeAliasInput(input aliasInput) (aliasInput, string, error) {
	input.Address = strings.ToLower(strings.TrimSpace(input.Address))
	if input.Kind == "" {
		if strings.HasPrefix(input.Address, "@") {
			input.Kind = "catch_all"
		} else {
			input.Kind = "forward"
		}
	}
	if input.Kind != "forward" && input.Kind != "catch_all" && input.Kind != "distribution" {
		return input, "", fmt.Errorf("kind must be forward, catch_all or distribution")
	}
	domain := ""
	if input.Kind == "catch_all" {
		domain = normalizeDomain(strings.TrimPrefix(input.Address, "@"))
		if !validDomain(domain) {
			return input, "", fmt.Errorf("catch-all address must be @domain")
		}
		input.Address = "@" + domain
	} else {
		parts := strings.Split(input.Address, "@")
		if len(parts) != 2 || !validLocalPart(parts[0]) || !validDomain(normalizeDomain(parts[1])) {
			return input, "", fmt.Errorf("enter a valid alias address")
		}
		domain = normalizeDomain(parts[1])
		input.Address = parts[0] + "@" + domain
	}
	if input.Target == "" && len(input.Targets) > 0 {
		input.Target = strings.Join(input.Targets, ",")
	}
	targets := []string{}
	seen := map[string]bool{}
	for _, value := range strings.Split(input.Target, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		if !sieveEmailPattern.MatchString(value) {
			return input, "", fmt.Errorf("target %q is not an email address", value)
		}
		seen[value] = true
		targets = append(targets, value)
	}
	if len(targets) == 0 {
		return input, "", fmt.Errorf("add at least one target address")
	}
	if len(targets) > 100 {
		return input, "", fmt.Errorf("a maximum of 100 targets is allowed")
	}
	sort.Strings(targets)
	input.Targets = targets
	input.Description = truncateText(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input.Description, "\r", " "), "\n", " ")), 500)
	return input, domain, nil
}

func (s *Server) aliasExists(ctx context.Context, address, exceptID string) bool {
	var count int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_aliases WHERE address=$1 AND ($2='' OR id<>NULLIF($2,'')::uuid)`, address, exceptID).Scan(&count)
	if count > 0 {
		return true
	}
	_ = s.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM public.mailboxes WHERE email=$1)
		     + (SELECT count(*) FROM public.user_aliases WHERE email=$1)
	`, address).Scan(&count)
	return count > 0
}

func (s *Server) loadAliases(ctx context.Context) ([]aliasRecord, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, domain::text, address::text, kind, targets, description, active, created_at
		FROM control.mail_aliases ORDER BY domain, address
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []aliasRecord{}
	for rows.Next() {
		var item aliasRecord
		if rows.Scan(&item.ID, &item.Domain, &item.Address, &item.Kind, &item.Targets, &item.Description, &item.Active, &item.CreatedAt) == nil {
			items = append(items, item)
		}
	}
	return items, rows.Err()
}

func (s *Server) handleAliases(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	domain := normalizeDomain(r.URL.Query().Get("domain"))
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, domain::text, address::text, kind, targets, description, active, created_at
		FROM control.mail_aliases
		WHERE ($1='' OR address::text ILIKE '%'||$1||'%' OR array_to_string(targets, ',') ILIKE '%'||$1||'%')
		  AND ($2='' OR domain=$2)
		ORDER BY domain, address
	`, query, domain)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, dom, address, kind, description string
		var targets []string
		var active bool
		var created time.Time
		if rows.Scan(&id, &dom, &address, &kind, &targets, &description, &active, &created) == nil {
			items = append(items, map[string]any{
				"id": id, "domain": dom, "address": address, "kind": kind, "targets": targets,
				"description": description, "active": active, "createdAt": created.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": items, "count": len(items)})
}

func (s *Server) handleAliasCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body aliasInput
	if err := decodeJSON(w, r, &body, 128<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	input, domain, err := normalizeAliasInput(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ALIAS", err.Error())
		return
	}
	if s.aliasExists(r.Context(), input.Address, "") {
		writeError(w, http.StatusConflict, "ALIAS", "That address or mailbox already exists")
		return
	}
	var tenantID string
	if err := s.db.QueryRow(r.Context(), `SELECT tenant_id::text FROM public.mail_domains WHERE domain=$1`, domain).Scan(&tenantID); err != nil {
		writeError(w, http.StatusBadRequest, "DOMAIN", "The alias domain is not managed by this panel")
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.mail_aliases (tenant_id,domain,address,kind,targets,description,active,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text
	`, tenantID, domain, input.Address, input.Kind, input.Targets, input.Description, active, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	changed, _ := s.ensureAliasMapQuery(r.Context())
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "mail.alias_created", input.Address, "Created a mail alias", map[string]any{"kind": input.Kind, "targets": input.Targets})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "address": input.Address})
}

func (s *Server) handleAliasUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body aliasInput
	if err := decodeJSON(w, r, &body, 128<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var current aliasRecord
	if err := s.db.QueryRow(r.Context(), `
		SELECT id::text, domain::text, address::text, kind, targets, description, active, created_at
		FROM control.mail_aliases WHERE id=$1
	`, id).Scan(&current.ID, &current.Domain, &current.Address, &current.Kind, &current.Targets, &current.Description, &current.Active, &current.CreatedAt); err != nil {
		writeFromError(w, err)
		return
	}
	if body.Address == "" {
		body.Address = current.Address
	}
	if body.Kind == "" {
		body.Kind = current.Kind
	}
	if body.Target == "" && len(body.Targets) == 0 {
		body.Target = strings.Join(current.Targets, ",")
	}
	input, domain, err := normalizeAliasInput(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ALIAS", err.Error())
		return
	}
	if s.aliasExists(r.Context(), input.Address, id) {
		writeError(w, http.StatusConflict, "ALIAS", "That address already exists")
		return
	}
	var tenantID string
	if err := s.db.QueryRow(r.Context(), `SELECT tenant_id::text FROM public.mail_domains WHERE domain=$1`, domain).Scan(&tenantID); err != nil {
		writeError(w, http.StatusBadRequest, "DOMAIN", "The alias domain is not managed by this panel")
		return
	}
	active := current.Active
	if input.Active != nil {
		active = *input.Active
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.mail_aliases SET tenant_id=$2, domain=$3, address=$4, kind=$5, targets=$6,
		  description=$7, active=$8, updated_at=now() WHERE id=$1
	`, id, tenantID, domain, input.Address, input.Kind, input.Targets, input.Description, active)
	if err != nil {
		writeFromError(w, err)
		return
	}
	changed, _ := s.ensureAliasMapQuery(r.Context())
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "mail.alias_updated", input.Address, "Updated a mail alias", map[string]any{"kind": input.Kind, "targets": input.Targets})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": input.Address})
}

func (s *Server) handleAliasDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var address string
	_ = s.db.QueryRow(r.Context(), `SELECT address::text FROM control.mail_aliases WHERE id=$1`, id).Scan(&address)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.mail_aliases WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Alias not found")
		return
	}
	s.actorAudit(r, sess, "mail.alias_deleted", address, "Deleted a mail alias", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": address})
}

func (s *Server) handleAliasPreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Action string     `json:"action"`
		ID     string     `json:"id"`
		Input  aliasInput `json:"input"`
	}
	if err := decodeJSON(w, r, &body, 128<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current, err := s.loadAliases(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	before := aliasLines(current)
	next := append([]aliasRecord{}, current...)
	switch strings.ToLower(body.Action) {
	case "create":
		input, domain, err := normalizeAliasInput(body.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ALIAS", err.Error())
			return
		}
		next = append(next, aliasRecord{Address: input.Address, Domain: domain, Kind: input.Kind, Targets: input.Targets, Active: input.Active == nil || *input.Active})
	case "update":
		input, domain, err := normalizeAliasInput(body.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ALIAS", err.Error())
			return
		}
		found := false
		for i := range next {
			if next[i].ID == body.ID {
				next[i].Address = input.Address
				next[i].Domain = domain
				next[i].Kind = input.Kind
				next[i].Targets = input.Targets
				next[i].Active = input.Active == nil || *input.Active
				found = true
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Alias not found")
			return
		}
	case "delete":
		filtered := []aliasRecord{}
		for _, alias := range next {
			if alias.ID != body.ID {
				filtered = append(filtered, alias)
			}
		}
		next = filtered
	default:
		writeError(w, http.StatusBadRequest, "ACTION", "action must be create, update or delete")
		return
	}
	after := aliasLines(next)
	diff := []string{}
	beforeSet := map[string]bool{}
	for _, line := range before {
		beforeSet[line] = true
	}
	afterSet := map[string]bool{}
	for _, line := range after {
		afterSet[line] = true
	}
	for _, line := range before {
		if !afterSet[line] {
			diff = append(diff, "- "+line)
		}
	}
	for _, line := range after {
		if !beforeSet[line] {
			diff = append(diff, "+ "+line)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"before": before, "after": after, "diff": diff,
		"postfixQuery": "SELECT public.postfix_alias_lookup('%s') AS target"})
}

func aliasLines(records []aliasRecord) []string {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		status := "disabled"
		if record.Active {
			status = "active"
		}
		lines = append(lines, fmt.Sprintf("%s -> %s [%s, %s]", record.Address, strings.Join(record.Targets, ", "), record.Kind, status))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return []string{"(no managed aliases)"}
	}
	return lines
}
