package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Server) registerDashboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/dashboard", s.require("dashboard.read", s.handleDashboard))
	mux.HandleFunc("GET /control/api/diagnostics", s.require("dashboard.read", s.handleDiagnostics))
	mux.HandleFunc("GET /control/api/config-sync", s.require("dashboard.read", s.handleConfigSync))
	mux.HandleFunc("GET /control/api/config-sync/history", s.require("dashboard.read", s.handleConfigSyncHistory))
	mux.HandleFunc("POST /control/api/config-sync/run", s.require("settings.write", s.handleConfigSyncRun))
	mux.HandleFunc("GET /control/api/config-sync/import-preview", s.require("settings.write", s.handleConfigImportPreview))
	mux.HandleFunc("POST /control/api/config-sync/import", s.require("settings.write", s.handleConfigImport))
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	ctx := r.Context()
	host := s.probe.hostMetrics(ctx, false)
	services := s.probe.serviceStatuses(ctx, false)
	queue, _ := s.probe.queue(ctx, false)
	syncRun, _ := s.latestConfigSync(ctx)

	traffic := map[string]any{}
	for key, interval := range map[string]string{
		"today": "date_trunc('day', now())",
		"week":  "date_trunc('week', now())",
		"month": "date_trunc('month', now())",
	} {
		var sent, received int64
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_events WHERE status='sent' AND direction='outbound' AND event_at >= `+interval).Scan(&sent)
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_events WHERE status='received' AND event_at >= `+interval).Scan(&received)
		traffic[key] = map[string]any{"sent": sent, "received": received}
	}
	var bounces, deferrals int64
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_events WHERE status='bounced' AND event_at > now() - interval '30 days'`).Scan(&bounces)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_events WHERE status='deferred' AND event_at > now() - interval '30 days'`).Scan(&deferrals)

	topDomains := []map[string]any{}
	rows, err := s.db.Query(ctx, `
		SELECT split_part(recipient::text, '@', 2) AS domain, count(*)
		FROM control.mail_events
		WHERE direction='outbound' AND status='sent' AND event_at > now() - interval '30 days'
		  AND position('@' in recipient::text) > 0
		GROUP BY 1 ORDER BY 2 DESC LIMIT 5
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var domain string
			var count int64
			if rows.Scan(&domain, &count) == nil {
				topDomains = append(topDomains, map[string]any{"domain": domain, "count": count})
			}
		}
	}

	volume := []map[string]any{}
	rows, err = s.db.Query(ctx, `
		WITH days AS (
		  SELECT generate_series(current_date - 29, current_date, interval '1 day')::date AS day
		)
		SELECT to_char(d.day, 'YYYY-MM-DD'),
		  count(*) FILTER (WHERE e.status='sent' AND e.direction='outbound') AS sent,
		  count(*) FILTER (WHERE e.status='received') AS received,
		  count(*) FILTER (WHERE e.status='bounced') AS bounced,
		  count(*) FILTER (WHERE e.status='deferred') AS deferred
		FROM days d LEFT JOIN control.mail_events e ON e.event_at::date = d.day
		GROUP BY d.day ORDER BY d.day
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var day string
			var sent, received, bounced, deferred int64
			if rows.Scan(&day, &sent, &received, &bounced, &deferred) == nil {
				volume = append(volume, map[string]any{
					"day": day, "sent": sent, "received": received, "bounced": bounced, "deferred": deferred,
				})
			}
		}
	}

	var domainCount, userCount, mailboxCount, aliasCount, suppressionCount int64
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains`).Scan(&domainCount)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.users WHERE deleted_at IS NULL`).Scan(&userCount)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE active`).Scan(&mailboxCount)
	_ = s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.user_aliases WHERE active) + (SELECT count(*) FROM control.mail_aliases WHERE active)`).Scan(&aliasCount)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.suppressions WHERE expires_at IS NULL OR expires_at > now()`).Scan(&suppressionCount)
	counts := map[string]any{"domains": domainCount, "users": userCount, "mailboxes": mailboxCount, "aliases": aliasCount, "suppressions": suppressionCount}

	recentAudit := []map[string]any{}
	rows, err = s.db.Query(ctx, `
		SELECT actor_email::text, action, target, detail, ip::text, created_at
		FROM control.audit_log ORDER BY created_at DESC LIMIT 8
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var actor, action, target, detail, ip string
			var createdAt time.Time
			if rows.Scan(&actor, &action, &target, &detail, &ip, &createdAt) == nil {
				recentAudit = append(recentAudit, map[string]any{
					"actor": actor, "action": action, "target": target, "detail": detail,
					"ip": ip, "createdAt": createdAt.UTC().Format(time.RFC3339),
				})
			}
		}
	}

	certificates := []map[string]any{}
	rows, err = s.db.Query(ctx, `SELECT name, not_after, self_signed FROM control.ssl_certificates ORDER BY not_after`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var notAfter time.Time
			var selfSigned bool
			if rows.Scan(&name, &notAfter, &selfSigned) == nil {
				certificates = append(certificates, map[string]any{
					"name": name, "notAfter": notAfter.UTC().Format(time.RFC3339),
					"daysLeft": int(time.Until(notAfter).Hours() / 24), "selfSigned": selfSigned,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"traffic": traffic, "bounces": bounces, "deferrals": deferrals,
		"topDomains": topDomains, "volume": volume, "counts": counts,
		"host": host, "hostHistory": s.probe.hostHistory(), "queueMessages": queue,
		"services": services, "configSync": syncRun, "recentAudit": recentAudit,
		"certificates": certificates, "deliverabilityHealth": s.deliverabilityHealth(ctx),
		"securityReports": s.securityReportSummary(ctx),
		"version":         s.version, "checkedAt": nowRFC3339(),
		"serverTime": nowRFC3339(), "session": sessionView(sess),
	})
}

type diagnosticCheck struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	FixHint string `json:"fixHint,omitempty"`
}

