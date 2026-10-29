package control

import (
	"context"
	"net/http"
)

func (s *Server) registerDeliveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/delivery/queue", s.require("delivery.read", s.handleQueue))
	mux.HandleFunc("GET /control/api/delivery/queue/{id}", s.require("delivery.read", s.handleQueueDetail))
	mux.HandleFunc("POST /control/api/delivery/queue/{id}/flush", s.require("delivery.manage", s.handleQueueFlush))
	mux.HandleFunc("DELETE /control/api/delivery/queue/{id}", s.require("delivery.manage", s.handleQueueDelete))
	mux.HandleFunc("DELETE /control/api/delivery/queue", s.require("delivery.manage", s.handleQueueDeleteAll))

	mux.HandleFunc("GET /control/api/delivery/smtp-tests", s.require("delivery.read", s.handleSMTPTests))
	mux.HandleFunc("POST /control/api/delivery/smtp-tests", s.require("delivery.manage", s.handleSMTPTestSend))
	mux.HandleFunc("GET /control/api/delivery/smtp-tests/{id}", s.require("delivery.read", s.handleSMTPTestDetail))

	mux.HandleFunc("GET /control/api/delivery/relay", s.require("delivery.read", s.handleRelayGet))
	mux.HandleFunc("PUT /control/api/delivery/relay", s.require("delivery.manage", s.handleRelaySave))
	mux.HandleFunc("POST /control/api/delivery/relay/test", s.require("delivery.manage", s.handleRelayTest))

	mux.HandleFunc("GET /control/api/delivery/outbound-ips", s.require("delivery.read", s.handleOutboundIPs))
	mux.HandleFunc("POST /control/api/delivery/outbound-ips", s.require("delivery.manage", s.handleOutboundIPCreate))
	mux.HandleFunc("PATCH /control/api/delivery/outbound-ips/{id}", s.require("delivery.manage", s.handleOutboundIPUpdate))
	mux.HandleFunc("DELETE /control/api/delivery/outbound-ips/{id}", s.require("delivery.manage", s.handleOutboundIPDelete))
	mux.HandleFunc("POST /control/api/delivery/outbound-ips/discover", s.require("delivery.manage", s.handleOutboundIPDiscover))
	mux.HandleFunc("GET /control/api/delivery/sender-routes", s.require("delivery.read", s.handleSenderRoutes))
	mux.HandleFunc("POST /control/api/delivery/sender-routes", s.require("delivery.manage", s.handleSenderRouteCreate))
	mux.HandleFunc("PATCH /control/api/delivery/sender-routes/{id}", s.require("delivery.manage", s.handleSenderRouteUpdate))
	mux.HandleFunc("DELETE /control/api/delivery/sender-routes/{id}", s.require("delivery.manage", s.handleSenderRouteDelete))

	mux.HandleFunc("GET /control/api/delivery/suppressions", s.require("delivery.read", s.handleSuppressions))
	mux.HandleFunc("POST /control/api/delivery/suppressions", s.require("delivery.manage", s.handleSuppressionCreate))
	mux.HandleFunc("PATCH /control/api/delivery/suppressions/{id}", s.require("delivery.manage", s.handleSuppressionUpdate))
	mux.HandleFunc("DELETE /control/api/delivery/suppressions/{id}", s.require("delivery.manage", s.handleSuppressionDelete))

	mux.HandleFunc("GET /control/api/delivery/blocklists", s.require("delivery.read", s.handleBlocklists))
	mux.HandleFunc("POST /control/api/delivery/blocklists/check", s.require("delivery.manage", s.handleBlocklistCheck))

	mux.HandleFunc("GET /control/api/delivery/deliverability", s.require("delivery.read", s.handleDeliverability))
}

