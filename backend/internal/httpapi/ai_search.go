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

type aiSearchFilters struct {
	Keywords      string `json:"keywords"`
	Subject       string `json:"subject"`
	From          string `json:"from"`
	To            string `json:"to"`
	Folder        string `json:"folder"`
	Unread        *bool  `json:"unread"`
	Starred       *bool  `json:"starred"`
	HasAttachment *bool  `json:"hasAttachment"`
	After         string `json:"after"`
	Before        string `json:"before"`
}

func (f aiSearchFilters) empty() bool {
	return f.Keywords == "" && f.Subject == "" && f.From == "" && f.To == "" &&
		f.Folder == "" && f.Unread == nil && f.Starred == nil && f.HasAttachment == nil &&
		f.After == "" && f.Before == ""
}

func normalizeAISearchFilters(filters aiSearchFilters) aiSearchFilters {
	filters.Keywords = truncate(strings.TrimSpace(filters.Keywords), 200)
	filters.Subject = truncate(strings.TrimSpace(filters.Subject), 200)
	filters.From = truncate(strings.TrimSpace(filters.From), 200)
	filters.To = truncate(strings.TrimSpace(filters.To), 200)
	filters.Folder = truncate(strings.ToLower(strings.TrimSpace(filters.Folder)), 80)
	if !validSearchDate(filters.After) {
		filters.After = ""
	}
	if !validSearchDate(filters.Before) {
		filters.Before = ""
	}
	return filters
}

func validSearchDate(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func parseAISearchFilters(raw string) (aiSearchFilters, error) {
	filters := aiSearchFilters{}
	raw = strings.TrimSpace(raw)
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end > start {
			raw = raw[start : end+1]
		}
	}
	if err := json.Unmarshal([]byte(raw), &filters); err != nil {
		return filters, fmt.Errorf("AI search returned invalid JSON")
	}
	filters = normalizeAISearchFilters(filters)
	if filters.empty() {
		return filters, fmt.Errorf("AI search could not interpret the query")
	}
	return filters, nil
}

func deterministicSearchFilters(query string) aiSearchFilters {
	filters := aiSearchFilters{}
	lower := strings.ToLower(query)
	words := strings.Fields(lower)
	kept := []string{}
	for index := 0; index < len(words); index++ {
		word := strings.Trim(words[index], ".,;:!?()[]{}\"'")
		switch word {
		case "unread":
			value := true
			filters.Unread = &value
		case "read":
			value := false
			filters.Unread = &value
		case "starred":
			value := true
			filters.Starred = &value
		case "attachment", "attachments":
			value := true
			filters.HasAttachment = &value
		case "from":
			if index+1 < len(words) {
				filters.From = strings.Trim(words[index+1], ".,;:!?()[]{}\"'")
				index++
			}
		case "today":
			filters.After = time.Now().UTC().Format("2006-01-02")
		case "yesterday":
			filters.After = time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
			filters.Before = time.Now().UTC().Format("2006-01-02")
		case "last":
			if index+1 < len(words) {
				next := strings.Trim(words[index+1], ".,;:!?()[]{}\"'")
				now := time.Now().UTC()
				switch next {
				case "week":
					filters.After = now.AddDate(0, 0, -7).Format("2006-01-02")
					index++
				case "month":
					filters.After = now.AddDate(0, -1, 0).Format("2006-01-02")
					index++
				}
			}
		case "about", "email", "emails", "message", "messages", "show", "me", "find", "search", "that", "with":
			continue
		default:
			if strings.HasPrefix(word, "from:") && len(word) > 5 {
				filters.From = word[5:]
				continue
			}
			if word != "" {
				kept = append(kept, word)
			}
		}
	}
	filters.Keywords = strings.Join(kept, " ")
	if filters.Keywords == "" {
		// A quoted phrase is handled as raw keywords when no other term survives.
		if start := strings.Index(query, `"`); start >= 0 {
			if end := strings.Index(query[start+1:], `"`); end > 0 {
				filters.Keywords = query[start+1 : start+1+end]
			}
		}
	}
	return normalizeAISearchFilters(filters)
}

func (s *Server) aiSearch(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	query := truncate(strings.TrimSpace(body.Query), 500)
	if len([]rune(query)) < 2 {
		writeError(w, http.StatusBadRequest, "AI_SEARCH", "Type a longer search query")
		return
	}
	limit := body.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	settings, _ := s.aiSettingsFor(r.Context(), sess.User.TenantID)
	filters := aiSearchFilters{}
	aiUsed := false
	if settings.Enabled && settings.Model != "" {
		system := `You convert a natural-language mailbox search into JSON filters.
Return strict JSON only: {"keywords":"","subject":"","from":"","to":"","folder":"","unread":null,"starred":null,"hasAttachment":null,"after":"YYYY-MM-DD","before":"YYYY-MM-DD"}.
Use empty strings and null when the user did not specify a value. Never invent dates: resolve relative dates against the provided current date. Keywords are literal search terms, not SQL.`
		user := "Current date: " + time.Now().UTC().Format("2006-01-02") + "\nQuery: " + query
		raw, err := s.callAIForTenant(r.Context(), sess.User.TenantID, settings, system, user, 300, 0.1)
		if err != nil {
			if aiBudgetError(err) {
				writeError(w, http.StatusPaymentRequired, "AI_BUDGET", aiBudgetMessage(err))
				return
			}
			filters = deterministicSearchFilters(query)
		} else if parsed, err := parseAISearchFilters(raw); err == nil {
			filters = parsed
			aiUsed = true
		} else {
			filters = deterministicSearchFilters(query)
		}
	} else {
		filters = deterministicSearchFilters(query)
	}
	items, err := s.runAISearch(r.Context(), sess.User.TenantID, sess.User.ID, filters, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AI_SEARCH", "Could not search the mailbox")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": query, "filters": filters, "items": items, "citations": items,
		"count": len(items), "aiUsed": aiUsed, "explanation": describeAISearchFilters(filters),
	})
}

