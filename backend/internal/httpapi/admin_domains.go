package httpapi

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var dkimSelectorPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

func validDKIMSelector(value string) bool {
	return len(value) <= 63 && dkimSelectorPattern.MatchString(value)
}

func (s *Server) adminListDomains(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id::text, domain::text, is_primary, COALESCE(dkim_selector, ''), status,
		       COALESCE(verification_token, ''), verified_at, mx_verified, spf_verified,
		       dkim_verified, dmarc_verified, dmarc_policy, catch_all, last_checked_at, last_error, created_at,
		       COALESCE(dkim_record, ''), COALESCE(dkim_key_error, ''),
		       mta_sts_mode, mta_sts_max_age, mta_sts_verified, mta_sts_last_error
		FROM mail_domains WHERE tenant_id = $1 ORDER BY is_primary DESC, domain
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOMAINS", "Could not load domains")
		return
	}
	defer rows.Close()
	domains := []map[string]any{}
	for rows.Next() {
		var id, domain, selector, status, token, dmarcPolicy, catchAll, lastError, dkimRecord, dkimKeyError string
		var mtaMode, mtaLastError string
		var mtaMaxAge int
		var isPrimary, mx, spf, dkim, dmarc, mtaVerified bool
		var verifiedAt, lastChecked, created *time.Time
		if rows.Scan(&id, &domain, &isPrimary, &selector, &status, &token, &verifiedAt, &mx, &spf, &dkim, &dmarc, &dmarcPolicy, &catchAll, &lastChecked, &lastError, &created, &dkimRecord, &dkimKeyError,
			&mtaMode, &mtaMaxAge, &mtaVerified, &mtaLastError) == nil {
			mailReady := (status == "verified" || status == "manual") && mx && spf && dkim && dmarc
			policy := mtaSTSPolicy{Domain: domain, Mode: mtaMode, MaxAge: mtaMaxAge, MailHost: s.cfg.MailHostname}
			entry := map[string]any{
				"id": id, "domain": domain, "isPrimary": isPrimary, "dkimSelector": selector,
				"status": status, "verificationToken": token, "dmarcPolicy": dmarcPolicy,
				"catchAll": catchAll, "lastError": lastError, "mailHostname": s.cfg.MailHostname,
				"dkimKeyReady": dkimRecord != "", "dkimKeyError": dkimKeyError,
				"mailReady":      mailReady,
				"mtaStsMode":     normalizeMTASTSMode(mtaMode),
				"mtaStsMaxAge":   normalizeMTASTSMaxAge(mtaMaxAge),
				"mtaStsVerified": mtaVerified, "mtaStsLastError": mtaLastError,
				"mtaStsPolicyUrl": policy.URL(),
				"checks": map[string]any{
					"verification": status == "verified" || status == "manual",
					"mx":           mx, "spf": spf, "dkim": dkim, "dmarc": dmarc,
					"mtaSts": normalizeMTASTSMode(mtaMode) == "none" || mtaVerified,
				},
				"records": domainRecordsWithMTA(domain, selector, token, s.cfg.MailHostname,
					dkimRecord, dmarcPolicy, mtaMode, mtaMaxAge),
			}
			if verifiedAt != nil {
				entry["verifiedAt"] = formatAdminTime(*verifiedAt)
			}
			if lastChecked != nil {
				entry["lastCheckedAt"] = formatAdminTime(*lastChecked)
			}
			if created != nil {
				entry["createdAt"] = formatAdminTime(*created)
			}
			domains = append(domains, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains, "count": len(domains)})
}

func domainRecords(domain, selector, token, mailHost, dkimRecord, dmarcPolicy string) []map[string]string {
	return domainRecordsWithMTA(domain, selector, token, mailHost, dkimRecord, dmarcPolicy, "none", 0)
}

