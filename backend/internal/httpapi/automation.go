package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type automationConditions struct {
	From              string `json:"from,omitempty"`
	FromDomain        string `json:"fromDomain,omitempty"`
	SubjectContains   string `json:"subjectContains,omitempty"`
	RecipientContains string `json:"recipientContains,omitempty"`
	Category          string `json:"category,omitempty"`
	HasAttachment     *bool  `json:"hasAttachment,omitempty"`
}

type automationAction struct {
	Type     string `json:"type"`
	Value    string `json:"value,omitempty"`
	Folder   string `json:"folder,omitempty"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"`
	Due      string `json:"due,omitempty"`
	URL      string `json:"url,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Body     string `json:"body,omitempty"`
}

type automationRuleRecord struct {
	ID         string
	TenantID   string
	TeamID     string
	Name       string
	Enabled    bool
	Priority   int
	Conditions automationConditions
	Actions    []automationAction
	SieveText  string
	Version    int
	UpdatedBy  string
	UpdatedAt  time.Time
}

type automationMessage struct {
	ID              string
	TenantID        string
	UserID          string
	ThreadID        string
	Subject         string
	FromName        string
	FromEmail       string
	ToText          string
	Snippet         string
	Categories      []string
	AttachmentCount int
	SentAt          time.Time
}

func (s *Server) requireAutomationEntitlement(w http.ResponseWriter) bool {
	if s.entitled("automation") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Automation rules require a license with the automation feature")
	return false
}

func validateAutomationConditions(raw json.RawMessage) (automationConditions, error) {
	var conditions automationConditions
	if len(raw) == 0 {
		return conditions, errors.New("choose at least one condition")
	}
	if err := json.Unmarshal(raw, &conditions); err != nil {
		return conditions, errors.New("conditions must be a JSON object")
	}
	var probe map[string]any
	_ = json.Unmarshal(raw, &probe)
	for key := range probe {
		switch key {
		case "from", "fromDomain", "subjectContains", "recipientContains", "category", "hasAttachment":
		default:
			return conditions, fmt.Errorf("unknown condition %q", key)
		}
	}
	if conditions.From == "" && conditions.FromDomain == "" && conditions.SubjectContains == "" &&
		conditions.RecipientContains == "" && conditions.Category == "" && conditions.HasAttachment == nil {
		return conditions, errors.New("choose at least one condition")
	}
	conditions.From = truncate(strings.TrimSpace(conditions.From), 320)
	conditions.FromDomain = strings.TrimSpace(conditions.FromDomain)
	conditions.SubjectContains = truncate(strings.TrimSpace(conditions.SubjectContains), 200)
	conditions.RecipientContains = truncate(strings.TrimSpace(conditions.RecipientContains), 320)
	conditions.Category = truncate(strings.TrimSpace(conditions.Category), 80)
	for name, value := range map[string]string{
		"from": conditions.From, "fromDomain": conditions.FromDomain,
		"subjectContains": conditions.SubjectContains, "recipientContains": conditions.RecipientContains,
		"category": conditions.Category,
	} {
		if len(value) > 320 {
			return conditions, fmt.Errorf("%s is too long", name)
		}
	}
	return conditions, nil
}

func validateAutomationActions(raw json.RawMessage) ([]automationAction, error) {
	var actions []automationAction
	if len(raw) == 0 || string(raw) == "null" {
		return actions, errors.New("choose at least one action")
	}
	if err := json.Unmarshal(raw, &actions); err != nil || len(actions) == 0 {
		return actions, errors.New("actions must be a non-empty JSON array")
	}
	if len(actions) > 20 {
		return actions, errors.New("a rule may have at most 20 actions")
	}
	for index := range actions {
		action := &actions[index]
		action.Type = strings.ToLower(strings.TrimSpace(action.Type))
		switch action.Type {
		case "label":
			action.Value = truncate(strings.TrimSpace(action.Value), 80)
			if action.Value == "" {
				return actions, errors.New("label actions need a label")
			}
		case "move":
			action.Folder = truncate(strings.TrimSpace(action.Folder), 80)
			if action.Folder == "" {
				return actions, errors.New("move actions need a destination folder")
			}
		case "notify":
			action.Title = truncate(strings.TrimSpace(action.Title), 160)
			if action.Title == "" {
				action.Title = "Automation notified the team"
			}
		case "createtask":
			action.Type = "createTask"
			action.Title = truncate(strings.TrimSpace(action.Title), 200)
			if action.Title == "" {
				return actions, errors.New("task actions need a title")
			}
			if action.Priority == "" {
				action.Priority = "normal"
			}
			if action.Priority != "low" && action.Priority != "normal" && action.Priority != "high" && action.Priority != "urgent" {
				return actions, errors.New("task priority is invalid")
			}
		case "webhook":
			action.URL = strings.TrimSpace(action.URL)
			if err := validateAutomationWebhookURL(action.URL); err != nil {
				return actions, err
			}
		case "autoreply":
			action.Type = "autoReply"
			action.Subject = truncate(strings.TrimSpace(action.Subject), 200)
			action.Body = truncate(strings.TrimSpace(action.Body), 5000)
			if action.Body == "" {
				return actions, errors.New("auto-reply actions need a body")
			}
		default:
			return actions, fmt.Errorf("unknown action %q", action.Type)
		}
	}
	return actions, nil
}

func validateAutomationWebhookURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("webhook actions require an https:// URL")
	}
	if parsed.User != nil {
		return errors.New("webhook URLs cannot include credentials")
	}
	return nil
}

func compileAutomationSieve(conditions automationConditions, actions []automationAction) string {
	var tests []string
	if conditions.From != "" {
		tests = append(tests, fmt.Sprintf(`header :contains "from" %q`, conditions.From))
	}
	if conditions.FromDomain != "" {
		tests = append(tests, fmt.Sprintf(`address :domain :is "from" %q`, conditions.FromDomain))
	}
	if conditions.SubjectContains != "" {
		tests = append(tests, fmt.Sprintf(`header :contains "subject" %q`, conditions.SubjectContains))
	}
	if len(tests) == 0 {
		return ""
	}
	var commands []string
	for _, action := range actions {
		if action.Type == "move" && action.Folder != "" {
			commands = append(commands, fmt.Sprintf(`fileinto %q;`, action.Folder))
		}
	}
	if len(commands) == 0 {
		return ""
	}
	return "require [\"fileinto\"];\nif allof(" + strings.Join(tests, ", ") + ") {\n  " + strings.Join(commands, "\n  ") + "\n}\n"
}

func automationConditionsMatch(conditions automationConditions, message automationMessage) bool {
	from := strings.ToLower(message.FromEmail)
	if conditions.From != "" && !strings.Contains(from, strings.ToLower(conditions.From)) {
		return false
	}
	if conditions.FromDomain != "" {
		_, domain := splitEmail(from)
		if !strings.EqualFold(domain, conditions.FromDomain) {
			return false
		}
	}
	if conditions.SubjectContains != "" && !strings.Contains(strings.ToLower(message.Subject), strings.ToLower(conditions.SubjectContains)) {
		return false
	}
	if conditions.RecipientContains != "" && !strings.Contains(strings.ToLower(message.ToText), strings.ToLower(conditions.RecipientContains)) {
		return false
	}
	if conditions.Category != "" {
		found := false
		for _, category := range message.Categories {
			if strings.EqualFold(category, conditions.Category) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if conditions.HasAttachment != nil {
		if (message.AttachmentCount > 0) != *conditions.HasAttachment {
			return false
		}
	}
	return true
}

func (s *Server) automationRuleRecord(ctx context.Context, tenantID, teamID, ruleID string) (automationRuleRecord, error) {
	var record automationRuleRecord
	var conditions, actions []byte
	var updatedAt time.Time
	var updatedBy string
	err := s.store.DB.QueryRow(ctx, `
		SELECT r.id, r.tenant_id::text, r.team_mailbox_id::text, r.name, r.enabled, r.priority,
		       r.conditions, r.actions, r.sieve_text, r.version, COALESCE(u.name,''), r.updated_at
		FROM public.automation_rules r
		LEFT JOIN public.users u ON u.id=r.updated_by
		WHERE r.id=$1 AND r.tenant_id=$2 AND r.team_mailbox_id=$3
	`, strings.TrimSpace(ruleID), tenantID, teamID).Scan(
		&record.ID, &record.TenantID, &record.TeamID, &record.Name, &record.Enabled, &record.Priority,
		&conditions, &actions, &record.SieveText, &record.Version, &updatedBy, &updatedAt)
	if err != nil {
		return record, err
	}
	_ = json.Unmarshal(conditions, &record.Conditions)
	_ = json.Unmarshal(actions, &record.Actions)
	record.UpdatedBy = updatedBy
	record.UpdatedAt = updatedAt
	return record, nil
}

func (s *Server) automationRuleForUser(ctx context.Context, user userRecord, ruleID string) (automationRuleRecord, teamMailboxRecord, error) {
	var teamID string
	if err := s.store.DB.QueryRow(ctx, `SELECT team_mailbox_id::text FROM public.automation_rules WHERE id=$1 AND tenant_id=$2`, strings.TrimSpace(ruleID), user.TenantID).Scan(&teamID); err != nil {
		return automationRuleRecord{}, teamMailboxRecord{}, fail(http.StatusNotFound, "RULE_NOT_FOUND", "Automation rule not found")
	}
	team, err := s.teamMailboxForUser(ctx, user, teamID)
	if err != nil {
		return automationRuleRecord{}, team, fail(http.StatusNotFound, "RULE_NOT_FOUND", "Automation rule not found")
	}
	record, err := s.automationRuleRecord(ctx, user.TenantID, team.ID, ruleID)
	if err != nil {
		return record, team, fail(http.StatusNotFound, "RULE_NOT_FOUND", "Automation rule not found")
	}
	return record, team, nil
}

func automationRulePayload(record automationRuleRecord) map[string]any {
	return map[string]any{
		"id": record.ID, "teamId": record.TeamID, "name": record.Name, "enabled": record.Enabled,
		"priority": record.Priority, "conditions": record.Conditions, "actions": record.Actions,
		"sieveText": record.SieveText, "version": record.Version, "updatedBy": record.UpdatedBy,
		"updatedAt": record.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) listAutomationRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.URL.Query().Get("teamId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, name, enabled, priority, conditions, actions, sieve_text, version, updated_at
		FROM public.automation_rules WHERE team_mailbox_id=$1 AND tenant_id=$2
		ORDER BY priority, name
	`, team.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTOMATION", "Could not load automation rules")
		return
	}
	defer rows.Close()
	rules := []map[string]any{}
	for rows.Next() {
		var id, name, sieve string
		var enabled bool
		var priority, version int
		var conditions, actions []byte
		var updated time.Time
		if rows.Scan(&id, &name, &enabled, &priority, &conditions, &actions, &sieve, &version, &updated) != nil {
			continue
		}
		var conditionValue automationConditions
		var actionValues []automationAction
		_ = json.Unmarshal(conditions, &conditionValue)
		_ = json.Unmarshal(actions, &actionValues)
		rules = append(rules, map[string]any{
			"id": id, "name": name, "enabled": enabled, "priority": priority,
			"conditions": conditionValue, "actions": actionValues, "sieveText": sieve,
			"version": version, "updatedAt": updated.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "count": len(rules), "canManage": s.teamCanManage(r.Context(), team, sess.User)})
}