func (s *Server) handleDeliverability(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	days := queryInt(r, "days", 30, 1, 90)
	rows, err := s.db.Query(r.Context(), `
		WITH events AS (
		  SELECT *, split_part(recipient::text, '@', 2) AS recipient_domain
		  FROM control.mail_events
		  WHERE event_at > now() - ($1::int || ' days')::interval
		)
		SELECT count(*) FILTER (WHERE status='sent') AS sent,
		       count(*) FILTER (WHERE status='bounced') AS bounced,
		       count(*) FILTER (WHERE status='deferred') AS deferred
		FROM events WHERE direction='outbound'
	`, days)
	if err != nil {
		writeFromError(w, err)
		return
	}
	var sent, bounced, deferred int64
	if rows.Next() {
		_ = rows.Scan(&sent, &bounced, &deferred)
	}
	rows.Close()
	rate := func(count, total int64) float64 {
		if total == 0 {
			return 0
		}
		return float64(count) / float64(total) * 100
	}
	byDomain := []map[string]any{}
	rows, err = s.db.Query(r.Context(), `
		SELECT split_part(recipient::text, '@', 2) AS domain,
		       count(*) FILTER (WHERE status='sent') AS sent,
		       count(*) FILTER (WHERE status='bounced') AS bounced,
		       count(*) FILTER (WHERE status='deferred') AS deferred
		FROM control.mail_events
		WHERE direction='outbound' AND event_at > now() - ($1::int || ' days')::interval
		GROUP BY 1 HAVING split_part(recipient::text, '@', 2) <> ''
		ORDER BY (count(*) FILTER (WHERE status='sent') + count(*) FILTER (WHERE status='bounced') + count(*) FILTER (WHERE status='deferred')) DESC LIMIT 50
	`, days)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var domain string
			var dSent, dBounced, dDeferred int64
			if rows.Scan(&domain, &dSent, &dBounced, &dDeferred) == nil {
				byDomain = append(byDomain, map[string]any{
					"domain": domain, "sent": dSent, "bounced": dBounced, "deferred": dDeferred,
					"bounceRate": rate(dBounced, dSent+dBounced+dDeferred), "deferralRate": rate(dDeferred, dSent+dBounced+dDeferred),
				})
			}
		}
	}
	bySender := []map[string]any{}
	rows, err = s.db.Query(r.Context(), `
		SELECT sender::text,
		       count(*) FILTER (WHERE status='sent') AS sent,
		       count(*) FILTER (WHERE status='bounced') AS bounced,
		       count(*) FILTER (WHERE status='deferred') AS deferred
		FROM control.mail_events
		WHERE direction='outbound' AND event_at > now() - ($1::int || ' days')::interval
		  AND sender <> ''
		GROUP BY 1 ORDER BY (count(*) FILTER (WHERE status='sent') + count(*) FILTER (WHERE status='bounced') + count(*) FILTER (WHERE status='deferred')) DESC LIMIT 50
	`, days)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sender string
			var sSent, sBounced, sDeferred int64
			if rows.Scan(&sender, &sSent, &sBounced, &sDeferred) == nil {
				bySender = append(bySender, map[string]any{
					"sender": sender, "sent": sSent, "bounced": sBounced, "deferred": sDeferred,
					"bounceRate": rate(sBounced, sSent+sBounced+sDeferred), "deferralRate": rate(sDeferred, sSent+sBounced+sDeferred),
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "sent": sent, "bounced": bounced, "deferred": deferred,
		"bounceRate": rate(bounced, sent+bounced+deferred), "deferralRate": rate(deferred, sent+bounced+deferred),
		"byDomain": byDomain, "bySender": bySender, "checkedAt": nowRFC3339(),
	})
}

func (s *Server) deliverabilitySummary(ctx context.Context) (int64, int64, int64, error) {
	var sent, bounced, deferred int64
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status='sent'),
		       count(*) FILTER (WHERE status='bounced'),
		       count(*) FILTER (WHERE status='deferred')
		FROM control.mail_events
		WHERE direction='outbound' AND event_at > now() - interval '30 days'
	`).Scan(&sent, &bounced, &deferred)
	return sent, bounced, deferred, err
}
