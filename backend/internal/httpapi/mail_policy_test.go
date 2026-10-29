package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestSieveForwardingPolicyDecision(t *testing.T) {
	domains := map[string]bool{"a8m.test": true, "a8m.example": true}
	externalOnly := defaultTenantPolicy().Mail
	externalOnly.AutoForwarding = false
	externalOnly.ExternalForwarding = true

	rules := []any{map[string]any{
		"name": "Forward", "enabled": true,
		"action": map[string]any{"type": "redirect", "value": "ops@a8m.example"},
	}}
	if errors := validateSieveForwarding(rules, "avery.chen@a8m.test", domains, externalOnly); len(errors) != 1 {
		t.Fatalf("internal forwarding should be blocked, got %v", errors)
	}
	rules[0].(map[string]any)["action"].(map[string]any)["value"] = "partner@example.test"
	if errors := validateSieveForwarding(rules, "avery.chen@a8m.test", domains, externalOnly); len(errors) != 0 {
		t.Fatalf("external forwarding should be allowed, got %v", errors)
	}

	internalOnly := externalOnly
	internalOnly.ExternalForwarding = false
	internalOnly.AutoForwarding = true
	if errors := validateSieveForwarding(rules, "avery.chen@a8m.test", domains, internalOnly); len(errors) != 1 {
		t.Fatalf("external forwarding should be blocked, got %v", errors)
	}
	filtered := sieveWithForwardingPolicy(map[string]any{"rules": rules}, "avery.chen@a8m.test", domains, internalOnly)
	rule := filtered["rules"].([]any)[0].(map[string]any)
	if rule["enabled"] != false {
		t.Fatal("blocked redirect was not disabled in the generated ruleset")
	}
	original := rules[0].(map[string]any)
	if original["enabled"] != true {
		t.Fatal("policy filtering mutated the stored ruleset")
	}
}

func TestQuarantineReleaseRequiresAdministrator(t *testing.T) {
	if !quarantineReleaseAction("notSpam", "") || !quarantineReleaseAction("archive", "") {
		t.Fatal("expected release actions")
	}
	if !quarantineReleaseAction("move", "inbox") {
		t.Fatal("moving to inbox should release quarantine")
	}
	if quarantineReleaseAction("move", "trash") || quarantineReleaseAction("delete", "") || quarantineReleaseAction("spam", "") {
		t.Fatal("containment actions must not be treated as releases")
	}
	if webmailAdminRole("member") || !webmailAdminRole("admin") || !webmailAdminRole("owner") {
		t.Fatal("webmail quarantine administration roles are wrong")
	}
}

func TestPasswordChangeRequiredByExpiryAndFlag(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	if passwordChangeRequired(userRecord{PasswordExpiresAt: &future}) {
		t.Fatal("future expiry should not force a change")
	}
	if !passwordChangeRequired(userRecord{PasswordExpiresAt: &past}) {
		t.Fatal("expired password should force a change")
	}
	if !passwordChangeRequired(userRecord{ForcePasswordChange: true, PasswordExpiresAt: &future}) {
		t.Fatal("administrator force flag should force a change")
	}
}

func TestSieveCommentCannotInjectStatements(t *testing.T) {
	stored := map[string]any{"rules": []any{map[string]any{
		"name": "Innocent\nredirect \"attacker@example.test\";", "enabled": true, "priority": 1,
		"conditions": []any{map[string]any{"field": "subject", "operator": "contains", "value": "x"}},
		"action":     map[string]any{"type": "keep"},
	}}}
	script := renderSieve(stored, "avery.chen@a8m.test")
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "redirect ") {
			t.Fatalf("rule name injected a Sieve statement:\n%s", script)
		}
	}
	_, _, errors := normalizeSieve(map[string]any{"rules": []any{map[string]any{
		"name": "bad\nname", "enabled": true,
		"conditions": []any{map[string]any{"field": "subject", "operator": "contains", "value": "x"}},
		"action":     map[string]any{"type": "keep"},
	}}}, []string{"inbox"})
	if len(errors) == 0 {
		t.Fatal("normalizeSieve accepted a rule name containing a line break")
	}
}
