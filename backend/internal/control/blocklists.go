package control

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var blocklistZones = []struct {
	Name string
	Zone string
}{
	{"Spamhaus ZEN", "zen.spamhaus.org"},
	{"Barracuda", "b.barracudacentral.org"},
	{"SpamCop", "bl.spamcop.net"},
	{"SORBS", "dnsbl.sorbs.net"},
	{"UCEPROTECT L1", "dnsbl-1.uceprotect.net"},
	{"UCEPROTECT L2", "dnsbl-2.uceprotect.net"},
	{"UCEPROTECT L3", "dnsbl-3.uceprotect.net"},
	{"DroneBL", "dnsbl.dronebl.org"},
	{"JustSpam", "dnsbl.justspam.org"},
}

func dnsblKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return strings.Join([]string{strconv.Itoa(int(v4[3])), strconv.Itoa(int(v4[2])), strconv.Itoa(int(v4[1])), strconv.Itoa(int(v4[0]))}, ".")
	}
	raw := ip.To16()
	nibbles := make([]string, 0, 32)
	for i := len(raw) - 1; i >= 0; i-- {
		nibbles = append(nibbles, string("0123456789abcdef"[raw[i]&0x0f]))
		nibbles = append(nibbles, string("0123456789abcdef"[raw[i]>>4]))
	}
	return strings.Join(nibbles, ".")
}

// blocklistResponseBlocked recognises resolver-policy replies such as
// 127.255.255.254. Spamhaus returns these when a query comes through a public
// resolver; they are not listings and must not be shown as a reputation hit.
func blocklistResponseBlocked(response string) bool {
	for _, value := range strings.Split(response, ",") {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil && v4[0] == 127 && v4[1] == 255 && v4[2] == 255 {
			return true
		}
	}
	return false
}

func (s *Server) blocklistResolver() *net.Resolver {
	return s.dnsResolver(context.Background())
}

func (s *Server) blocklistIPs(ctx context.Context) []net.IP {
	rows, err := s.db.Query(ctx, `SELECT host(address) FROM control.outbound_ips WHERE active AND mode <> 'disabled' ORDER BY priority, address`)
	if err == nil {
		defer rows.Close()
		ips := []net.IP{}
		for rows.Next() {
			var value string
			if rows.Scan(&value) == nil {
				if ip := net.ParseIP(value); ip != nil && !ip.IsPrivate() && !ip.IsLoopback() {
					ips = append(ips, ip)
				}
			}
		}
		if len(ips) > 0 {
			return ips
		}
	}
	// Prefer an explicit Postfix source address when one is configured; that
	// is literally the address used for outbound SMTP.
	for _, key := range []string{"smtp_bind_address", "smtp_bind_address6"} {
		if configured := postconfValue(ctx, key); configured != "" {
			if ip := net.ParseIP(strings.TrimSpace(configured)); ip != nil && !ip.IsPrivate() && !ip.IsLoopback() {
				return []net.IP{ip}
			}
		}
	}
	if configured := s.settingString(ctx, "server.public_ipv4", ""); configured != "" {
		if ip := net.ParseIP(configured); ip != nil {
			return []net.IP{ip}
		}
	}
	addresses, _ := net.LookupIP(s.mailHostname(ctx))
	for _, ip := range addresses {
		if !ip.IsPrivate() && !ip.IsLoopback() {
			return []net.IP{ip}
		}
	}
	return nil
}

func (s *Server) handleBlocklists(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT DISTINCT ON (host(ip), blocklist) host(ip), blocklist, listed, response, checked_at
		FROM control.blocklist_results
		ORDER BY host(ip), blocklist, checked_at DESC
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	results := []map[string]any{}
	for rows.Next() {
		var ip, blocklist, response string
		var listed bool
		var checked time.Time
		if rows.Scan(&ip, &blocklist, &listed, &response, &checked) == nil {
			blocked := blocklistResponseBlocked(response)
			results = append(results, map[string]any{
				"ip": ip, "blocklist": blocklist, "listed": listed && !blocked,
				"blocked": blocked, "response": response, "checkedAt": checked.UTC().Format(time.RFC3339),
			})
		}
	}
	ips := []string{}
	for _, ip := range s.blocklistIPs(r.Context()) {
		ips = append(ips, ip.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "zones": blocklistZones, "ips": ips,
	})
}

func (s *Server) handleBlocklistCheck(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	results, err := s.runBlocklistCheck(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "delivery.blocklist_check", "DNSBL", "Ran outbound blocklist checks",
		map[string]any{"results": len(results)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results, "checkedAt": nowRFC3339()})
}

