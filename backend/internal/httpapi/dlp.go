package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type dlpMatch struct {
	RuleID         string `json:"ruleId"`
	RuleName       string `json:"ruleName"`
	Term           string `json:"term"`
	Field          string `json:"field"`
	Action         string `json:"action"`
	Classification string `json:"classification"`
}

func dlpAttachmentNames(attachments []any) []string {
	names := []string{}
	for _, raw := range attachments {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		name := strings.TrimSpace(stateString(item, "name"))
		if name == "" {
			name = strings.TrimSpace(stateString(item, "filename"))
		}
		if name != "" {
			names = append(names, truncate(name, 240))
		}
	}
	return names
}

// evaluateOutboundDLP applies the tenant's outbound classification rules. It
// returns the effective classification, human-readable warnings and the
// matched rules. Blocking is a separate flag because a warn/label rule can
// still raise the classification.
func evaluateOutboundDLP(policy adminTenantPolicy, subject, bodyText string, attachmentNames []string, selected string) (string, []string, []dlpMatch, bool) {
	classification := canonicalDLPClassification(selected)
	if classification == "" {
		classification = canonicalDLPClassification(policy.DLP.DefaultClassification)
	}
	if classification == "" {
		classification = "Internal"
	}
	if !policy.DLP.Enabled {
		return classification, []string{}, []dlpMatch{}, false
	}
	subjectLower := strings.ToLower(subject)
	bodyLower := strings.ToLower(bodyText)
	attachmentLower := strings.ToLower(strings.Join(attachmentNames, "\n"))
	matches := []dlpMatch{}
	warnings := []string{}
	blocked := false
	seen := map[string]bool{}
	for _, rule := range policy.DLP.Rules {
		if !rule.Enabled {
			continue
		}
		direction := strings.ToLower(strings.TrimSpace(rule.Direction))
		if direction != "outbound" && direction != "both" {
			continue
		}
		fields := rule.Fields
		if len(fields) == 0 {
			fields = []string{"subject", "body"}
		}
		for _, rawTerm := range rule.Terms {
			term := strings.TrimSpace(rawTerm)
			if term == "" {
				continue
			}
			needle := strings.ToLower(term)
			for _, field := range fields {
				haystack := ""
				switch strings.ToLower(field) {
				case "subject":
					haystack = subjectLower
				case "body":
					haystack = bodyLower
				case "attachment":
					haystack = attachmentLower
				default:
					continue
				}
				if !strings.Contains(haystack, needle) {
					continue
				}
				key := rule.ID + "\x00" + strings.ToLower(term) + "\x00" + field
				if seen[key] {
					continue
				}
				seen[key] = true
				action := strings.ToLower(strings.TrimSpace(rule.Action))
				match := dlpMatch{
					RuleID: rule.ID, RuleName: rule.Name, Term: term, Field: field,
					Action: action, Classification: canonicalDLPClassification(rule.Classification),
				}
				matches = append(matches, match)
				if rank := dlpClassificationRank(match.Classification); rank > dlpClassificationRank(classification) {
					classification = match.Classification
				}
				if action == "block" {
					blocked = true
					warnings = append(warnings, "Blocked by \""+rule.Name+"\": \""+term+"\" appears in the "+field+".")
				} else if action == "warn" {
					warnings = append(warnings, "Policy \""+rule.Name+"\" matched \""+term+"\" in the "+field+".")
				}
			}
		}
	}
	return classification, warnings, matches, blocked
}

func (s *Server) dlpCheck(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Subject        string `json:"subject"`
		BodyText       string `json:"bodyText"`
		BodyHTML       string `json:"bodyHtml"`
		Attachments    []any  `json:"attachments"`
		Classification string `json:"classification"`
	}
	if !s.decodeBody(w, r, &body, 12*1024*1024) {
		return
	}
	bodyText := body.BodyText
	if bodyText == "" && body.BodyHTML != "" {
		bodyText = htmlToPlainText(body.BodyHTML)
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	classification, warnings, matches, blocked := evaluateOutboundDLP(
		policy, body.Subject, bodyText, dlpAttachmentNames(body.Attachments), body.Classification,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"allowed": !blocked, "blocked": blocked,
		"classification":        classification,
		"defaultClassification": policy.DLP.DefaultClassification,
		"labels":                dlpClassifications,
		"enabled":               policy.DLP.Enabled,
		"warnings":              warnings, "matches": matches,
	})
}

// enforceOutboxDLP runs at delivery time as well as at compose time. Policy
// changes made after a message was scheduled must not let it bypass the
// organization's current rules.
func (s *Server) enforceOutboxDLP(ctx context.Context, userID, tenantID, itemID string) error {
	var subject, bodyText, classification string
	var attachmentsJSON []byte
	err := s.store.DB.QueryRow(ctx, `
		SELECT subject, body_text, attachments, classification
		FROM outbox WHERE id=$1 AND user_id=$2
	`, itemID, userID).Scan(&subject, &bodyText, &attachmentsJSON, &classification)
	if err != nil {
		return nil
	}
	attachments, _ := decodeJSONAny(attachmentsJSON, []any{}).([]any)
	policy := s.tenantPolicyFor(ctx, tenantID)
	effective, warnings, matches, blocked := evaluateOutboundDLP(
		policy, subject, bodyText, dlpAttachmentNames(attachments), classification,
	)
	encodedWarnings, _ := json.Marshal(warnings)
	if blocked {
		message := "This message is blocked by your organization's data protection policy."
		restored := false
		restoreErr := s.withMailState(ctx, userRecord{ID: userID, TenantID: tenantID}, nil, false, func(state map[string]any) error {
			item := stateObjectByID(stateSlice(state, "outbox"), itemID)
			if item == nil {
				return nil
			}
			item["classification"] = effective
			item["dlpWarnings"] = warnings
			restoreDraft(state, item)
			state["outbox"] = stateRemoveByID(stateSlice(state, "outbox"), itemID)
			appendStateAudit(state, "send.dlp_blocked", subject,
				stateString(stateMap(state, "user"), "name"),
				truncate(message+" "+strings.Join(warnings, " "), 500),
				stateString(item, "ip"), stateString(item, "userAgent"))
			restored = true
			return nil
		})
		if restoreErr != nil || !restored {
			_, _ = s.store.DB.Exec(ctx, `
				UPDATE outbox
				SET delivery_state='blocked', last_error=$3, classification=$4,
				    dlp_warnings=$5::jsonb, updated_at=now()
				WHERE id=$1 AND user_id=$2
			`, itemID, userID, truncate(message+" "+strings.Join(warnings, " "), 500), effective, string(encodedWarnings))
		}
		return failDetails(http.StatusUnprocessableEntity, "DLP_BLOCKED", message,
			map[string]any{"matches": matches, "warnings": warnings, "classification": effective})
	}
	if effective != classification || len(warnings) > 0 {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE outbox SET classification=$3, dlp_warnings=$4::jsonb, updated_at=now()
			WHERE id=$1 AND user_id=$2
		`, itemID, userID, effective, string(encodedWarnings))
	}
	return nil
}

func (s *Server) registerDLPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/dlp/check", s.requireUser(s.dlpCheck))
}