type automationRuleBody struct {
	TeamID     string          `json:"teamId"`
	Name       string          `json:"name"`
	Enabled    *bool           `json:"enabled"`
	Priority   *int            `json:"priority"`
	Conditions json.RawMessage `json:"conditions"`
	Actions    json.RawMessage `json:"actions"`
}

func (s *Server) createAutomationRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	var body automationRuleBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	team, err := s.teamMailboxForUser(r.Context(), sess.User, body.TeamID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "AUTOMATION_FORBIDDEN", "Only a team owner or administrator can manage automation")
		return
	}
	name := truncate(strings.TrimSpace(body.Name), 100)
	if name == "" {
		writeError(w, http.StatusBadRequest, "AUTOMATION", "Rule name is required")
		return
	}
	conditions, err := validateAutomationConditions(body.Conditions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "AUTOMATION", err.Error())
		return
	}
	actions, err := validateAutomationActions(body.Actions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "AUTOMATION", err.Error())
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	priority := 100
	if body.Priority != nil {
		priority = *body.Priority
	}
	if priority < 0 || priority > 10000 {
		writeError(w, http.StatusBadRequest, "AUTOMATION", "Priority must be 0–10000")
		return
	}
	var count int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM public.automation_rules WHERE team_mailbox_id=$1`, team.ID).Scan(&count)
	if count >= 50 {
		writeError(w, http.StatusConflict, "AUTOMATION_LIMIT", "A team inbox may have at most 50 automation rules")
		return
	}
	conditionJSON, _ := json.Marshal(conditions)
	actionJSON, _ := json.Marshal(actions)
	sieve := compileAutomationSieve(conditions, actions)
	id := newID("rule")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.automation_rules
		  (id, tenant_id, team_mailbox_id, name, enabled, priority, conditions, actions, sieve_text, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10,$10)
	`, id, sess.User.TenantID, team.ID, name, enabled, priority, conditionJSON, actionJSON, sieve, sess.User.ID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "AUTOMATION_EXISTS", "A rule with that name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "AUTOMATION", "Could not create the rule")
		return
	}
	s.audit(r, sess.User, "automation.rule_created", id, "Created automation rule "+name)
	record, _ := s.automationRuleRecord(r.Context(), sess.User.TenantID, team.ID, id)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "rule": automationRulePayload(record)})
}

