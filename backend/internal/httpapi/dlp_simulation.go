package httpapi

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) adminSimulateDLP(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Subject         string   `json:"subject"`
		BodyText        string   `json:"bodyText"`
		AttachmentNames []string `json:"attachmentNames"`
		Classification  string   `json:"classification"`
	}
	if !s.decodeBody(w, r, &body, 256*1024) {
		return
	}
	if body.Classification != "" && !validDLPClassification(body.Classification) {
		writeError(w, http.StatusBadRequest, "DLP_CLASSIFICATION", "Choose a valid classification")
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.ActiveTenantID)
	attachments := []string{}
	for _, name := range body.AttachmentNames {
		if trimmed := truncate(strings.TrimSpace(name), 240); trimmed != "" {
			attachments = append(attachments, trimmed)
		}
		if len(attachments) >= 50 {
			break
		}
	}
	classification, warnings, matches, blocked := evaluateOutboundDLP(policy,
		truncate(strings.TrimSpace(body.Subject), 500), truncate(body.BodyText, 32000), attachments, body.Classification)
	detectors := []string{}
	seen := map[string]bool{}
	for _, match := range matches {
		if match.Detector != "" && !seen[match.Detector] {
			seen[match.Detector] = true
			detectors = append(detectors, match.Detector)
		}
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.dlp_simulated", "DLP simulation",
		"Simulated outbound DLP rules", map[string]any{
			"tenantId": sess.ActiveTenantID, "blocked": blocked, "matches": len(matches),
		})
	writeJSON(w, http.StatusOK, map[string]any{
		"classification": classification, "warnings": warnings, "matches": matches,
		"blocked": blocked, "detectors": detectors,
		"redactedPreview": truncate(redactDLPDetectors(body.BodyText, policy), 1000),
	})
}

func (s *Server) adminListDLPReviews(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, rule_id, detector, match_preview, context, status, created_at, updated_at
		FROM public.dlp_false_positives
		WHERE tenant_id=$1 AND ($2='' OR status=$2)
		ORDER BY CASE WHEN status='open' THEN 0 ELSE 1 END, created_at DESC LIMIT 500
	`, sess.ActiveTenantID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DLP_REVIEW", "Could not load DLP reviews")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, ruleID, detector, preview, context, itemStatus string
		var created, updated time.Time
		if rows.Scan(&id, &ruleID, &detector, &preview, &context, &itemStatus, &created, &updated) == nil {
			items = append(items, map[string]any{
				"id": id, "ruleId": ruleID, "detector": detector, "matchPreview": preview,
				"context": context, "status": itemStatus,
				"createdAt": created.UTC().Format(time.RFC3339), "updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": items, "count": len(items)})
}

func (s *Server) adminCreateDLPReview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		RuleID       string `json:"ruleId"`
		Detector     string `json:"detector"`
		MatchPreview string `json:"matchPreview"`
		Context      string `json:"context"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	ruleID := truncate(strings.TrimSpace(body.RuleID), 120)
	if ruleID == "" {
		writeError(w, http.StatusBadRequest, "DLP_REVIEW", "Choose the rule to review")
		return
	}
	reviewID := newID("dlpreview")
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO public.dlp_false_positives (id, tenant_id, rule_id, detector, match_preview, context)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, reviewID, sess.ActiveTenantID, ruleID, truncate(strings.TrimSpace(body.Detector), 40),
		truncate(strings.TrimSpace(body.MatchPreview), 240), truncate(strings.TrimSpace(body.Context), 1000))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DLP_REVIEW", "Could not save the DLP review")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.dlp_review_created", ruleID, "Queued a DLP false-positive review", nil)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": reviewID})
}

func (s *Server) adminUpdateDLPReview(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Status string `json:"status"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "confirmed" && status != "false_positive" && status != "open" {
		writeError(w, http.StatusBadRequest, "DLP_REVIEW", "Status must be open, confirmed or false_positive")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.dlp_false_positives SET status=$3, reviewed_by=$4, updated_at=now()
		WHERE id=$1 AND tenant_id=$2
	`, r.PathValue("id"), sess.ActiveTenantID, status, sess.User.ID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "DLP_REVIEW", "Review not found")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.dlp_review_updated", r.PathValue("id"), "Reviewed a DLP finding", map[string]any{"status": status})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}