func domainRecordsWithMTA(domain, selector, token, mailHost, dkimRecord, dmarcPolicy, mtaMode string, mtaMaxAge int) []map[string]string {
	if selector == "" {
		selector = "a8m"
	}
	if mailHost == "" {
		mailHost = "mail.a8m.test"
	}
	mailHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mailHost)), ".")
	dmarcPolicy = strings.ToLower(strings.TrimSpace(dmarcPolicy))
	switch dmarcPolicy {
	case "none", "quarantine", "reject":
	default:
		dmarcPolicy = "none"
	}
	dkim := map[string]string{
		"type": "TXT", "name": selector + "._domainkey." + domain,
		"purpose": "DKIM signing · selector " + selector,
	}
	if strings.TrimSpace(dkimRecord) == "" {
		dkim["value"] = ""
		dkim["status"] = "provisioning"
		dkim["note"] = "The DKIM signing key is being provisioned. Refresh this page after the dkimprovision timer finishes."
	} else {
		dkim["value"] = strings.TrimSpace(dkimRecord)
		dkim["status"] = "ready"
	}
	records := []map[string]string{
		{"type": "TXT", "name": "_a8m-verification." + domain, "value": "a8m-verify=" + token, "purpose": "Ownership verification"},
		{"type": "MX", "name": domain, "value": "10 " + mailHost + ".", "purpose": "Inbound mail routing"},
		{"type": "TXT", "name": domain, "value": "v=spf1 mx -all", "purpose": "SPF sender authorization"},
		dkim,
		{"type": "TXT", "name": "_dmarc." + domain, "value": "v=DMARC1; p=" + dmarcPolicy + "; rua=mailto:dmarc@" + domain, "purpose": "DMARC policy"},
	}
	policy := mtaSTSPolicy{Domain: domain, Mode: mtaMode, MaxAge: mtaMaxAge, MailHost: mailHost}
	if policy.Body() != nil {
		records = append(records,
			map[string]string{
				"type": "TXT", "name": "_mta-sts." + domain,
				"value":   "v=STSv1; id=" + policy.ID(),
				"purpose": "MTA-STS policy advertisement · mode " + normalizeMTASTSMode(mtaMode),
			},
			map[string]string{
				"type": "POLICY", "name": "mta-sts." + domain + "/.well-known/mta-sts.txt",
				"value":   string(policy.Body()),
				"purpose": "Published MTA-STS policy · requires a valid TLS certificate for mta-sts." + domain,
			},
		)
	}
	return records
}

