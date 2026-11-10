package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func mcpWriteRequiredArgs(tool string, args map[string]any) error {
	required := map[string][]string{
		"calendar_write": {"title", "startAt", "endAt"},
		"tasks_write":    {"title"},
		"labels_write":   {"messageId", "label"},
		"draft_send":     {"to", "subject", "bodyText"},
	}
	for _, key := range required[tool] {
		if strings.TrimSpace(mcpArgString(args, key)) == "" {
			return fmt.Errorf("%s requires %s", tool, key)
		}
	}
	return nil
}

func (s *Server) mcpRequestWrite(r *http.Request, token mcpTokenRecord, tool string, args map[string]any, dryRun bool) (map[string]any, error) {
	if err := mcpWriteRequiredArgs(tool, args); err != nil {
		return nil, err
	}
	if dryRun {
		return mcpTextResult("Dry run: this action would create an approval request and would not execute until a signed-in user approves it.", map[string]any{
			"structuredContent": map[string]any{"status": "pending_approval", "dryRun": true, "tool": tool},
		}), nil
	}
	policy := s.tenantPolicyFor(r.Context(), token.TenantID)
	if !policy.MCP.WriteApprovalRequired {
		if err := s.executeMCPAction(r.Context(), token.User, tool, args); err != nil {
			return nil, err
		}
		s.audit(r, token.User, "mcp.action_executed", tool, "MCP write action executed under organization policy")
		return mcpTextResult("The organization allows this write action without review. It has been executed.", map[string]any{
			"structuredContent": map[string]any{"status": "executed", "tool": tool},
		}), nil
	}
	encoded, _ := json.Marshal(args)
	id := newID("mcpaction")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mcp_action_requests (id, tenant_id, user_id, connection_id, tool, arguments)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)
	`, id, token.TenantID, token.User.ID, token.ConnectionID, tool, encoded)
	if err != nil {
		return nil, fmt.Errorf("could not create the approval request")
	}
	s.audit(r, token.User, "mcp.action_requested", tool, "MCP write tool requested approval")
	return mcpTextResult("Approval required. A signed-in user must review this action before anything is created or sent.", map[string]any{
		"structuredContent": map[string]any{"status": "pending_approval", "requestId": id, "tool": tool},
	}), nil
}

func (s *Server) listMCPActionRequests(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT a.id, a.tool, a.arguments, a.status, a.decision_note, a.created_at, a.decided_at, cli.client_name
		FROM public.mcp_action_requests a
		JOIN public.mcp_connections c ON c.id=a.connection_id
		JOIN public.mcp_clients cli ON cli.id=c.client_id
		WHERE a.user_id=$1 ORDER BY a.created_at DESC LIMIT 100
	`, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "MCP", "Could not load MCP actions")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, tool, status, note, client string
		var args []byte
		var created time.Time
		var decided *time.Time
		if rows.Scan(&id, &tool, &args, &status, &note, &created, &decided, &client) == nil {
			item := map[string]any{"id": id, "tool": tool, "arguments": json.RawMessage(args), "status": status, "note": note, "clientName": client, "createdAt": created.UTC().Format(time.RFC3339)}
			if decided != nil {
				item["decidedAt"] = decided.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": items, "count": len(items)})
}

func (s *Server) decideMCPAction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	decision := strings.ToLower(strings.TrimSpace(body.Decision))
	if decision != "approve" && decision != "deny" {
		writeError(w, http.StatusBadRequest, "MCP_ACTION", "Decision must be approve or deny")
		return
	}
	id := r.PathValue("id")
	var tool string
	var args []byte
	if err := s.store.DB.QueryRow(r.Context(), `SELECT tool, arguments FROM public.mcp_action_requests WHERE id=$1 AND user_id=$2 AND status='pending'`, id, sess.User.ID).Scan(&tool, &args); err != nil {
		writeError(w, http.StatusNotFound, "MCP_ACTION", "Pending action not found")
		return
	}
	if decision == "deny" {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mcp_action_requests SET status='denied', decision_note=$3, decided_at=now() WHERE id=$1 AND user_id=$2`, id, sess.User.ID, truncate(strings.TrimSpace(body.Note), 500))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "denied"})
		return
	}
	var values map[string]any
	_ = json.Unmarshal(args, &values)
	if err := s.executeMCPAction(r.Context(), sess.User, tool, values); err != nil {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mcp_action_requests SET status='failed', decision_note=$3, decided_at=now() WHERE id=$1 AND user_id=$2`, id, sess.User.ID, truncate(err.Error(), 500))
		writeError(w, http.StatusBadRequest, "MCP_ACTION", err.Error())
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `UPDATE public.mcp_action_requests SET status='approved', decision_note=$3, decided_at=now() WHERE id=$1 AND user_id=$2`, id, sess.User.ID, truncate(strings.TrimSpace(body.Note), 500))
	s.audit(r, sess.User, "mcp.action_approved", tool, "Approved an MCP write action")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "approved"})
}