func (s *Server) updateAutomationRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.automationRuleForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "AUTOMATION_FORBIDDEN", "Only a team owner or administrator can manage automation")
		return
	}
	var body automationRuleBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	name := firstNonEmpty(truncate(strings.TrimSpace(body.Name), 100), record.Name)
	conditionRaw, _ := json.Marshal(record.Conditions)
	if len(body.Conditions) > 0 {
		conditionRaw = body.Conditions
	}
	actionRaw, _ := json.Marshal(record.Actions)
	if len(body.Actions) > 0 {
		actionRaw = body.Actions
	}
	conditions, err := validateAutomationConditions(conditionRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "AUTOMATION", err.Error())
		return
	}
	actions, err := validateAutomationActions(actionRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "AUTOMATION", err.Error())
		return
	}
	enabled := record.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	priority := record.Priority
	if body.Priority != nil {
		priority = *body.Priority
	}
	if priority < 0 || priority > 10000 {
		writeError(w, http.StatusBadRequest, "AUTOMATION", "Priority must be 0–10000")
		return
	}
	conditionJSON, _ := json.Marshal(conditions)
	actionJSON, _ := json.Marshal(actions)
	sieve := compileAutomationSieve(conditions, actions)
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE public.automation_rules SET name=$2, enabled=$3, priority=$4, conditions=$5::jsonb,
		  actions=$6::jsonb, sieve_text=$7, version=version+1, updated_by=$8, updated_at=now()
		WHERE id=$1
	`, record.ID, name, enabled, priority, conditionJSON, actionJSON, sieve, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTOMATION", "Could not update the rule")
		return
	}
	s.audit(r, sess.User, "automation.rule_updated", record.ID, "Updated automation rule "+name)
	updated, _ := s.automationRuleRecord(r.Context(), sess.User.TenantID, team.ID, record.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": automationRulePayload(updated)})
}

func (s *Server) deleteAutomationRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.automationRuleForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.teamCanManage(r.Context(), team, sess.User) {
		writeError(w, http.StatusForbidden, "AUTOMATION_FORBIDDEN", "Only a team owner or administrator can manage automation")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM public.automation_rules WHERE id=$1`, record.ID)
	s.audit(r, sess.User, "automation.rule_deleted", record.ID, "Deleted automation rule "+record.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) automationMessageByID(ctx context.Context, tenantID, ownerID, messageID string) (automationMessage, error) {
	var message automationMessage
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, tenant_id::text, user_id::text, thread_id, subject, from_name, from_email::text,
		       to_text, snippet, categories,
		       (SELECT count(*) FROM public.mail_message_attachments a WHERE a.message_id=mail_messages.id),
		       sent_at
		FROM public.mail_messages WHERE id=$1 AND tenant_id=$2 AND user_id=$3
	`, strings.TrimSpace(messageID), tenantID, ownerID).Scan(
		&message.ID, &message.TenantID, &message.UserID, &message.ThreadID, &message.Subject,
		&message.FromName, &message.FromEmail, &message.ToText, &message.Snippet,
		&message.Categories, &message.AttachmentCount, &message.SentAt)
	return message, err
}

func (s *Server) dryRunAutomationRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	record, team, err := s.automationRuleForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	var body struct {
		MessageID string `json:"messageId"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	message, err := s.automationMessageByID(r.Context(), team.TenantID, team.OwnerID, body.MessageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "MESSAGE_NOT_FOUND", "Message not found")
		return
	}
	matched := automationConditionsMatch(record.Conditions, message)
	writeJSON(w, http.StatusOK, map[string]any{
		"matched": matched, "rule": automationRulePayload(record),
		"actions": record.Actions, "wouldRun": matched && record.Enabled,
		"message": map[string]any{"id": message.ID, "subject": message.Subject, "from": message.FromEmail},
	})
}

