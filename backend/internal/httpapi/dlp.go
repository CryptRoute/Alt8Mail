package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

var dlpEngineHooks enterprise.DLPEngineHooks

type dlpMatch struct {
	RuleID         string `json:"ruleId"`
	RuleName       string `json:"ruleName"`
	Detector       string `json:"detector"`
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
	if hook := dlpEngineHooks.Evaluate; hook != nil {
		classification, warnings, matches, blocked := hook(context.Background(), policy.tenantID, subject, bodyText, attachmentNames, selected)
		converted := make([]dlpMatch, 0, len(matches))
		for _, match := range matches {
			converted = append(converted, dlpMatch{
				RuleID: match.RuleID, RuleName: match.RuleName, Detector: match.Detector, Term: match.Term,
				Field: match.Field, Action: match.Action, Classification: match.Classification,
			})
		}
		return classification, warnings, converted, blocked
	}
	classification := canonicalDLPClassification(selected)
	if classification == "" {
		classification = canonicalDLPClassification(policy.DLP.DefaultClassification)
	}
	if classification == "" {
		classification = "Internal"
	}
	return classification, []string{}, []dlpMatch{}, false
}

func (s *Server) dlpCheck(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption.Mode == "client_side" {
		writeError(w, http.StatusForbidden, "ENCRYPTION_CLIENT_SIDE", "Server-side data protection checks are disabled in client-side encryption mode")
		return
	}
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
