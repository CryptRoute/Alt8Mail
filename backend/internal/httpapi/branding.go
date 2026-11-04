package httpapi

import (
	"html"
	"net"
	"net/http"
	"strings"
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

func publicBrandingPayload(policy adminTenantPolicy) map[string]any {
	product := truncate(strings.TrimSpace(policy.Branding.ProductName), 60)
	if product == "" {
		product = "Alt8Mail"
	}
	support := truncate(strings.TrimSpace(policy.Branding.SupportEmail), 200)
	loginMessage := truncate(strings.TrimSpace(policy.Branding.LoginMessage), 240)
	return map[string]any{
		"productName": product, "primaryColor": safeBrandColor(policy.Branding.PrimaryColor),
		"supportEmail": support, "loginMessage": loginMessage,
	}
}

func (s *Server) publicBranding(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	policy := defaultTenantPolicy()
	if host != "" {
		var raw []byte
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT t.settings FROM public.tenants t
			JOIN public.mail_domains d ON d.tenant_id=t.id
			WHERE lower(d.domain::text)=lower($1) AND t.status='active'
			LIMIT 1
		`, host).Scan(&raw)
		if err == nil {
			policy = tenantPolicyFromSettings(raw)
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, publicBrandingPayload(policy))
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