func (s *Server) listAutomationRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireAutomationEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.URL.Query().Get("teamId"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, rule_id, thread_id, message_id, status, dry_run, attempts, result, error, created_at, finished_at
		FROM public.automation_runs WHERE team_mailbox_id=$1 ORDER BY created_at DESC LIMIT 100
	`, team.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTOMATION_RUNS", "Could not load automation runs")
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id, ruleID, threadID, messageID, status, detail string
		var dryRun bool
		var attempts int
		var result []byte
		var created time.Time
		var finished *time.Time
		if rows.Scan(&id, &ruleID, &threadID, &messageID, &status, &dryRun, &attempts, &result, &detail, &created, &finished) != nil {
			continue
		}
		item := map[string]any{
			"id": id, "ruleId": ruleID, "threadId": threadID, "messageId": messageID,
			"status": status, "dryRun": dryRun, "attempts": attempts,
			"result": json.RawMessage(result), "error": detail,
			"createdAt": created.UTC().Format(time.RFC3339),
		}
		if finished != nil {
			item["finishedAt"] = finished.UTC().Format(time.RFC3339)
		}
		runs = append(runs, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "count": len(runs)})
}

func (s *Server) evaluateAutomationRule(ctx context.Context, team teamMailboxRecord, rule automationRuleRecord, message automationMessage, dryRun bool) (map[string]any, error) {
	matched := automationConditionsMatch(rule.Conditions, message)
	result := map[string]any{"matched": matched, "rule": rule.Name, "messageId": message.ID, "actionsRun": []string{}}
	if dryRun || !rule.Enabled || !matched {
		return result, nil
	}
	runID := newID("run")
	idempotencyKey := rule.ID + ":" + message.ID
	if _, err := s.store.DB.Exec(ctx, `
		INSERT INTO public.automation_runs
		  (id, tenant_id, team_mailbox_id, rule_id, thread_id, message_id, idempotency_key, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'running')
		ON CONFLICT (idempotency_key) DO NOTHING
	`, runID, team.TenantID, team.ID, rule.ID, message.ThreadID, message.ID, idempotencyKey); err != nil {
		return result, err
	}
	var stored bool
	_ = s.store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.automation_runs WHERE id=$1)`, runID).Scan(&stored)
	if !stored {
		result["duplicate"] = true
		return result, nil
	}
	ran := []string{}
	var actionErr error
	for _, action := range rule.Actions {
		if err := s.runAutomationAction(ctx, team, rule, message, action); err != nil {
			actionErr = err
			break
		}
		ran = append(ran, action.Type)
	}
	result["actionsRun"] = ran
	status := "completed"
	detail := ""
	if actionErr != nil {
		status = "failed"
		detail = truncate(actionErr.Error(), 800)
		result["error"] = detail
	}
	resultJSON, _ := json.Marshal(result)
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE public.automation_runs SET status=$2, result=$3::jsonb, error=$4, finished_at=now() WHERE id=$1
	`, runID, status, resultJSON, detail)
	return result, actionErr
}

func (s *Server) runAutomationAction(ctx context.Context, team teamMailboxRecord, rule automationRuleRecord, message automationMessage, action automationAction) error {
	switch action.Type {
	case "label":
		_, err := s.store.DB.Exec(ctx, `
			UPDATE public.mail_messages SET categories = (
			  SELECT array_agg(DISTINCT value) FROM unnest(array_append(categories, $2)) AS value
			), updated_at=now()
			WHERE id=$1 AND user_id=$3
		`, message.ID, action.Value, team.OwnerID)
		return err
	case "move":
		_, err := s.store.DB.Exec(ctx, `
			UPDATE public.mail_messages SET folder=$2, imap_folder=$2, updated_at=now()
			WHERE id=$1 AND user_id=$3
		`, message.ID, action.Folder, team.OwnerID)
		return err
	case "notify":
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications
			  (id, tenant_id, user_id, kind, title, body, detail, thread_id, owner_user_id)
			VALUES ($1,$2,$3,'automation',$4,$5,$6,$7,$3)
		`, newID("notification"), team.TenantID, team.OwnerID, action.Title,
			firstNonEmpty(message.Subject, "(no subject)"), "Automation: "+rule.Name, message.ThreadID)
		return err
	case "createTask":
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO public.tasks
			  (id, tenant_id, user_id, title, notes, status, priority, source, tags,
			   linked_thread_id, linked_subject)
			VALUES ($1,$2,$3,$4,$5,'open',$6,'automation',ARRAY['automation'],$7,$8)
		`, newID("task"), team.TenantID, team.OwnerID, action.Title,
			"Created by automation rule "+rule.Name, action.Priority, message.ThreadID, message.Subject)
		return err
	case "webhook":
		payload, _ := json.Marshal(map[string]any{
			"rule": map[string]any{"id": rule.ID, "name": rule.Name},
			"message": map[string]any{
				"id": message.ID, "threadId": message.ThreadID, "subject": message.Subject,
				"from": message.FromEmail, "snippet": message.Snippet, "sentAt": message.SentAt.UTC().Format(time.RFC3339),
			},
			"teamInbox":      map[string]any{"id": team.ID, "name": team.Name, "address": team.Address},
			"idempotencyKey": rule.ID + ":" + message.ID,
		})
		return s.postAutomationWebhook(ctx, action.URL, payload)
	case "autoReply":
		if strings.EqualFold(message.FromEmail, team.OwnerEmail) || strings.EqualFold(message.FromEmail, team.Address) {
			return errors.New("auto-reply skipped a message from the team mailbox itself")
		}
		subject := action.Subject
		if subject == "" {
			subject = "Re: " + message.Subject
		}
		owner, err := s.userByID(ctx, team.OwnerID)
		if err != nil {
			return errors.New("auto-reply owner mailbox is unavailable")
		}
		classification, _, _, err := s.validateProgrammaticSend(ctx, owner, []string{message.FromEmail}, nil, nil, subject, action.Body, "Internal")
		if err != nil {
			return fmt.Errorf("auto-reply blocked by send policy: %w", err)
		}
		var accountID string
		if err := s.store.DB.QueryRow(ctx, `SELECT id FROM public.mail_accounts WHERE user_id=$1 AND kind='native' LIMIT 1`, owner.ID).Scan(&accountID); err != nil {
			return errors.New("auto-reply native mailbox is unavailable")
		}
		var alreadyQueued bool
		_ = s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM public.outbox WHERE user_id=$1 AND source_message_id=$2 AND status IN ('queued','sending','sent'))
		`, owner.ID, message.ID).Scan(&alreadyQueued)
		if alreadyQueued {
			return nil
		}
		fromJSON, _ := json.Marshal(map[string]any{"name": owner.Name, "email": owner.Email})
		toJSON, _ := json.Marshal([]map[string]any{{"email": message.FromEmail, "name": message.FromName}})
		_, err = s.store.DB.Exec(ctx, `
			INSERT INTO public.outbox
			  (id, tenant_id, user_id, from_json, to_json, subject, body_text, body_html,
			   status, scheduled, send_at, account_id, classification, source_message_id, thread_id)
			VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,'queued',true,now(),$9,$10,$11,$12)
		`, newID("send"), owner.TenantID, owner.ID, fromJSON, toJSON, truncate(subject, 1000),
			truncate(action.Body, 100000), textToHTML(action.Body), accountID, classification,
			message.ID, message.ThreadID)
		if err == nil {
			s.insertAudit(owner, "automation.auto_reply_queued", message.ID, "Auto-reply queued by rule "+rule.Name, "", "automation")
		}
		return err
	}
	return errors.New("unsupported automation action")
}