func (s *Server) runAISearch(ctx context.Context, tenantID, userID string, filters aiSearchFilters, limit int) ([]map[string]any, error) {
	where := []string{"m.user_id=$1", "m.tenant_id=$2"}
	args := []any{userID, tenantID}
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if filters.Keywords != "" {
		args = append(args, filters.Keywords, likePattern(filters.Keywords))
		where = append(where, fmt.Sprintf(`(m.search_vector @@ websearch_to_tsquery('simple', $%d) OR m.subject ILIKE $%d ESCAPE '\' OR m.body_text ILIKE $%d ESCAPE '\' OR m.from_name ILIKE $%d ESCAPE '\' OR m.from_email::text ILIKE $%d ESCAPE '\')`, len(args)-1, len(args), len(args), len(args), len(args)))
	}
	if filters.Subject != "" {
		add(`m.subject ILIKE $%d ESCAPE '\'`, likePattern(filters.Subject))
	}
	if filters.From != "" {
		pattern := likePattern(filters.From)
		args = append(args, pattern, pattern)
		where = append(where, fmt.Sprintf(`(m.from_email::text ILIKE $%d ESCAPE '\' OR m.from_name ILIKE $%d ESCAPE '\')`, len(args)-1, len(args)))
	}
	if filters.To != "" {
		add(`m.to_json::text ILIKE $%d ESCAPE '\'`, likePattern(filters.To))
	}
	if filters.Folder != "" {
		add(`lower(m.folder)=lower($%d)`, filters.Folder)
	}
	if filters.Unread != nil {
		add(`m.unread=$%d`, *filters.Unread)
	}
	if filters.Starred != nil {
		add(`m.starred=$%d`, *filters.Starred)
	}
	if filters.HasAttachment != nil {
		if *filters.HasAttachment {
			where = append(where, `jsonb_array_length(m.attachments) > 0`)
		} else {
			where = append(where, `jsonb_array_length(m.attachments) = 0`)
		}
	}
	if filters.After != "" {
		add(`m.sent_at >= $%d::date`, filters.After)
	}
	if filters.Before != "" {
		add(`m.sent_at < ($%d::date + interval '1 day')`, filters.Before)
	}
	args = append(args, limit)
	query := fmt.Sprintf(`
		SELECT m.id, m.subject, m.from_name, m.from_email::text, m.folder, m.snippet,
		       COALESCE(NULLIF(m.body_text,''), m.snippet), m.sent_at, m.unread, m.starred
		FROM public.mail_messages m
		WHERE %s AND NOT m.draft
		ORDER BY m.sent_at DESC LIMIT $%d
	`, strings.Join(where, " AND "), len(args))
	rows, err := s.store.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, subject, fromName, fromEmail, folder, snippet, body string
		var sent time.Time
		var unread, starred bool
		if rows.Scan(&id, &subject, &fromName, &fromEmail, &folder, &snippet, &body, &sent, &unread, &starred) == nil {
			excerpt := strings.TrimSpace(snippet)
			if excerpt == "" {
				excerpt = truncate(strings.TrimSpace(body), 240)
			}
			items = append(items, map[string]any{
				"id": id, "messageId": id, "subject": subject, "from": fromEmail,
				"fromName": fromName, "folder": folder, "excerpt": truncate(excerpt, 240),
				"sentAt": sent.UTC().Format(time.RFC3339), "unread": unread, "starred": starred,
			})
		}
	}
	return items, nil
}

func describeAISearchFilters(filters aiSearchFilters) string {
	parts := []string{}
	if filters.Keywords != "" {
		parts = append(parts, "matching "+strconv.Quote(filters.Keywords))
	}
	if filters.Subject != "" {
		parts = append(parts, "subject contains "+strconv.Quote(filters.Subject))
	}
	if filters.From != "" {
		parts = append(parts, "from "+filters.From)
	}
	if filters.To != "" {
		parts = append(parts, "to "+filters.To)
	}
	if filters.Folder != "" {
		parts = append(parts, "in "+filters.Folder)
	}
	if filters.Unread != nil {
		parts = append(parts, map[bool]string{true: "unread", false: "read"}[*filters.Unread])
	}
	if filters.Starred != nil {
		parts = append(parts, map[bool]string{true: "starred", false: "not starred"}[*filters.Starred])
	}
	if filters.HasAttachment != nil {
		parts = append(parts, map[bool]string{true: "with attachments", false: "without attachments"}[*filters.HasAttachment])
	}
	if filters.After != "" {
		parts = append(parts, "on or after "+filters.After)
	}
	if filters.Before != "" {
		parts = append(parts, "on or before "+filters.Before)
	}
	return "Searched mail " + strings.Join(parts, ", ")
}

func (s *Server) registerAISearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/ai/search", s.requireUser(s.aiSearch))
}