func (s *Server) runBlocklistCheck(ctx context.Context) ([]map[string]any, error) {
	ips := s.blocklistIPs(ctx)
	if len(ips) == 0 {
		return nil, fail(409, "NO_OUTBOUND_IPS", "Register or discover an outbound IP before checking blocklists")
	}
	resolver := s.blocklistResolver()
	results := []map[string]any{}
	for _, ip := range ips {
		key := dnsblKey(ip)
		for _, zone := range blocklistZones {
			query := key + "." + zone.Zone
			lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			addresses, err := resolver.LookupHost(lookupCtx, query)
			cancel()
			listed := err == nil && len(addresses) > 0
			response := strings.Join(addresses, ",")
			if err != nil {
				response = err.Error()
			}
			blocked := blocklistResponseBlocked(response)
			if blocked {
				listed = false
			}
			var previous bool
			_ = s.db.QueryRow(ctx, `
				SELECT listed FROM control.blocklist_results
				WHERE ip=$1 AND blocklist=$2 ORDER BY checked_at DESC LIMIT 1
			`, ip.String(), zone.Name).Scan(&previous)
			_, _ = s.db.Exec(ctx, `
				INSERT INTO control.blocklist_results (ip, blocklist, listed, response) VALUES ($1,$2,$3,$4)
			`, ip.String(), zone.Name, listed, truncateText(response, 500))
			if listed {
				_, _ = s.db.Exec(ctx, `UPDATE control.outbound_ips SET last_listed_at=now(), last_checked_at=now() WHERE host(address)=$1`, ip.String())
				if !previous {
					s.notifyEvent(ctx, "blocklist.listed", map[string]any{"ip": ip.String(), "blocklist": zone.Name, "response": response})
					s.hub.Broadcast("blocklist.listed", map[string]any{"ip": ip.String(), "blocklist": zone.Name})
				}
			} else {
				_, _ = s.db.Exec(ctx, `UPDATE control.outbound_ips SET last_checked_at=now() WHERE host(address)=$1`, ip.String())
			}
			results = append(results, map[string]any{
				"ip": ip.String(), "blocklist": zone.Name, "listed": listed,
				"blocked": blocked, "response": truncateText(response, 300),
			})
		}
	}
	return results, nil
}

func (s *Server) blocklistSummary(ctx context.Context) (listed int64) {
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.blocklist_results WHERE listed AND checked_at > now() - interval '24 hours'`).Scan(&listed)
	return
}

// deliverabilityHealth returns a dashboard-oriented view of the system IPs
// Postfix sends from and their latest DNSBL state. It reads persisted results
// (the hourly scheduler and manual checks write them) so the dashboard never
// blocks on dozens of DNS lookups.
func (s *Server) deliverabilityHealth(ctx context.Context) map[string]any {
	ips := s.blocklistIPs(ctx)
	zoneCount := len(blocklistZones)
	current := map[string]bool{}
	ipList := []string{}
	for _, ip := range ips {
		value := ip.String()
		current[value] = true
		ipList = append(ipList, value)
	}
	type latestResult struct {
		Listed    bool
		Blocked   bool
		CheckedAt time.Time
	}
	latest := map[string]map[string]latestResult{}
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (host(ip), blocklist) host(ip), blocklist, listed, response, checked_at
		FROM control.blocklist_results
		ORDER BY host(ip), blocklist, checked_at DESC
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ip, blocklist, response string
			var listed bool
			var checked time.Time
			if rows.Scan(&ip, &blocklist, &listed, &response, &checked) == nil {
				if latest[ip] == nil {
					latest[ip] = map[string]latestResult{}
				}
				latest[ip][blocklist] = latestResult{Listed: listed, Blocked: blocklistResponseBlocked(response), CheckedAt: checked}
			}
		}
	}
	entries := []map[string]any{}
	overall := "unchecked"
	totalListed := 0
	lastChecked := time.Time{}
	for _, ip := range ipList {
		results := latest[ip]
		checked := 0
		listedZones := []string{}
		blockedZones := []string{}
		zoneLast := time.Time{}
		for _, zone := range blocklistZones {
			result, ok := results[zone.Name]
			if !ok {
				continue
			}
			checked++
			if result.CheckedAt.After(zoneLast) {
				zoneLast = result.CheckedAt
			}
			if result.Listed {
				listedZones = append(listedZones, zone.Name)
			}
			if result.Blocked {
				blockedZones = append(blockedZones, zone.Name)
			}
		}
		state := "unchecked"
		if len(listedZones) > 0 {
			state = "listed"
			totalListed++
			overall = "listed"
		} else if len(blockedZones) > 0 {
			state = "blocked"
			if overall == "unchecked" || overall == "clear" {
				overall = "blocked"
			}
		} else if checked >= zoneCount {
			state = "clear"
			if overall == "unchecked" {
				overall = "clear"
			}
		} else if checked > 0 {
			state = "partial"
			if overall == "unchecked" {
				overall = "partial"
			}
		}
		if zoneLast.After(lastChecked) {
			lastChecked = zoneLast
		}
		entries = append(entries, map[string]any{
			"ip": ip, "state": state, "listed": len(listedZones) > 0,
			"listedZones": listedZones, "blockedZones": blockedZones, "zonesChecked": checked, "zonesTotal": zoneCount,
			"lastCheckedAt": func() string {
				if zoneLast.IsZero() {
					return ""
				}
				return zoneLast.UTC().Format(time.RFC3339)
			}(),
		})
	}
	if len(entries) == 0 {
		overall = "no_ips"
	}
	return map[string]any{
		"ips": entries, "zoneCount": zoneCount, "overall": overall,
		"listedIps": totalListed, "lastCheckedAt": func() string {
			if lastChecked.IsZero() {
				return ""
			}
			return lastChecked.UTC().Format(time.RFC3339)
		}(),
		"checkedAt": nowRFC3339(),
	}
}