func (s *Server) postAutomationWebhook(ctx context.Context, rawURL string, payload []byte) error {
	if s.automationPost != nil {
		return s.automationPost(ctx, rawURL, payload)
	}
	if err := validateAutomationWebhookURL(rawURL); err != nil {
		return err
	}
	parsed, _ := url.Parse(rawURL)
	if err := ensurePublicHost(parsed.Hostname()); err != nil {
		return errors.New("webhook host must resolve to a public network")
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout: 8 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many webhook redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("webhook redirects must stay on https")
			}
			return ensurePublicHost(req.URL.Hostname())
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Alt8Mail-Automation/1.0")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Server) automationWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanAutomationRules(ctx)
		}
	}
}

func (s *Server) scanAutomationRules(ctx context.Context) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT r.id, r.team_mailbox_id::text, r.tenant_id::text
		FROM public.automation_rules r
		JOIN public.team_mailboxes tm ON tm.id=r.team_mailbox_id AND tm.active
		WHERE r.enabled
		ORDER BY r.priority
	`)
	if err != nil {
		return
	}
	type pending struct{ ruleID, teamID, tenantID string }
	list := []pending{}
	for rows.Next() {
		var item pending
		if rows.Scan(&item.ruleID, &item.teamID, &item.tenantID) == nil {
			list = append(list, item)
		}
	}
	rows.Close()
	for _, item := range list {
		team, err := s.teamMailboxRecord(ctx, item.tenantID, item.teamID)
		if err != nil {
			continue
		}
		rule, err := s.automationRuleRecord(ctx, item.tenantID, team.ID, item.ruleID)
		if err != nil {
			continue
		}
		messages, err := s.store.DB.Query(ctx, `
			SELECT m.id, m.tenant_id::text, m.user_id::text, m.thread_id, m.subject, m.from_name,
			       m.from_email::text, m.to_text, m.snippet, m.categories,
			       (SELECT count(*) FROM public.mail_message_attachments a WHERE a.message_id=m.id),
			       m.sent_at
			FROM public.mail_messages m
			WHERE m.user_id=$1 AND m.folder='inbox' AND m.sent_at > now()-interval '14 days'
			  AND NOT EXISTS (SELECT 1 FROM public.automation_runs r WHERE r.rule_id=$2 AND r.message_id=m.id)
			ORDER BY m.sent_at DESC LIMIT 50
		`, team.OwnerID, rule.ID)
		if err != nil {
			continue
		}
		candidates := []automationMessage{}
		for messages.Next() {
			var message automationMessage
			if messages.Scan(&message.ID, &message.TenantID, &message.UserID, &message.ThreadID, &message.Subject,
				&message.FromName, &message.FromEmail, &message.ToText, &message.Snippet,
				&message.Categories, &message.AttachmentCount, &message.SentAt) == nil {
				candidates = append(candidates, message)
			}
		}
		messages.Close()
		for _, message := range candidates {
			if _, err := s.evaluateAutomationRule(ctx, team, rule, message, false); err != nil {
				break
			}
		}
	}
}

func (s *Server) registerAutomationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/automation/rules", s.requireUser(s.listAutomationRules))
	mux.HandleFunc("POST /api/automation/rules", s.requireUser(s.createAutomationRule))
	mux.HandleFunc("PATCH /api/automation/rules/{id}", s.requireUser(s.updateAutomationRule))
	mux.HandleFunc("DELETE /api/automation/rules/{id}", s.requireUser(s.deleteAutomationRule))
	mux.HandleFunc("POST /api/automation/rules/{id}/dry-run", s.requireUser(s.dryRunAutomationRule))
	mux.HandleFunc("GET /api/automation/runs", s.requireUser(s.listAutomationRuns))
}
