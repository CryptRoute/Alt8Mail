package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// enforceSenderDomainDNS prevents outbound mail from a domain whose DNS setup
// is incomplete. The ownership TXT record (_a8m-verification) is mandatory, and
// MX/SPF/DKIM/DMARC must all be ready before the domain is allowed to send.
func (s *Server) enforceSenderDomainDNS(ctx context.Context, tenantID, senderEmail string) error {
	domain := domainOf(senderEmail)
	if domain == "" {
		return failDetails(http.StatusForbidden, "DOMAIN_DNS_REQUIRED", "The sender domain could not be determined", map[string]any{"domain": ""})
	}
	var registeredDomain, selector, token, dkimRecord, mtaMode string
	var mtaMaxAge int
	err := s.store.DB.QueryRow(ctx, `
		SELECT domain::text, COALESCE(dkim_selector,''), COALESCE(verification_token,''),
		       COALESCE(dkim_record,''), COALESCE(mta_sts_mode,'none'), COALESCE(mta_sts_max_age, 604800)
		FROM mail_domains WHERE tenant_id=$1 AND lower(domain::text)=lower($2)
	`, tenantID, domain).Scan(&registeredDomain, &selector, &token, &dkimRecord, &mtaMode, &mtaMaxAge)
	if err != nil {
		return failDetails(http.StatusForbidden, "DOMAIN_DNS_REQUIRED",
			"Sending is blocked because "+domain+" is not registered for this organization.",
			map[string]any{"domain": domain, "missing": []string{"domain registration"},
				"ownership": map[string]any{"name": "_a8m-verification." + domain, "type": "TXT", "value": "a8m-verify=<verification token>"}})
	}
	checks := s.verifyDomainDNSWithMTA(ctx, net.DefaultResolver, registeredDomain, selector, token, dkimRecord,
		s.cfg.MailHostname, mtaMode, mtaMaxAge, s.fetchMTASTSPolicy)
	if checks.Ready {
		return nil
	}
	missing := []string{}
	details := map[string]string{}
	if !checks.Verification {
		missing = append(missing, "ownership TXT (_a8m-verification)")
		details["ownership"] = checks.Details["ownership"]
	}
	if !checks.MX {
		missing = append(missing, "MX")
		details["mx"] = checks.Details["mx"]
	}
	if !checks.SPF {
		missing = append(missing, "SPF")
		details["spf"] = checks.Details["spf"]
	}
	if !checks.DKIM {
		missing = append(missing, "DKIM")
		details["dkim"] = checks.Details["dkim"]
	}
	if !checks.DMARC {
		missing = append(missing, "DMARC")
		details["dmarc"] = checks.Details["dmarc"]
	}
	if normalizeMTASTSMode(mtaMode) != "none" && !checks.MTASTS {
		missing = append(missing, "MTA-STS")
		details["mtaSts"] = checks.Details["mta-sts"]
	}
	ownership := map[string]any{
		"name": "_a8m-verification." + registeredDomain, "type": "TXT",
		"value": "a8m-verify=" + token,
	}
	dkimName := "a8m._domainkey." + registeredDomain
	records := map[string]any{
		"ownership": ownership,
		"mx":        map[string]any{"name": registeredDomain, "type": "MX", "value": "10 " + s.cfg.MailHostname + "."},
		"spf":       map[string]any{"name": registeredDomain, "type": "TXT", "value": "v=spf1 mx -all"},
		"dkim":      map[string]any{"name": dkimName, "type": "TXT", "value": dkimRecord},
		"dmarc":     map[string]any{"name": "_dmarc." + registeredDomain, "type": "TXT", "value": "v=DMARC1; p=none"},
	}
	message := "Sending is blocked until DNS is complete for " + registeredDomain + ". Missing or invalid: " + strings.Join(missing, ", ") + "."
	return failDetails(http.StatusForbidden, "DOMAIN_DNS_REQUIRED", message, map[string]any{
		"domain": registeredDomain, "missing": missing, "details": details,
		"ownership": ownership, "records": records,
	})
}
