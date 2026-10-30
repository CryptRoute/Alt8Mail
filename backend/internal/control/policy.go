package control

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StartPolicyDaemon runs a Postfix SMTPD policy service that enforces rolling
// per-envelope-sender-domain caps. It is deliberately built in rather than
// relying on an external postfwd process: the rolling counts live in the same
// PostgreSQL transaction as the accept/defer decision, so retries, webmail
// submissions and authenticated relay clients share one counter.
func (s *Server) StartPolicyDaemon(ctx context.Context) {
	listener, err := net.Listen("tcp", s.cfg.PolicyListen)
	if err != nil {
		s.logger.Error("policy daemon listen failed", "addr", s.cfg.PolicyListen, "err", err)
		return
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.handlePolicyConnection(ctx, conn)
		}
	}()
}

func (s *Server) handlePolicyConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReaderSize(conn, 64<<10)
	writer := bufio.NewWriter(conn)
	for {
		attrs := map[string]string{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil && strings.TrimSpace(line) == "" {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			key, value, ok := strings.Cut(line, "=")
			if ok {
				attrs[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
			if err != nil {
				break
			}
		}
		if len(attrs) == 0 {
			return
		}
		response := s.policyDecision(ctx, attrs)
		_, _ = writer.WriteString(response + "\n\n")
		_ = writer.Flush()
	}
}

func (s *Server) policyDecision(ctx context.Context, attrs map[string]string) string {
	if !strings.EqualFold(attrs["protocol_state"], "RCPT") {
		return "action=DUNNO"
	}
	sender := attrs["sender"]
	recipient := attrs["recipient"]
	domain := emailDomain(sender)
	if domain == "" || recipient == "" {
		return "action=DUNNO"
	}
	if blocked, err := s.securityReportBlocked(ctx, sender); err == nil && blocked {
		return "action=REJECT 5.7.1 Sender blocked by Alt8Mail Security review"
	} else if err != nil {
		s.logger.Warn("security report policy lookup failed", "err", err)
	}
	clientIP := net.ParseIP(attrs["client_address"])
	authenticated := strings.TrimSpace(attrs["sasl_username"]) != ""
	localClient := clientIP != nil && (clientIP.IsLoopback() || clientIP.IsPrivate())
	if !authenticated && !localClient {
		// An unauthenticated external inbound transaction must never consume a
		// customer's outbound allowance.
		return "action=DUNNO"
	}
	var managed int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains WHERE domain=$1`, domain).Scan(&managed)
	if managed == 0 {
		return "action=DUNNO"
	}
	queueID := strings.TrimSpace(attrs["queue_id"])
	size, _ := strconv.ParseInt(attrs["size"], 10, 64)
	reason, allowed := s.checkDomainCaps(ctx, domain, queueID, recipient, size, sender, attrs["sasl_username"], attrs["client_address"])
	if allowed {
		return "action=DUNNO"
	}
	return "action=DEFER 4.7.1 " + reason
}

// securityReportBlocked enforces user-submitted security reports at the SMTP
// policy layer. Pending reports block immediately; dismissing a report removes
// the block, while confirming it with domain scope widens the block.
func (s *Server) securityReportBlocked(ctx context.Context, sender string) (bool, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if sender == "" || !strings.Contains(sender, "@") {
		return false, nil
	}
	domain := emailDomain(sender)
	var blocked bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.security_reports
			WHERE status IN ('pending', 'confirmed')
			  AND (
			    (block_scope = 'address' AND lower(sender_email::text) = $1)
			    OR (block_scope = 'domain' AND $2 <> '' AND lower(sender_domain) = $2)
			  )
		)
	`, sender, domain).Scan(&blocked)
	return blocked, err
}

func (s *Server) checkDomainCaps(ctx context.Context, domain, queueID, recipient string, size int64, sender, saslUser, client string) (string, bool) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "Outbound policy is temporarily unavailable", false
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "alt8mail-cap:"+domain); err != nil {
		return "Outbound policy is temporarily unavailable", false
	}
	var capsEnabled bool
	var capHour, capDay, capMonth, burst int
	err = tx.QueryRow(ctx, `
		SELECT caps_enabled, cap_hour, cap_day, cap_month, burst
		FROM control.domain_settings cs JOIN public.mail_domains d ON d.id=cs.domain_id
		WHERE d.domain=$1
	`, domain).Scan(&capsEnabled, &capHour, &capDay, &capMonth, &burst)
	if err == pgx.ErrNoRows {
		return "", true
	}
	if err != nil {
		return "Outbound policy is temporarily unavailable", false
	}
	var hourCount, dayCount, monthCount int64
	_ = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE attempted_at > now()-interval '1 hour'),
		       count(*) FILTER (WHERE attempted_at > now()-interval '24 hours'),
		       count(*) FILTER (WHERE attempted_at > now()-interval '30 days')
		FROM control.outbound_attempts WHERE sender_domain=$1 AND action='permit'
	`, domain).Scan(&hourCount, &dayCount, &monthCount)
	if capsEnabled {
		effectiveHour := capHour
		if effectiveHour > 0 && burst > 0 {
			effectiveHour += burst
		}
		switch {
		case effectiveHour > 0 && hourCount+1 > int64(effectiveHour):
			reason := fmt.Sprintf("Outgoing hourly cap reached for %s (%d/hour%s)", domain, capHour, burstText(burst))
			_, _ = tx.Exec(ctx, attemptInsertSQL(), queueID, sender, domain, recipient, size, saslUser, client, "defer", reason)
			_ = tx.Commit(ctx)
			return reason, false
		case capDay > 0 && dayCount+1 > int64(capDay):
			reason := fmt.Sprintf("Outgoing daily cap reached for %s (%d/day)", domain, capDay)
			_, _ = tx.Exec(ctx, attemptInsertSQL(), queueID, sender, domain, recipient, size, saslUser, client, "defer", reason)
			_ = tx.Commit(ctx)
			return reason, false
		case capMonth > 0 && monthCount+1 > int64(capMonth):
			reason := fmt.Sprintf("Outgoing monthly cap reached for %s (%d/30 days)", domain, capMonth)
			_, _ = tx.Exec(ctx, attemptInsertSQL(), queueID, sender, domain, recipient, size, saslUser, client, "defer", reason)
			_ = tx.Commit(ctx)
			return reason, false
		}
	}
	_, err = tx.Exec(ctx, attemptInsertSQL(), queueID, sender, domain, recipient, size, saslUser, client, "permit", "")
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return "Outbound policy is temporarily unavailable", false
	}
	if err := tx.Commit(ctx); err != nil {
		return "Outbound policy is temporarily unavailable", false
	}
	return "", true
}

func attemptInsertSQL() string {
	return `
		INSERT INTO control.outbound_attempts
		  (queue_id,sender,sender_domain,recipient,size_bytes,sasl_user,client_address,action,reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT DO NOTHING
	`
}

func burstText(burst int) string {
	if burst <= 0 {
		return ""
	}
	return fmt.Sprintf(" + %d burst", burst)
}

func (s *Server) syncPolicyServiceConfig(ctx context.Context) (bool, error) {
	changed, err := postconfEnsureAppend(ctx, "smtpd_recipient_restrictions",
		"check_policy_service inet:"+s.cfg.PolicyListen)
	if err != nil {
		return false, err
	}
	defaultChanged, err := postconfEnsure(ctx, "smtpd_policy_service_default_action", "DUNNO")
	if err != nil {
		return changed, err
	}
	return changed || defaultChanged, nil
}
