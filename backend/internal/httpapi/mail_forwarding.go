package httpapi

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) getForwarding(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	sieveState := stateMap(state, "sieve")
	forwarding := stateMap(sieveState, "forwarding")
	if len(forwarding) == 0 {
		forwarding = defaultForwarding()
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	writeJSON(w, http.StatusOK, map[string]any{
		"forwarding": publicForwarding(forwarding),
		"policy": map[string]any{
			"autoForwarding":     policy.Mail.AutoForwarding,
			"externalForwarding": policy.Mail.ExternalForwarding,
		},
	})
}

func (s *Server) saveForwarding(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body map[string]any
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	forwarding, errors := normalizeForwarding(body)
	if len(errors) > 0 {
		writeError(w, http.StatusBadRequest, "FORWARDING_VALIDATION", strings.Join(errors, "; "))
		return
	}
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
	if err := validateForwardingPolicy(forwarding, sess.User.Email, s.tenantMailDomains(r.Context(), sess.User.TenantID), policy.Mail); err != nil {
		writeFromError(w, err)
		return
	}
	var response map[string]any
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		existing := stateMap(state, "sieve")
		if len(existing) == 0 {
			existing = defaultSieve()
		}
		next := map[string]any{}
		for key, value := range existing {
			next[key] = value
		}
		next["forwarding"] = forwarding
		next["revision"] = stateInt(existing, "revision") + 1
		next["updatedAt"] = nowISO()
		script := renderSieveWithFolders(next, sess.User.Email, folderMapForSieve(state))
		if err := s.pushSieveScript(r.Context(), sess.User, script); err != nil {
			return failDetails(http.StatusBadGateway, "SIEVE_SERVICE", "Dovecot rejected the forwarding rule", map[string]any{"error": err.Error()})
		}
		next["script"] = script
		state["sieve"] = next
		appendStateAudit(state, "forwarding.updated", forwarding["target"].(string),
			stateString(stateMap(state, "user"), "name"),
			forwardingAuditDetail(forwarding), clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = map[string]any{
			"ok": true, "forwarding": publicForwarding(forwarding),
			"revision": next["revision"], "script": script,
		}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func defaultForwarding() map[string]any {
	return map[string]any{
		"enabled": false, "target": "", "keepCopy": true,
		"permanent": true, "start": "", "end": "",
	}
}

func normalizeForwarding(input map[string]any) (map[string]any, []string) {
	errors := []string{}
	target := strings.ToLower(strings.TrimSpace(stateString(input, "target")))
	enabled := stateBool(input, "enabled")
	keepCopy := true
	if value, ok := input["keepCopy"]; ok {
		keepCopy = value == true
	}
	permanent := true
	if value, ok := input["permanent"]; ok {
		permanent = value == true
	}
	start := strings.TrimSpace(stateString(input, "start"))
	end := strings.TrimSpace(stateString(input, "end"))
	if enabled {
		if target == "" || !sieveEmailPattern.MatchString(target) {
			errors = append(errors, "Enter a valid forwarding address")
		}
		if !permanent {
			if !sieveDatePattern.MatchString(start) || !sieveDatePattern.MatchString(end) {
				errors = append(errors, "Choose a start and end date (YYYY-MM-DD)")
			} else {
				if end < start {
					errors = append(errors, "The forwarding end date must be on or after the start date")
				}
				if end < time.Now().UTC().Format("2006-01-02") {
					errors = append(errors, "The forwarding period has already ended")
				}
			}
		}
	}
	return map[string]any{
		"enabled": enabled, "target": target, "keepCopy": keepCopy,
		"permanent": permanent, "start": start, "end": end,
		"updatedAt": nowISO(),
	}, errors
}

func validateForwardingPolicy(forwarding map[string]any, ownEmail string, domains map[string]bool, policy adminMailPolicy) error {
	if !stateBool(forwarding, "enabled") {
		return nil
	}
	if !policy.AutoForwarding {
		return fail(http.StatusForbidden, "FORWARDING_DISABLED", "Auto-forwarding is disabled by your organization")
	}
	target := strings.ToLower(stateString(forwarding, "target"))
	if target == strings.ToLower(ownEmail) {
		return fail(http.StatusBadRequest, "FORWARDING_SELF", "A mailbox cannot forward to itself")
	}
	if !sieveRedirectInternal(target, ownEmail, domains) && !policy.ExternalForwarding {
		return fail(http.StatusForbidden, "FORWARDING_EXTERNAL", "Forwarding to an external address is disabled by your organization")
	}
	return nil
}

func publicForwarding(forwarding map[string]any) map[string]any {
	return map[string]any{
		"enabled":   stateBool(forwarding, "enabled"),
		"target":    stateString(forwarding, "target"),
		"keepCopy":  forwarding["keepCopy"] != false,
		"permanent": forwarding["permanent"] != false,
		"start":     stateString(forwarding, "start"),
		"end":       stateString(forwarding, "end"),
		"updatedAt": stateString(forwarding, "updatedAt"),
	}
}

func forwardingAuditDetail(forwarding map[string]any) string {
	if !stateBool(forwarding, "enabled") {
		return "Disabled mail forwarding"
	}
	period := "permanent"
	if forwarding["permanent"] == false {
		period = stateString(forwarding, "start") + " to " + stateString(forwarding, "end")
	}
	keep := "keep a copy"
	if forwarding["keepCopy"] == false {
		keep = "do not keep a copy"
	}
	return "Forward to " + stateString(forwarding, "target") + " · " + period + " · " + keep
}

func renderForwarding(forwarding map[string]any) []string {
	if !stateBool(forwarding, "enabled") {
		return nil
	}
	target := stateString(forwarding, "target")
	if target == "" {
		return nil
	}
	out := []string{"# Mail forwarding"}
	dateTests := []string{}
	if forwarding["permanent"] == false {
		if start := stateString(forwarding, "start"); start != "" {
			dateTests = append(dateTests, `currentdate :value "ge" "date" `+sieveQuote(start))
		}
		if end := stateString(forwarding, "end"); end != "" {
			dateTests = append(dateTests, `currentdate :value "le" "date" `+sieveQuote(end))
		}
	}
	if len(dateTests) > 0 {
		out = append(out, "if "+joinSieveTests(dateTests)+" {")
		out = append(out, "  redirect "+sieveQuote(target)+";")
		if forwarding["keepCopy"] == false {
			out = append(out, "  stop;")
		}
		out = append(out, "}")
	} else {
		out = append(out, "redirect "+sieveQuote(target)+";")
		if forwarding["keepCopy"] == false {
			out = append(out, "stop;")
		}
	}
	return out
}
