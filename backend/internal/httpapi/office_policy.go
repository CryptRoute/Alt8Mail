package httpapi

import (
	"context"
	"strings"
)

func splitEmailDomain(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return email[at+1:]
}

// officeUserAllowed applies the tenant Office policy to an internal user:
// tenant enablement, allowed groups (any) and allowed domains (any).
func (s *Server) officeUserAllowed(ctx context.Context, user userRecord) (adminOfficePolicy, bool) {
	policy := s.tenantPolicyFor(ctx, user.TenantID).Office
	if !policy.Enabled {
		return policy, false
	}
	if len(policy.AllowedDomains) > 0 {
		domain := splitEmailDomain(user.Email)
		allowed := false
		for _, candidate := range policy.AllowedDomains {
			if strings.EqualFold(strings.TrimSpace(candidate), domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			return policy, false
		}
	}
	if len(policy.AllowedGroups) > 0 {
		names := make([]string, 0, len(policy.AllowedGroups))
		for _, name := range policy.AllowedGroups {
			names = append(names, strings.ToLower(strings.TrimSpace(name)))
		}
		var member bool
		_ = s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM group_members gm
				JOIN groups g ON g.id = gm.group_id
				WHERE gm.user_id=$1 AND g.tenant_id=$2 AND g.status='active'
				  AND lower(g.name) = ANY($3::text[])
			)
		`, user.ID, user.TenantID, names).Scan(&member)
		if !member {
			return policy, false
		}
	}
	return policy, true
}

// officeGuestAllowed applies the tenant policy to an external Office link.
// Group restrictions are internal-only; domain restrictions still apply when
// the visitor supplied a verified recipient email.
func (s *Server) officeGuestAllowed(ctx context.Context, tenantID, email string) (adminOfficePolicy, bool) {
	policy := s.tenantPolicyFor(ctx, tenantID).Office
	if !policy.Enabled {
		return policy, false
	}
	if len(policy.AllowedDomains) > 0 {
		domain := splitEmailDomain(email)
		if domain == "" {
			return policy, false
		}
		allowed := false
		for _, candidate := range policy.AllowedDomains {
			if strings.EqualFold(strings.TrimSpace(candidate), domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			return policy, false
		}
	}
	return policy, true
}
