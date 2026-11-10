package httpapi

import (
	"context"
	"errors"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var safeBrandColor = func(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 7 && strings.HasPrefix(value, "#") {
		for _, char := range value[1:] {
			if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
				return "#0f6cbd"
			}
		}
		return value
	}
	return "#0f6cbd"
}

var brandingHostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// normalizeBrandingHost strips a port and a trailing dot and lowercases the
// hostname. It never adds a scheme; invalid forms are rejected later.
func normalizeBrandingHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.TrimSuffix(host, ".")
}

func validBrandingHost(host string) bool {
	return host != "" && len(host) <= 253 && net.ParseIP(host) == nil && brandingHostnamePattern.MatchString(host)
}

func validBrandingURL(raw string) bool {
	if len(raw) > 512 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return true
}

// normalizeBrandingPolicy trims and validates the extended white-label fields
// and returns the normalized policy plus fixed validation errors.
func normalizeBrandingPolicy(branding adminBrandingPolicy) (adminBrandingPolicy, []string) {
	errorsList := []string{}
	branding.LogoURL = strings.TrimSpace(branding.LogoURL)
	branding.SupportURL = strings.TrimSpace(branding.SupportURL)
	branding.LegalURL = strings.TrimSpace(branding.LegalURL)
	for field, value := range map[string]string{
		"Logo URL": branding.LogoURL, "Support URL": branding.SupportURL, "Legal URL": branding.LegalURL,
	} {
		if value != "" && !validBrandingURL(value) {
			errorsList = append(errorsList, field+" must be an absolute https:// URL without credentials, up to 512 characters")
		}
	}
	hosts := []string{}
	seen := map[string]bool{}
	for _, raw := range branding.WebmailHosts {
		host := normalizeBrandingHost(raw)
		if host == "" {
			continue
		}
		if !validBrandingHost(host) {
			errorsList = append(errorsList, "Custom webmail hostname is not a valid DNS name")
			continue
		}
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	if len(hosts) > 5 {
		errorsList = append(errorsList, "At most 5 custom webmail hostnames are allowed")
		hosts = hosts[:5]
	}
	branding.WebmailHosts = hosts
	return branding, errorsList
}

// applyBrandingEntitlement strips paid white-label fields for deployments
// without a valid white_label entitlement. Existing product/color/support/
// message branding remains free.
func (s *Server) applyBrandingEntitlement(policy adminTenantPolicy) adminTenantPolicy {
	if s.entitled("white_label") {
		return policy
	}
	policy.Branding.LogoURL = ""
	policy.Branding.SupportURL = ""
	policy.Branding.LegalURL = ""
	policy.Branding.HideAttribution = false
	policy.Branding.WebmailHosts = []string{}
	return policy
}

func (s *Server) publicBrandingPayload(policy adminTenantPolicy) map[string]any {
	policy = s.applyBrandingEntitlement(policy)
	product := truncate(strings.TrimSpace(policy.Branding.ProductName), 60)
	if product == "" {
		product = "Alt8Mail"
	}
	support := truncate(strings.TrimSpace(policy.Branding.SupportEmail), 200)
	loginMessage := truncate(strings.TrimSpace(policy.Branding.LoginMessage), 240)
	return map[string]any{
		"productName": product, "primaryColor": safeBrandColor(policy.Branding.PrimaryColor),
		"supportEmail": support, "loginMessage": loginMessage,
		"logoUrl": truncate(policy.Branding.LogoURL, 512), "supportUrl": truncate(policy.Branding.SupportURL, 512),
		"legalUrl": truncate(policy.Branding.LegalURL, 512), "hideAttribution": policy.Branding.HideAttribution,
	}
}

// tenantBrandingForHost resolves branding by custom webmail hostname first,
// then by the tenant's mail domain. Unknown hosts get the default identity.
func (s *Server) tenantBrandingForHost(ctx context.Context, rawHost string) (adminTenantPolicy, bool) {
	host := normalizeBrandingHost(rawHost)
	if host == "" || s.store == nil || s.store.DB == nil {
		return defaultTenantPolicy(), false
	}
	var raw []byte
	err := s.store.DB.QueryRow(ctx, `
		SELECT t.settings FROM public.tenants t
		JOIN public.tenant_webmail_hosts h ON h.tenant_id=t.id
		WHERE h.hostname=lower($1) AND t.status='active'
		LIMIT 1
	`, host).Scan(&raw)
	if err == nil {
		return tenantPolicyFromSettings(raw), true
	}
	err = s.store.DB.QueryRow(ctx, `
		SELECT t.settings FROM public.tenants t
		JOIN public.mail_domains d ON d.tenant_id=t.id
		WHERE lower(d.domain::text)=lower($1) AND t.status='active'
		LIMIT 1
	`, host).Scan(&raw)
	if err == nil {
		return tenantPolicyFromSettings(raw), true
	}
	return defaultTenantPolicy(), false
}

// validateBrandingHostsDB checks global hostname ownership and the Control
// Plane Web Net route/certificate inventory. Control remains authoritative for
// DNS, TLS and nginx.
func (s *Server) validateBrandingHostsDB(ctx context.Context, tenantID string, hosts []string) []string {
	errorsList := []string{}
	if len(hosts) == 0 || s.store == nil || s.store.DB == nil {
		return errorsList
	}
	for _, host := range hosts {
		var routed bool
		if err := s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM control.webnet_routes
				WHERE lower(hostname::text)=lower($1) AND service='webmail'
				  AND enabled AND certificate_name <> ''
			)
		`, host).Scan(&routed); err != nil || !routed {
			errorsList = append(errorsList, "Custom webmail hostname has no enabled webmail Web Net route with a certificate: "+host)
		}
		var owner string
		err := s.store.DB.QueryRow(ctx, `SELECT tenant_id::text FROM tenant_webmail_hosts WHERE hostname=$1`, host).Scan(&owner)
		if err == nil && owner != tenantID {
			errorsList = append(errorsList, "Custom webmail hostname belongs to another organization: "+host)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			errorsList = append(errorsList, "Custom webmail hostname could not be validated: "+host)
		}
	}
	return errorsList
}

// brandingExtendedRequested reports whether a submitted branding map asks for
// a paid white-label field with a non-empty/true value.
func brandingExtendedRequested(raw map[string]any) bool {
	for _, key := range []string{"logoUrl", "supportUrl", "legalUrl"} {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	if value, ok := raw["hideAttribution"].(bool); ok && value {
		return true
	}
	if value, ok := raw["webmailHosts"].([]any); ok && len(value) > 0 {
		return true
	}
	if value, ok := raw["webmailHosts"].([]string); ok && len(value) > 0 {
		return true
	}
	return false
}

func (s *Server) syncTenantWebmailHosts(ctx context.Context, tx pgx.Tx, tenantID string, hosts []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM tenant_webmail_hosts WHERE tenant_id=$1`, tenantID); err != nil {
		return err
	}
	for _, host := range hosts {
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_webmail_hosts (hostname, tenant_id) VALUES ($1, $2)`, host, tenantID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) publicBranding(w http.ResponseWriter, r *http.Request) {
	policy, _ := s.tenantBrandingForHost(r.Context(), r.Host)
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, s.publicBrandingPayload(policy))
}

// applyBrandingFooter adds the tenant's configured footer to outbound mail.
// The default Alt8Mail identity stays clean; a custom product name or support
// address opts the tenant into the footer.
func applyBrandingFooter(policy adminTenantPolicy, bodyText, bodyHTML string) (string, string) {
	product := truncate(strings.TrimSpace(policy.Branding.ProductName), 60)
	support := truncate(strings.TrimSpace(policy.Branding.SupportEmail), 200)
	if product == "" {
		product = "Alt8Mail"
	}
	if product == "Alt8Mail" && support == "" {
		return bodyText, bodyHTML
	}
	if strings.Contains(bodyHTML, "data-a8m-brand-footer") {
		return bodyText, bodyHTML
	}
	originalText := bodyText
	textFooter := "\n\n—\n" + product
	if support != "" {
		textFooter += " · " + support
	}
	if !strings.Contains(bodyText, product+" · "+support) && !strings.Contains(bodyText, "\n—\n"+product) {
		bodyText = strings.TrimRight(bodyText, "\r\n") + textFooter
	}
	supportHTML := ""
	if support != "" {
		supportHTML = ` · <a href="mailto:` + html.EscapeString(support) + `" style="color:#475569">` + html.EscapeString(support) + `</a>`
	}
	htmlFooter := `<div data-a8m-brand-footer="1" style="margin-top:18px;padding-top:10px;border-top:1px solid #e2e8f0;font-family:Arial,sans-serif;font-size:12px;color:#64748b">` +
		html.EscapeString(product) + supportHTML + `</div>`
	if bodyHTML == "" {
		bodyHTML = textToHTML(originalText)
	}
	bodyHTML = strings.TrimRight(bodyHTML, " \r\n") + htmlFooter
	return bodyText, bodyHTML
}
