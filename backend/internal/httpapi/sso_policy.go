package httpapi

import "strings"

// emailDomainAllowed reports whether email's domain exactly matches one entry
// in domains. Entries are trimmed, lowercased and may carry an optional leading
// "@"; duplicates and empty entries are ignored. An empty allowlist accepts any
// domain. OIDC and SAML share this rule so both providers apply identical
// exact-match semantics.
func emailDomainAllowed(domains []string, email string) bool {
	if len(domains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	for _, allowed := range domains {
		allowed = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(allowed, "@")))
		if allowed != "" && domain == allowed {
			return true
		}
	}
	return false
}

// policyRequiresMFA reports whether the tenant MFA enforcement setting requires
// strong MFA evidence for user: "all" for every user, "admins" for owner and
// admin roles. OIDC and SAML share this rule.
func policyRequiresMFA(policy adminTenantPolicy, user userRecord) bool {
	switch policy.Security.MFAEnforcement {
	case "all":
		return true
	case "admins":
		return user.Role == "owner" || user.Role == "admin"
	default:
		return false
	}
}