func (s *Server) executeMCPAction(ctx context.Context, user userRecord, tool string, args map[string]any) error {
	switch tool {
	case "calendar_write":
		start, err := time.Parse(time.RFC3339, mcpArgString(args, "startAt"))
		if err != nil {
			return fmt.Errorf("startAt must be RFC3339")
		}
		end, err := time.Parse(time.RFC3339, mcpArgString(args, "endAt"))
		if err != nil || !end.After(start) {
			return fmt.Errorf("endAt must be after startAt")
		}
		_, err = s.store.DB.Exec(ctx, `
			INSERT INTO public.calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, starts_at, ends_at, timezone, status)
			VALUES ($1,$2,$3,'default',$4,$5,$6,$7,$8,'UTC','confirmed')
		`, newID("event"), user.TenantID, user.ID, newID("uid"), truncate(mcpArgString(args, "title"), 200), truncate(mcpArgString(args, "description"), 2000), start, end)
		return err
	case "tasks_write":
		due := mcpArgString(args, "due")
		if due != "" && !taskDatePattern.MatchString(due) {
			return fmt.Errorf("due must be YYYY-MM-DD")
		}
		priority := firstNonEmpty(mcpArgString(args, "priority"), "normal")
		if priority != "low" && priority != "normal" && priority != "high" && priority != "urgent" {
			return fmt.Errorf("invalid task priority")
		}
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.tasks (id, tenant_id, user_id, title, notes, status, priority, due_date, source)
			VALUES ($1,$2,$3,$4,$5,'open',$6,NULLIF($7,'')::date,'mcp')
		`, newID("task"), user.TenantID, user.ID, truncate(mcpArgString(args, "title"), 200), truncate(mcpArgString(args, "notes"), 5000), priority, due)
		return err
	case "labels_write":
		messageID := mcpArgString(args, "messageId")
		label := truncate(mcpArgString(args, "label"), 80)
		if label == "" {
			return fmt.Errorf("label is required")
		}
		tag, err := s.store.DB.Exec(ctx, `
			UPDATE public.mail_messages SET categories = (
			  SELECT array_agg(DISTINCT value) FROM unnest(array_append(categories, $2)) AS value
			), updated_at=now() WHERE id=$1 AND user_id=$3
		`, messageID, label, user.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return fmt.Errorf("message not found")
		}
		return nil
	case "draft_send":
		to := mcpArgString(args, "to")
		subject := truncate(mcpArgString(args, "subject"), 1000)
		body := truncate(mcpArgString(args, "bodyText"), 100000)
		recipients := []map[string]any{}
		for _, raw := range strings.FieldsFunc(to, func(char rune) bool { return char == ',' || char == ';' }) {
			email := normalizeEmailValue(raw)
			if !looksLikeEmail(email) {
				return fmt.Errorf("invalid recipient %q", raw)
			}
			recipients = append(recipients, map[string]any{"email": email})
		}
		if len(recipients) == 0 || len(recipients) > 50 {
			return fmt.Errorf("use between 1 and 50 recipients")
		}
		classification, _, _, policyErr := s.validateProgrammaticSend(ctx, user, mcpRecipientList(to), nil, nil, subject, body, "Internal")
		if policyErr != nil {
			return policyErr
		}
		var accountID string
		if err := s.store.DB.QueryRow(ctx, `SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native' LIMIT 1`, user.ID).Scan(&accountID); err != nil {
			return fmt.Errorf("native mailbox is unavailable")
		}
		toJSON, _ := json.Marshal(recipients)
		fromJSON, _ := json.Marshal(map[string]any{"name": user.Name, "email": user.Email})
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.outbox
			  (id, tenant_id, user_id, from_json, to_json, subject, body_text, body_html, status, scheduled, send_at, account_id, classification)
			VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,'queued',true,now(),$9,$10)
		`, newID("send"), user.TenantID, user.ID, fromJSON, toJSON, subject, body, textToHTML(body), accountID, classification)
		return err
	}
	return fmt.Errorf("unsupported MCP action")
}

func (s *Server) registerMCPActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/action-requests", s.requireUser(s.listMCPActionRequests))
	mux.HandleFunc("POST /api/mcp/action-requests/{id}/decide", s.requireUser(s.decideMCPAction))
}
