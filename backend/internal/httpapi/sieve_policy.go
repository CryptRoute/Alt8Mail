package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// tenantMailDomains returns every domain owned by the tenant. A redirect to
// one of these domains is an internal transfer even when the user's primary
// address sits on another verified domain.
func (s *Server) tenantMailDomains(ctx context.Context, tenantID string) map[string]bool {
	domains := map[string]bool{}
	rows, err := s.store.DB.Query(ctx, `SELECT lower(domain::text) FROM mail_domains WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return domains
	}
	defer rows.Close()
	for rows.Next() {
		var domain string
		if rows.Scan(&domain) == nil && domain != "" {
			domains[domain] = true
		}
	}
	return domains
}

func sieveRedirectInternal(target, userEmail string, domains map[string]bool) bool {
	targetDomain := domainOf(strings.TrimSpace(target))
	if targetDomain == "" {
		return false
	}
	if domains[targetDomain] {
		return true
	}
	return strings.EqualFold(targetDomain, domainOf(userEmail))
}

// validateSieveForwarding is the write-time policy gate. It rejects a rule
// rather than silently changing the user's intent, so the UI can explain why
// the redirect is unavailable.
func validateSieveForwarding(rules []any, userEmail string, domains map[string]bool, policy adminMailPolicy) []string {
	errors := []string{}
	for index, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule == nil || rule["enabled"] == false {
			continue
		}
		action, _ := rule["action"].(map[string]any)
		if stateString(action, "type") != "redirect" {
			continue
		}
		if sieveRedirectAllowed(stateString(action, "value"), userEmail, domains, policy) {
			continue
		}
		name := firstNonEmpty(stateString(rule, "name"), fmt.Sprintf("Rule %d", index+1))
		if sieveRedirectInternal(stateString(action, "value"), userEmail, domains) {
			errors = append(errors, name+": internal auto-forwarding is disabled by your organization")
		} else {
			errors = append(errors, name+": external auto-forwarding is disabled by your organization")
		}
	}
	return errors
}

func sieveRedirectAllowed(target, userEmail string, domains map[string]bool, policy adminMailPolicy) bool {
	if sieveRedirectInternal(target, userEmail, domains) {
		return policy.AutoForwarding
	}
	return policy.ExternalForwarding
}

// sieveWithForwardingPolicy clones a ruleset and disables redirects that the
// current tenant policy no longer permits. The stored rule is preserved; only
// the generated script is filtered, so the user can edit it if policy changes.
func sieveWithForwardingPolicy(sieve map[string]any, userEmail string, domains map[string]bool, policy adminMailPolicy) map[string]any {
	encoded, err := json.Marshal(sieve)
	if err != nil {
		return sieve
	}
	clone := map[string]any{}
	if json.Unmarshal(encoded, &clone) != nil {
		return sieve
	}
	rules, _ := clone["rules"].([]any)
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule == nil || rule["enabled"] == false {
			continue
		}
		action, _ := rule["action"].(map[string]any)
		if stateString(action, "type") != "redirect" {
			continue
		}
		if !sieveRedirectAllowed(stateString(action, "value"), userEmail, domains, policy) {
			rule["enabled"] = false
		}
	}
	return clone
}

// reapplyTenantSieve pushes a freshly filtered script for each active mailbox
// in the tenant. It runs in the background after an organization policy change;
// a large tenant must not hold the admin request open while every IMAP login is
// refreshed. Only one pass per tenant runs at a time; a change that arrives
// during a pass sets a pending flag so the final pass always reads the newest
// policy and cannot be overtaken by an older goroutine.
func (s *Server) reapplyTenantSieve(tenantID string) {
	if !s.mailSyncConfigured() {
		return
	}
	s.sieveSyncMu.Lock()
	if s.sieveSyncActive[tenantID] {
		s.sieveSyncAgain[tenantID] = true
		s.sieveSyncMu.Unlock()
		return
	}
	s.sieveSyncActive[tenantID] = true
	s.sieveSyncMu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			s.reapplyTenantSieveOnce(ctx, tenantID)
			cancel()
			s.sieveSyncMu.Lock()
			if s.sieveSyncAgain[tenantID] {
				s.sieveSyncAgain[tenantID] = false
				s.sieveSyncMu.Unlock()
				continue
			}
			delete(s.sieveSyncActive, tenantID)
			s.sieveSyncMu.Unlock()
			return
		}
	}()
}

func (s *Server) reapplyTenantSieveOnce(ctx context.Context, tenantID string) {
	policy := s.tenantPolicyFor(ctx, tenantID)
	domains := s.tenantMailDomains(ctx, tenantID)
	rows, err := s.store.DB.Query(ctx, `
			SELECT u.email::text FROM users u JOIN mailboxes m ON m.user_id = u.id
			WHERE u.tenant_id = $1 AND u.status = 'active' AND u.deleted_at IS NULL AND m.active = true
			ORDER BY u.email
		`, tenantID)
	if err != nil {
		s.logger.Warn("sieve policy reconciliation user list failed", "tenant", tenantID, "err", err)
		return
	}
	emails := []string{}
	for rows.Next() {
		var email string
		if rows.Scan(&email) == nil {
			emails = append(emails, email)
		}
	}
	rows.Close()
	updated := 0
	for _, email := range emails {
		if ctx.Err() != nil {
			s.logger.Warn("sieve policy reconciliation stopped", "tenant", tenantID, "err", ctx.Err())
			return
		}
		user, err := s.userByEmail(ctx, email)
		if err != nil {
			continue
		}
		state, err := s.loadState(ctx, user.ID)
		if err != nil {
			continue
		}
		sieve := stateMap(state, "sieve")
		if len(sieve) == 0 {
			continue
		}
		script := renderSieve(sieveWithForwardingPolicy(sieve, user.Email, domains, policy.Mail), user.Email)
		if err := s.pushSieveScript(ctx, user, script); err != nil {
			s.logger.Warn("sieve policy update failed", "user", email, "err", err)
			continue
		}
		_, _ = s.store.DB.Exec(ctx, `
				UPDATE app_states SET state = jsonb_set(state, '{sieve,script}', to_jsonb($2::text), true), updated_at = now()
				WHERE user_id = $1
			`, user.ID, script)
		updated++
	}
	s.logger.Info("sieve policy reconciliation complete", "tenant", tenantID, "users", updated)
}