func (s *Server) RunDoctor(ctx context.Context) []diagnosticCheck {
	checks := []diagnosticCheck{}
	add := func(id, label, status, detail, fix string) {
		checks = append(checks, diagnosticCheck{ID: id, Label: label, Status: status, Detail: detail, FixHint: fix})
	}

	ctxCheck, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.db.Ping(ctxCheck)
	cancel()
	if err != nil {
		add("database", "Control database", "fail", err.Error(), "Check PostgreSQL and CONTROL_DATABASE_URL")
	} else {
		add("database", "Control database", "pass", "PostgreSQL is reachable", "")
	}

	if redisOK, redisDetail := s.redisPing(ctx); redisOK {
		add("redis", "Redis", "pass", redisDetail, "")
	} else {
		add("redis", "Redis", "warn", redisDetail, "Check redis-server and its local binding")
	}

	postfixCheck, postfixErr := runCommand(ctx, 15*time.Second, "postfix", "check")
	if postfixErr == nil {
		add("postfix_config", "Postfix configuration", "pass", "postfix check completed", "")
	} else {
		detail := strings.TrimSpace(postfixCheck.Stderr)
		if detail == "" {
			detail = postfixErr.Error()
		}
		add("postfix_config", "Postfix configuration", "fail", detail, "Fix the reported main.cf/master.cf issue, then run postfix check")
	}

	dovecotCheck, dovecotErr := runCommand(ctx, 15*time.Second, "doveconf", "-n")
	if dovecotErr == nil {
		add("dovecot_config", "Dovecot configuration", "pass", fmt.Sprintf("doveconf parsed %d configuration lines", len(strings.Split(dovecotCheck.Stdout, "\n"))), "")
	} else {
		add("dovecot_config", "Dovecot configuration", "fail", strings.TrimSpace(dovecotCheck.Stderr), "Run doveconf -n and correct the reported directive")
	}

	services := s.probe.serviceStatuses(ctx, true)
	down := []string{}
	for _, service := range services {
		if !service.Active {
			down = append(down, service.Unit)
		}
	}
	if len(down) == 0 {
		add("services", "Core services", "pass", "All monitored services are active", "")
	} else {
		add("services", "Core services", "warn", "Inactive: "+strings.Join(down, ", "), "Start the stopped services or remove retired units from the monitored list")
	}

	host := s.probe.hostMetrics(ctx, true)
	if host.DiskPercent >= 95 {
		add("disk", "Disk capacity", "fail", fmt.Sprintf("Root filesystem is %.1f%% full", host.DiskPercent), "Free space before mail delivery is interrupted")
	} else if host.DiskPercent >= 85 {
		add("disk", "Disk capacity", "warn", fmt.Sprintf("Root filesystem is %.1f%% full", host.DiskPercent), "Plan storage cleanup or expansion")
	} else {
		add("disk", "Disk capacity", "pass", fmt.Sprintf("Root filesystem is %.1f%% full", host.DiskPercent), "")
	}
	if host.MemoryPercent >= 95 {
		add("memory", "Memory pressure", "fail", fmt.Sprintf("Memory is %.1f%% used", host.MemoryPercent), "Find the leaking service before delivery degrades")
	} else if host.MemoryPercent >= 85 {
		add("memory", "Memory pressure", "warn", fmt.Sprintf("Memory is %.1f%% used", host.MemoryPercent), "Review service memory usage")
	} else {
		add("memory", "Memory pressure", "pass", fmt.Sprintf("Memory is %.1f%% used", host.MemoryPercent), "")
	}

	if _, err := os.Stat(s.cfg.MailLogPath); err == nil {
		if file, openErr := os.Open(s.cfg.MailLogPath); openErr == nil {
			file.Close()
			add("mail_log", "Mail log ingestion", "pass", s.cfg.MailLogPath+" is readable", "")
		} else {
			add("mail_log", "Mail log ingestion", "fail", openErr.Error(), "Make the control service a member of the mail log group")
		}
	} else {
		add("mail_log", "Mail log ingestion", "warn", "Mail log not found at "+s.cfg.MailLogPath, "Set CONTROL_MAIL_LOG to the Postfix syslog file")
	}

	var missingDKIM, missingPostmaster, missingMaildir int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains WHERE COALESCE(dkim_record,'') = ''`).Scan(&missingDKIM)
	_ = s.db.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_domains d
		WHERE NOT EXISTS (SELECT 1 FROM public.mailboxes m WHERE m.domain=d.domain AND m.local_part='postmaster' AND m.active)
	`).Scan(&missingPostmaster)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE active AND provision_status <> 'ready'`).Scan(&missingMaildir)
	if missingDKIM == 0 && missingPostmaster == 0 && missingMaildir == 0 {
		add("mail_resources", "Mail resources", "pass", "DKIM, postmaster mailboxes and Maildirs are present", "")
	} else {
		add("mail_resources", "Mail resources", "warn",
			fmt.Sprintf("%d domains without DKIM, %d without postmaster, %d mailboxes without a ready Maildir", missingDKIM, missingPostmaster, missingMaildir),
			"Run configuration sync to provision missing resources")
	}

	resolver := s.dnsResolver(ctx)
	if _, err := resolver.LookupHost(ctx, s.mailHostname(ctx)); err != nil {
		add("dns", "DNS resolver", "warn", "Could not resolve the mail hostname via the configured resolver: "+err.Error(), "Check server.hostname, DNS resolver and public DNS")
	} else {
		add("dns", "DNS resolver", "pass", "Mail hostname resolves via the configured resolver", "")
	}

	conn, err := net.DialTimeout("tcp", s.cfg.SMTPAddr, 3*time.Second)
	if err != nil {
		add("smtp", "Local SMTP listener", "fail", err.Error(), "Check Postfix is listening on the configured SMTP address")
	} else {
		conn.Close()
		add("smtp", "Local SMTP listener", "pass", "Connected to "+s.cfg.SMTPAddr, "")
	}

	if len(checks) == 0 {
		add("doctor", "Doctor", "unknown", "No checks ran", "")
	}
	return checks
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	checks := s.RunDoctor(r.Context())
	fail, warn := 0, 0
	for _, check := range checks {
		if check.Status == "fail" {
			fail++
		}
		if check.Status == "warn" {
			warn++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checks": checks, "failures": fail, "warnings": warn,
		"ok": fail == 0, "checkedAt": nowRFC3339(),
	})
}

func (s *Server) mailHostname(ctx context.Context) string {
	value := s.settingString(ctx, "server.mail_hostname", "")
	if value == "" {
		value = s.cfg.MailHostname
	}
	return strings.TrimSpace(value)
}