func (s *Server) adminCreateDomain(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Domain       string `json:"domain"`
		Primary      bool   `json:"primary"`
		DKIMSelector string `json:"dkimSelector"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Domain = strings.ToLower(strings.TrimSpace(body.Domain))
	body.DKIMSelector = strings.ToLower(strings.TrimSpace(body.DKIMSelector))
	if !validMailDomain(body.Domain) {
		writeError(w, http.StatusBadRequest, "DOMAIN", "Enter a valid mail domain")
		return
	}
	if body.DKIMSelector == "" {
		body.DKIMSelector = "a8m"
	}
	if !validDKIMSelector(body.DKIMSelector) {
		writeError(w, http.StatusBadRequest, "DKIM", "Invalid DKIM selector")
		return
	}
	var existingPrimary bool
	_ = s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mail_domains WHERE tenant_id = $1 AND is_primary)`, sess.ActiveTenantID).Scan(&existingPrimary)
	isPrimary := body.Primary || !existingPrimary
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DOMAIN", "Could not add domain")
		return
	}
	defer tx.Rollback(r.Context())
	if isPrimary {
		_, _ = tx.Exec(r.Context(), `UPDATE mail_domains SET is_primary = false WHERE tenant_id = $1`, sess.ActiveTenantID)
	}
	token := randomToken(18)
	var id string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO mail_domains (tenant_id, domain, is_primary, dkim_selector, status, verification_token)
		VALUES ($1, $2, $3, $4, 'pending', $5) RETURNING id::text
	`, sess.ActiveTenantID, body.Domain, isPrimary, body.DKIMSelector, token).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "DOMAIN_EXISTS", "That domain is already attached to an organization")
		} else {
			writeError(w, http.StatusInternalServerError, "DOMAIN", "Could not add domain")
		}
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DOMAIN", "Could not commit domain")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.domain_created", body.Domain, "Added mail domain", map[string]any{"domain": body.Domain, "primary": isPrimary})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "domain": map[string]any{
		"id": id, "domain": body.Domain, "status": "pending", "isPrimary": isPrimary,
		"dkimSelector": body.DKIMSelector, "dkimKeyReady": false, "dkimKeyError": "",
		"mailReady": false, "mailHostname": s.cfg.MailHostname,
		"records": domainRecords(body.Domain, body.DKIMSelector, token, s.cfg.MailHostname, "", "none"),
	}})
}

func (s *Server) adminUpdateDomain(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var id, domain, selector, status, dmarcPolicy, catchAll, mtaMode string
	var mtaMaxAge int
	var isPrimary bool
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id::text, domain::text, is_primary, COALESCE(dkim_selector, ''), status, dmarc_policy, catch_all,
		       mta_sts_mode, mta_sts_max_age
		FROM mail_domains WHERE id = $1 AND tenant_id = $2
	`, r.PathValue("id"), sess.ActiveTenantID).Scan(&id, &domain, &isPrimary, &selector, &status, &dmarcPolicy, &catchAll, &mtaMode, &mtaMaxAge)
	if err != nil {
		writeError(w, http.StatusNotFound, "DOMAIN_NOT_FOUND", "Domain not found")
		return
	}
	oldSelector := selector
	oldMTA := normalizeMTASTSMode(mtaMode) + ":" + strconv.Itoa(normalizeMTASTSMaxAge(mtaMaxAge))
	var body struct {
		Primary      *bool   `json:"primary"`
		DKIMSelector *string `json:"dkimSelector"`
		DMARCPolicy  *string `json:"dmarcPolicy"`
		CatchAll     *string `json:"catchAll"`
		MTASTSMode   *string `json:"mtaStsMode"`
		MTASTSMaxAge *int    `json:"mtaStsMaxAge"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.DKIMSelector != nil && strings.TrimSpace(*body.DKIMSelector) != "" {
		selector = strings.ToLower(strings.TrimSpace(*body.DKIMSelector))
		if !validDKIMSelector(selector) {
			writeError(w, http.StatusBadRequest, "DKIM", "Invalid DKIM selector")
			return
		}
	}
	if body.DMARCPolicy != nil {
		switch *body.DMARCPolicy {
		case "none", "quarantine", "reject":
			dmarcPolicy = *body.DMARCPolicy
		default:
			writeError(w, http.StatusBadRequest, "DMARC", "DMARC policy must be none, quarantine or reject")
			return
		}
	}
	if body.CatchAll != nil {
		catchAll = strings.ToLower(strings.TrimSpace(*body.CatchAll))
		if catchAll != "" && !strings.Contains(catchAll, "@") {
			writeError(w, http.StatusBadRequest, "CATCH_ALL", "Catch-all must be an email address")
			return
		}
	}
	if body.MTASTSMode != nil {
		requested := strings.ToLower(strings.TrimSpace(*body.MTASTSMode))
		switch requested {
		case "none", "testing", "enforce":
			mtaMode = requested
		default:
			writeError(w, http.StatusBadRequest, "MTA_STS", "MTA-STS mode must be none, testing or enforce")
			return
		}
	}
	if body.MTASTSMaxAge != nil {
		if *body.MTASTSMaxAge < 86400 || *body.MTASTSMaxAge > 31557600 {
			writeError(w, http.StatusBadRequest, "MTA_STS", "MTA-STS max age must be between 86400 and 31557600 seconds")
			return
		}
		mtaMaxAge = *body.MTASTSMaxAge
	}
	if body.Primary != nil {
		if *body.Primary && !isPrimary {
			_, _ = s.store.DB.Exec(r.Context(), `UPDATE mail_domains SET is_primary = false WHERE tenant_id = $1`, sess.ActiveTenantID)
			isPrimary = true
		} else if !*body.Primary && isPrimary {
			writeError(w, http.StatusBadRequest, "PRIMARY", "Choose another primary domain instead of removing the primary flag")
			return
		}
	}
	selectorChanged := selector != oldSelector
	mtaChanged := (normalizeMTASTSMode(mtaMode) + ":" + strconv.Itoa(normalizeMTASTSMaxAge(mtaMaxAge))) != oldMTA
	update := `UPDATE mail_domains SET is_primary = $2, dkim_selector = $3, dmarc_policy = $4, catch_all = $5,
		mta_sts_mode = $6, mta_sts_max_age = $7`
	if selectorChanged {
		// The signing key is selector-specific. Clear the old public material;
		// dkimprovision generates the new key on its next pass.
		update += `, dkim_public_key = '', dkim_record = '', dkim_key_created_at = NULL,
			dkim_key_error = '', dkim_verified = false`
	}
	if mtaChanged {
		update += `, mta_sts_verified = false, mta_sts_last_error = ''`
	}
	update += ` WHERE id = $1 AND tenant_id = $8`
	if _, err := s.store.DB.Exec(r.Context(), update, id, isPrimary, selector, dmarcPolicy, catchAll,
		normalizeMTASTSMode(mtaMode), normalizeMTASTSMaxAge(mtaMaxAge), sess.ActiveTenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "DOMAIN", "Could not update domain")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.domain_updated", domain, "Updated domain routing policy", map[string]any{"domainId": id, "selectorChanged": selectorChanged, "mtaStsChanged": mtaChanged})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "selectorChanged": selectorChanged,
		"dkimProvisioning": selectorChanged, "mtaStsChanged": mtaChanged})
}

func (s *Server) adminDeleteDomain(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var id, domain string
	var isPrimary bool
	err := s.store.DB.QueryRow(r.Context(), `SELECT id::text, domain::text, is_primary FROM mail_domains WHERE id = $1 AND tenant_id = $2`, r.PathValue("id"), sess.ActiveTenantID).Scan(&id, &domain, &isPrimary)
	if err != nil {
		writeError(w, http.StatusNotFound, "DOMAIN_NOT_FOUND", "Domain not found")
		return
	}
	if isPrimary {
		writeError(w, http.StatusConflict, "PRIMARY_DOMAIN", "Make another domain primary before deleting this one")
		return
	}
	var mailboxes int
	_ = s.store.DB.QueryRow(r.Context(), `SELECT count(*) FROM mailboxes WHERE tenant_id = $1 AND domain = $2`, sess.ActiveTenantID, domain).Scan(&mailboxes)
	if mailboxes > 0 {
		writeError(w, http.StatusConflict, "DOMAIN_IN_USE", "Move or delete users on this domain before removing it")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM mail_domains WHERE id = $1 AND tenant_id = $2`, id, sess.ActiveTenantID)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.domain_deleted", domain, "Deleted mail domain", map[string]any{"domain": domain})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminVerifyDomain(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var id, domain, selector, token, currentStatus, dkimRecord, dmarcPolicy, mtaMode string
	var mtaMaxAge int
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT id::text, domain::text, COALESCE(dkim_selector, ''), COALESCE(verification_token, ''),
		       status, COALESCE(dkim_record, ''), dmarc_policy, mta_sts_mode, mta_sts_max_age
		FROM mail_domains WHERE id = $1 AND tenant_id = $2
	`, r.PathValue("id"), sess.ActiveTenantID).Scan(&id, &domain, &selector, &token, &currentStatus, &dkimRecord, &dmarcPolicy, &mtaMode, &mtaMaxAge)
	if err != nil {
		writeError(w, http.StatusNotFound, "DOMAIN_NOT_FOUND", "Domain not found")
		return
	}
	if strings.TrimSpace(token) == "" {
		token = randomToken(18)
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE mail_domains SET verification_token = $2 WHERE id = $1`, id, token)
	}
	checks := s.verifyDomainDNSWithMTA(r.Context(), net.DefaultResolver, domain, selector, token, dkimRecord,
		s.cfg.MailHostname, mtaMode, mtaMaxAge, s.fetchMTASTSPolicy)
	status := "pending"
	if checks.Verification {
		status = "verified"
	} else if currentStatus == "verified" || currentStatus == "manual" {
		status = currentStatus
	}
	var verifiedAt any
	if checks.Verification {
		verifiedAt = time.Now()
	}
	lastError := ""
	if !checks.Ready {
		lastError = checks.Error
	}
	if checks.DMARCPolicy == "" {
		checks.DMARCPolicy = dmarcPolicy
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE mail_domains SET status = $2, mx_verified = $3, spf_verified = $4, dkim_verified = $5,
		       dmarc_verified = $6, verified_at = COALESCE($7, verified_at), last_checked_at = now(),
		       last_error = $8, dmarc_policy = COALESCE(NULLIF($9, ''), dmarc_policy),
		       mta_sts_verified = $11, mta_sts_last_error = $12
		WHERE id = $1 AND tenant_id = $10
	`, id, status, checks.MX, checks.SPF, checks.DKIM, checks.DMARC, verifiedAt, lastError, checks.DMARCPolicy,
		sess.ActiveTenantID, checks.MTASTS, checks.Details["mta-sts"])
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.domain_verified", domain, "Checked DNS verification", map[string]any{"domain": domain, "status": status, "ready": checks.Ready, "checks": checks})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": checks.Verification, "ready": checks.Ready, "status": status, "domain": domain,
		"checks": map[string]any{
			"verification": checks.Verification, "mx": checks.MX, "spf": checks.SPF,
			"dkim": checks.DKIM, "dmarc": checks.DMARC, "mtaSts": checks.MTASTS,
		},
		"details": checks.Details,
		"error":   checks.Error,
		"records": domainRecordsWithMTA(domain, selector, token, s.cfg.MailHostname, dkimRecord, checks.DMARCPolicy, mtaMode, mtaMaxAge),
	})
}

type domainDNSChecks struct {
	Verification bool
	MX           bool
	SPF          bool
	DKIM         bool
	DMARC        bool
	MTASTS       bool
	Ready        bool
	DMARCPolicy  string
	Error        string
	Details      map[string]string
}

type domainLookup interface {
	LookupTXT(context.Context, string) ([]string, error)
	LookupMX(context.Context, string) ([]*net.MX, error)
}

func (s *Server) verifyDomainDNS(ctx context.Context, domain, selector, token, expectedDKIM, mailHost string) domainDNSChecks {
	return s.verifyDomainDNSWithMTA(ctx, net.DefaultResolver, domain, selector, token, expectedDKIM,
		mailHost, "none", defaultMTASTSMaxAge, s.fetchMTASTSPolicy)
}

func (s *Server) verifyDomainDNSWithMTA(ctx context.Context, resolver domainLookup, domain, selector, token,
	expectedDKIM, mailHost, mtaMode string, mtaMaxAge int, fetchPolicy mtaSTSFetcher) domainDNSChecks {
	timeoutCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return verifyDomainDNSFull(timeoutCtx, resolver, domain, selector, token, expectedDKIM, mailHost,
		mtaMode, mtaMaxAge, fetchPolicy)
}

func verifyDomainDNSWith(ctx context.Context, resolver domainLookup, domain, selector, token, expectedDKIM, mailHost string) domainDNSChecks {
	return verifyDomainDNSFull(ctx, resolver, domain, selector, token, expectedDKIM, mailHost,
		"none", defaultMTASTSMaxAge, nil)
}

func verifyDomainDNSFull(ctx context.Context, resolver domainLookup, domain, selector, token, expectedDKIM,
	mailHost, mtaMode string, mtaMaxAge int, fetchPolicy mtaSTSFetcher) domainDNSChecks {
	checks := domainDNSChecks{Details: map[string]string{}, MTASTS: normalizeMTASTSMode(mtaMode) == "none"}
	if selector == "" {
		selector = "a8m"
	}
	mailHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(mailHost)), ".")

	if token == "" {
		checks.Details["ownership"] = "verification token is missing"
	} else if txts, err := resolver.LookupTXT(ctx, "_a8m-verification."+domain); err != nil {
		checks.Details["ownership"] = "TXT lookup failed: " + err.Error()
	} else {
		expected := "a8m-verify=" + token
		for _, txt := range txts {
			if strings.TrimSpace(txt) == expected {
				checks.Verification = true
				break
			}
		}
		if !checks.Verification {
			checks.Details["ownership"] = "no matching a8m-verify TXT record"
		}
	}

	if mx, err := resolver.LookupMX(ctx, domain); err != nil {
		checks.Details["mx"] = "MX lookup failed: " + err.Error()
	} else if len(mx) == 0 {
		checks.Details["mx"] = "no MX records"
	} else if mailHost == "" {
		checks.MX = true
	} else {
		found := []string{}
		for _, record := range mx {
			if record == nil {
				continue
			}
			host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Host)), ".")
			found = append(found, host)
			if host == mailHost {
				checks.MX = true
			}
		}
		if !checks.MX {
			checks.Details["mx"] = "MX points to " + strings.Join(found, ", ") + "; expected " + mailHost
		}
	}

	if txts, err := resolver.LookupTXT(ctx, domain); err != nil {
		checks.Details["spf"] = "TXT lookup failed: " + err.Error()
	} else {
		for _, txt := range txts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(txt)), "v=spf1") {
				checks.SPF = true
				break
			}
		}
		if !checks.SPF {
			checks.Details["spf"] = "no v=spf1 TXT record"
		}
	}

	expectedP := dkimPublicKey(expectedDKIM)
	if expectedP == "" {
		checks.Details["dkim"] = "signing key has not been provisioned yet"
	} else if txts, err := resolver.LookupTXT(ctx, selector+"._domainkey."+domain); err != nil {
		checks.Details["dkim"] = "TXT lookup failed: " + err.Error()
	} else {
		mismatch := false
		for _, txt := range txts {
			tags := parseDKIMTags(txt)
			if !strings.EqualFold(tags["v"], "DKIM1") {
				continue
			}
			p := normalizeDKIMBase64(tags["p"])
			if p == "" {
				mismatch = true
				continue
			}
			if p == expectedP {
				checks.DKIM = true
				break
			}
			mismatch = true
		}
		if !checks.DKIM {
			if mismatch {
				checks.Details["dkim"] = "TXT p= does not match the active signing key"
			} else {
				checks.Details["dkim"] = "no DKIM1 TXT record at " + selector + "._domainkey." + domain
			}
		}
	}

	if txts, err := resolver.LookupTXT(ctx, "_dmarc."+domain); err != nil {
		checks.Details["dmarc"] = "TXT lookup failed: " + err.Error()
	} else {
		for _, txt := range txts {
			tags := parseDKIMTags(txt)
			if !strings.EqualFold(tags["v"], "DMARC1") {
				continue
			}
			policy := strings.ToLower(strings.TrimSpace(tags["p"]))
			switch policy {
			case "none", "quarantine", "reject":
				checks.DMARC = true
				checks.DMARCPolicy = policy
			default:
				checks.Details["dmarc"] = "DMARC p= must be none, quarantine or reject"
			}
			break
		}
		if !checks.DMARC && checks.Details["dmarc"] == "" {
			checks.Details["dmarc"] = "no DMARC1 record with a valid p= policy"
		}
	}

	if normalizeMTASTSMode(mtaMode) != "none" {
		policy := mtaSTSPolicy{Domain: domain, Mode: mtaMode, MaxAge: mtaMaxAge, MailHost: mailHost}
		expectedID := policy.ID()
		txtOK := false
		if txts, err := resolver.LookupTXT(ctx, "_mta-sts."+domain); err != nil {
			checks.Details["mta-sts"] = "TXT lookup failed: " + err.Error()
		} else {
			for _, txt := range txts {
				fields := parseDKIMTags(txt)
				if strings.EqualFold(strings.TrimSpace(fields["v"]), "STSv1") &&
					strings.EqualFold(strings.TrimSpace(fields["id"]), expectedID) {
					txtOK = true
					break
				}
			}
			if !txtOK {
				checks.Details["mta-sts"] = "no _mta-sts TXT record with id=" + expectedID
			}
		}
		if txtOK {
			if fetchPolicy == nil {
				checks.Details["mta-sts"] = "policy fetch is not configured"
			} else if body, err := fetchPolicy(ctx, policy.URL()); err != nil {
				checks.Details["mta-sts"] = "policy fetch failed: " + err.Error()
			} else if err := verifyMTASTSPolicyRecord(body, domain, mtaMode, mtaMaxAge, mailHost); err != nil {
				checks.Details["mta-sts"] = err.Error()
			} else {
				checks.MTASTS = true
				checks.Details["mta-sts"] = ""
			}
		}
	}

	missing := []string{}
	addMissing := func(key, label string) {
		if detail := checks.Details[key]; detail != "" {
			label += " (" + detail + ")"
		}
		missing = append(missing, label)
	}
	if !checks.Verification {
		addMissing("ownership", "ownership TXT")
	}
	if !checks.MX {
		addMissing("mx", "MX")
	}
	if !checks.SPF {
		addMissing("spf", "SPF")
	}
	if !checks.DKIM {
		addMissing("dkim", "DKIM")
	}
	if !checks.DMARC {
		addMissing("dmarc", "DMARC")
	}
	if normalizeMTASTSMode(mtaMode) != "none" && !checks.MTASTS {
		addMissing("mta-sts", "MTA-STS")
	}
	checks.Ready = len(missing) == 0
	if !checks.Ready {
		checks.Error = "Missing or invalid DNS records: " + strings.Join(missing, ", ")
	}
	return checks
}

func parseDKIMTags(record string) map[string]string {
	tags := map[string]string{}
	for _, part := range strings.Split(record, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		tags[key] = strings.TrimSpace(value)
	}
	return tags
}

func normalizeDKIMBase64(value string) string {
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "\t", "")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return strings.Trim(value, "\"")
}

func dkimPublicKey(record string) string {
	return normalizeDKIMBase64(parseDKIMTags(record)["p"])
}
