package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`)

func (s *Server) registerServerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/server/settings", s.require("server.read", s.handleServerSettings))
	mux.HandleFunc("PUT /control/api/server/settings", s.require("server.manage", s.handleServerSettingsSave))
	mux.HandleFunc("GET /control/api/server/network", s.require("server.read", s.handleNetwork))
	mux.HandleFunc("POST /control/api/server/addresses", s.require("server.manage", s.handleNetworkAddressCreate))
	mux.HandleFunc("DELETE /control/api/server/addresses/{id}", s.require("server.manage", s.handleNetworkAddressDelete))
	mux.HandleFunc("POST /control/api/server/connectivity-test", s.require("server.read", s.handleConnectivityTest))
	mux.HandleFunc("GET /control/api/server/time", s.require("server.read", s.handleTime))
	mux.HandleFunc("PUT /control/api/server/time", s.require("server.manage", s.handleTimeSave))
	mux.HandleFunc("POST /control/api/server/time/sync", s.require("server.manage", s.handleNTPSync))
	mux.HandleFunc("GET /control/api/server/firewall", s.require("server.read", s.handleFirewall))
	mux.HandleFunc("POST /control/api/server/firewall", s.require("server.manage", s.handleFirewallAdd))
	mux.HandleFunc("DELETE /control/api/server/firewall/{number}", s.require("server.manage", s.handleFirewallDelete))
	mux.HandleFunc("POST /control/api/server/firewall/state", s.require("server.manage", s.handleFirewallState))
}

func (s *Server) handleServerSettings(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	s.seedServerIdentity(r.Context())
	settings, err := s.allSettings(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "hostname": actualHostname()})
}

func actualHostname() string {
	name, _ := os.Hostname()
	return name
}

func (s *Server) handleServerSettingsSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Hostname      *string `json:"hostname"`
		MailHost      *string `json:"mailHostname"`
		WebmailURL    *string `json:"webmailUrl"`
		DNSResolver   *string `json:"dnsResolver"`
		PublicIPv4    *string `json:"publicIpv4"`
		PublicIPv6    *string `json:"publicIpv6"`
		MailFrom      *string `json:"mailFrom"`
		ApplyHostname bool    `json:"applyHostname"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	changes := map[string]string{}
	if body.Hostname != nil {
		hostname := normalizeDomain(*body.Hostname)
		if !validDomain(hostname) {
			writeError(w, http.StatusBadRequest, "HOSTNAME", "Hostname is invalid")
			return
		}
		changes["server.hostname"] = hostname
		if body.ApplyHostname {
			result, err := runCommand(r.Context(), 20*time.Second, "hostnamectl", "set-hostname", hostname)
			if err != nil {
				writeError(w, http.StatusBadGateway, "HOSTNAME", "hostnamectl failed: "+strings.TrimSpace(result.Stderr))
				return
			}
		}
	}
	if body.MailHost != nil {
		hostname := normalizeDomain(*body.MailHost)
		if !validDomain(hostname) {
			writeError(w, http.StatusBadRequest, "MAIL_HOST", "Mail hostname is invalid")
			return
		}
		changes["server.mail_hostname"] = hostname
	}
	if body.WebmailURL != nil {
		parsed, err := url.Parse(strings.TrimSpace(*body.WebmailURL))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			writeError(w, http.StatusBadRequest, "WEBMAIL_URL", "Webmail URL must be an http(s) URL")
			return
		}
		changes["server.webmail_url"] = strings.TrimRight(parsed.String(), "/")
	}
	if body.DNSResolver != nil {
		resolver, err := normalizeResolver(*body.DNSResolver)
		if err != nil {
			writeError(w, http.StatusBadRequest, "DNS_RESOLVER", err.Error())
			return
		}
		changes["server.dns_resolver"] = resolver
	}
	for key, pointer := range map[string]*string{"server.public_ipv4": body.PublicIPv4, "server.public_ipv6": body.PublicIPv6} {
		if pointer == nil {
			continue
		}
		value := strings.TrimSpace(*pointer)
		if value != "" {
			ip := net.ParseIP(value)
			if ip == nil || (key == "server.public_ipv4" && ip.To4() == nil) || (key == "server.public_ipv6" && ip.To4() != nil) {
				writeError(w, http.StatusBadRequest, "PUBLIC_IP", "Public IP is invalid for "+key)
				return
			}
			value = ip.String()
		}
		changes[key] = value
	}
	if body.MailFrom != nil {
		if _, err := mail.ParseAddress(*body.MailFrom); err != nil {
			writeError(w, http.StatusBadRequest, "MAIL_FROM", "Mail from address is invalid")
			return
		}
		changes["server.mail_from"] = strings.TrimSpace(*body.MailFrom)
	}
	for key, value := range changes {
		if err := s.setSetting(r.Context(), key, value, sess.User.ID); err != nil {
			writeFromError(w, err)
			return
		}
	}
	s.actorAudit(r, sess, "server.settings_updated", "Server settings", "Updated server and mail identity settings", map[string]any{"keys": sortedKeys(changes)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": sortedKeys(changes)})
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func normalizeResolver(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("DNS resolver is required")
	}
	if _, _, err := net.SplitHostPort(value); err != nil {
		value = net.JoinHostPort(value, "53")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || net.ParseIP(host) == nil {
		return "", fmt.Errorf("DNS resolver must be an IP address with an optional port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("DNS resolver port is invalid")
	}
	return net.JoinHostPort(host, strconv.Itoa(number)), nil
}

func (s *Server) dnsResolver(ctx context.Context) *net.Resolver {
	address := s.settingString(ctx, "server.dns_resolver", s.cfg.DNSResolver)
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "53")
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 4 * time.Second}
			return dialer.DialContext(ctx, "udp", address)
		},
	}
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 10*time.Second, "ip", "-j", "addr", "show")
	if err != nil {
		writeError(w, http.StatusBadGateway, "NETWORK", "Could not inspect interfaces")
		return
	}
	var interfaces any
	_ = json.Unmarshal([]byte(result.Stdout), &interfaces)
	routesResult, _ := runCommand(r.Context(), 10*time.Second, "ip", "-j", "route", "show")
	var routes any
	_ = json.Unmarshal([]byte(routesResult.Stdout), &routes)
	rows, _ := s.db.Query(r.Context(), `SELECT id::text,interface_name,address_cidr,label,active,created_at FROM control.network_addresses ORDER BY created_at DESC`)
	managed := []map[string]any{}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id, iface, address, label string
			var active bool
			var created time.Time
			if rows.Scan(&id, &iface, &address, &label, &active, &created) == nil {
				managed = append(managed, map[string]any{"id": id, "interface": iface, "address": address,
					"label": label, "active": active, "createdAt": created.UTC().Format(time.RFC3339)})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": interfaces, "routes": routes, "managedAddresses": managed})
}

func (s *Server) handleNetworkAddressCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Interface string `json:"interface"`
		Address   string `json:"address"`
		Prefix    int    `json:"prefix"`
		Label     string `json:"label"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !interfacePattern.MatchString(body.Interface) {
		writeError(w, http.StatusBadRequest, "INTERFACE", "Invalid interface name")
		return
	}
	ip := net.ParseIP(strings.TrimSpace(body.Address))
	if ip == nil {
		writeError(w, http.StatusBadRequest, "ADDRESS", "Invalid IP address")
		return
	}
	if body.Prefix == 0 {
		if ip.To4() != nil {
			body.Prefix = 32
		} else {
			body.Prefix = 128
		}
	}
	maxPrefix := 128
	if ip.To4() != nil {
		maxPrefix = 32
	}
	if body.Prefix < 1 || body.Prefix > maxPrefix {
		writeError(w, http.StatusBadRequest, "PREFIX", "Invalid prefix length")
		return
	}
	cidr := fmt.Sprintf("%s/%d", ip.String(), body.Prefix)
	result, err := runCommand(r.Context(), 20*time.Second, "ip", "addr", "add", cidr, "dev", body.Interface)
	if err != nil {
		writeError(w, http.StatusBadGateway, "NETWORK", "ip addr add failed: "+strings.TrimSpace(result.Stderr))
		return
	}
	var id string
	_ = s.db.QueryRow(r.Context(), `
		INSERT INTO control.network_addresses (interface_name,address_cidr,label,created_by)
		VALUES ($1,$2,$3,$4) RETURNING id::text
	`, body.Interface, cidr, strings.TrimSpace(body.Label), sess.User.ID).Scan(&id)
	s.actorAudit(r, sess, "server.address_added", cidr, "Added an IP address to an interface", map[string]any{"interface": body.Interface, "runtimeOnly": true})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "address": cidr,
		"note": "The address is active now. Add it to the host's network configuration if it must survive a reboot."})
}

func (s *Server) handleNetworkAddressDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var iface, cidr string
	if err := s.db.QueryRow(r.Context(), `SELECT interface_name,address_cidr FROM control.network_addresses WHERE id=$1`, id).Scan(&iface, &cidr); err != nil {
		writeFromError(w, err)
		return
	}
	result, err := runCommand(r.Context(), 20*time.Second, "ip", "addr", "del", cidr, "dev", iface)
	if err != nil {
		writeError(w, http.StatusBadGateway, "NETWORK", "ip addr del failed: "+strings.TrimSpace(result.Stderr))
		return
	}
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.network_addresses WHERE id=$1`, id)
	s.actorAudit(r, sess, "server.address_removed", cidr, "Removed an IP address from an interface", map[string]any{"interface": iface})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConnectivityTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Timeout int    `json:"timeoutSeconds"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Host = strings.TrimSpace(body.Host)
	if body.Host == "" || body.Port < 1 || body.Port > 65535 {
		writeError(w, http.StatusBadRequest, "TEST", "Host and port are required")
		return
	}
	if body.Timeout == 0 || body.Timeout > 20 {
		body.Timeout = 5
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(body.Host, strconv.Itoa(body.Port)), time.Duration(body.Timeout)*time.Second)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "elapsedMs": time.Since(start).Milliseconds()})
		return
	}
	conn.Close()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "host": body.Host, "port": body.Port, "elapsedMs": time.Since(start).Milliseconds()})
}

func (s *Server) handleTime(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	timezone := commandValue(r.Context(), "timedatectl", "show", "--property=Timezone", "--value")
	ntp := commandValue(r.Context(), "timedatectl", "show", "--property=NTP", "--value")
	sync := commandValue(r.Context(), "timedatectl", "show", "--property=NTP,synchronized", "--value")
	now := commandValue(r.Context(), "date", "--rfc-3339=seconds")
	writeJSON(w, http.StatusOK, map[string]any{"timezone": timezone, "ntp": ntp, "synchronized": sync, "now": now})
}

func commandValue(ctx context.Context, name string, args ...string) string {
	result, err := runCommand(ctx, 10*time.Second, name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func (s *Server) handleTimeSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Timezone = strings.TrimSpace(body.Timezone)
	if !validTimezone(r.Context(), body.Timezone) {
		writeError(w, http.StatusBadRequest, "TIMEZONE", "Unknown IANA time zone")
		return
	}
	result, err := runCommand(r.Context(), 20*time.Second, "timedatectl", "set-timezone", body.Timezone)
	if err != nil {
		writeError(w, http.StatusBadGateway, "TIMEZONE", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "server.timezone_updated", body.Timezone, "Changed the server time zone", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "timezone": body.Timezone})
}

func validTimezone(ctx context.Context, zone string) bool {
	if zone == "" || strings.ContainsAny(zone, " \t\r\n;'\"") {
		return false
	}
	zones := strings.Fields(commandValue(ctx, "timedatectl", "list-timezones"))
	for _, candidate := range zones {
		if candidate == zone {
			return true
		}
	}
	_, err := os.Stat(filepath.Join("/usr/share/zoneinfo", filepath.FromSlash(zone)))
	return err == nil && !strings.Contains(zone, "..")
}

func (s *Server) handleNTPSync(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 30*time.Second, "chronyc", "makestep")
	if err != nil {
		writeError(w, http.StatusBadGateway, "NTP", "NTP step failed: "+strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "server.ntp_sync", "chrony", "Requested an NTP step", map[string]any{"output": strings.TrimSpace(result.Stdout)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": strings.TrimSpace(result.Stdout)})
}

func (s *Server) handleFirewall(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	if path, err := exec.LookPath("ufw"); err == nil {
		result, err := runCommand(r.Context(), 10*time.Second, path, "status", "numbered")
		if err != nil {
			writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"backend": "ufw", "raw": result.Stdout})
		return
	}
	if path, err := exec.LookPath("firewall-cmd"); err == nil {
		result, err := runCommand(r.Context(), 10*time.Second, path, "--list-all")
		if err != nil {
			writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"backend": "firewalld", "raw": result.Stdout})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backend": "none", "raw": "No ufw or firewalld command found"})
}

func (s *Server) handleFirewallAdd(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Port  int    `json:"port"`
		Proto string `json:"proto"`
		From  string `json:"from"`
		Deny  bool   `json:"deny"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Port < 1 || body.Port > 65535 || (body.Proto != "tcp" && body.Proto != "udp") {
		writeError(w, http.StatusBadRequest, "FIREWALL", "Port and protocol are invalid")
		return
	}
	if body.From != "" && net.ParseIP(body.From) == nil {
		writeError(w, http.StatusBadRequest, "FIREWALL", "Source address is invalid")
		return
	}
	action := "allow"
	if body.Deny {
		action = "deny"
	}
	var args []string
	if path, err := exec.LookPath("ufw"); err == nil {
		if body.From != "" {
			args = []string{action, "from", body.From, "to", "any", "port", strconv.Itoa(body.Port), "proto", body.Proto}
		} else {
			args = []string{action, strconv.Itoa(body.Port) + "/" + body.Proto}
		}
		result, err := runCommand(r.Context(), 20*time.Second, path, args...)
		if err != nil {
			writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
			return
		}
		s.actorAudit(r, sess, "server.firewall_rule_added", fmt.Sprintf("%d/%s", body.Port, body.Proto), "Added a firewall rule", map[string]any{"from": body.From, "deny": body.Deny})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backend": "ufw", "output": strings.TrimSpace(result.Stdout)})
		return
	}
	if path, err := exec.LookPath("firewall-cmd"); err == nil {
		args = []string{"--permanent", "--add-port=" + strconv.Itoa(body.Port) + "/" + body.Proto}
		if body.From != "" {
			args = []string{"--permanent", "--add-rich-rule=rule family=ipv4 source address=" + body.From + " port port=" + strconv.Itoa(body.Port) + " protocol=" + body.Proto + " " + action}
		}
		result, err := runCommand(r.Context(), 20*time.Second, path, args...)
		if err != nil {
			writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
			return
		}
		_, _ = runCommand(r.Context(), 20*time.Second, path, "--reload")
		s.actorAudit(r, sess, "server.firewall_rule_added", fmt.Sprintf("%d/%s", body.Port, body.Proto), "Added a firewalld rule", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backend": "firewalld", "output": strings.TrimSpace(result.Stdout)})
		return
	}
	writeError(w, http.StatusConflict, "FIREWALL", "No supported firewall manager is installed")
}

func (s *Server) handleFirewallDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	number, err := strconv.Atoi(strings.TrimSpace(r.PathValue("number")))
	if err != nil || number < 1 {
		writeError(w, http.StatusBadRequest, "FIREWALL", "Rule number is invalid")
		return
	}
	if path, lookupErr := exec.LookPath("ufw"); lookupErr == nil {
		result, err := runCommand(r.Context(), 20*time.Second, path, "--force", "delete", strconv.Itoa(number))
		if err != nil {
			writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
			return
		}
		s.actorAudit(r, sess, "server.firewall_rule_deleted", "ufw rule "+strconv.Itoa(number), "Deleted a firewall rule", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": strings.TrimSpace(result.Stdout)})
		return
	}
	writeError(w, http.StatusConflict, "FIREWALL", "Numbered deletion is only supported for ufw")
}

func (s *Server) handleFirewallState(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	path, err := exec.LookPath("ufw")
	if err != nil {
		writeError(w, http.StatusConflict, "FIREWALL", "ufw is not installed")
		return
	}
	action := "disable"
	if body.Enabled {
		action = "enable"
	}
	result, err := runCommand(r.Context(), 20*time.Second, path, "--force", action)
	if err != nil {
		writeError(w, http.StatusBadGateway, "FIREWALL", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "server.firewall_state", action, "Changed firewall state", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": strings.TrimSpace(result.Stdout)})
}

// ---- PostgreSQL management ----

func (s *Server) registerDatabaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/database", s.require("database.read", s.handleDatabaseStatus))
	mux.HandleFunc("POST /control/api/database/provision", s.require("database.manage", s.handleDatabaseProvision))
	mux.HandleFunc("POST /control/api/database/test", s.require("database.read", s.handleDatabaseTest))
	mux.HandleFunc("POST /control/api/database/migrate", s.require("database.manage", s.handleDatabaseMigrate))
	mux.HandleFunc("GET /control/api/database/migrate", s.require("database.read", s.handleDatabaseMigrateList))
	mux.HandleFunc("POST /control/api/database/restart", s.require("database.manage", s.handleDatabaseRestart))
}

func (s *Server) handleDatabaseStatus(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var version string
	var size, connections, maxConnections int64
	var cacheHit float64
	_ = s.db.QueryRow(r.Context(), `SELECT version()`).Scan(&version)
	_ = s.db.QueryRow(r.Context(), `SELECT pg_database_size(current_database())`).Scan(&size)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM pg_stat_activity`).Scan(&connections)
	_ = s.db.QueryRow(r.Context(), `SELECT setting::bigint FROM pg_settings WHERE name='max_connections'`).Scan(&maxConnections)
	_ = s.db.QueryRow(r.Context(), `
		SELECT COALESCE(round(100.0 * sum(blks_hit) / NULLIF(sum(blks_hit + blks_read),0), 2),0)
		FROM pg_stat_database
	`).Scan(&cacheHit)
	databases := []map[string]any{}
	rows, _ := s.db.Query(r.Context(), `SELECT datname, pg_database_size(datname), (SELECT count(*) FROM pg_stat_activity a WHERE a.datname=d.datname) FROM pg_database d WHERE datistemplate=false ORDER BY datname`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			var bytes, conns int64
			if rows.Scan(&name, &bytes, &conns) == nil {
				databases = append(databases, map[string]any{"name": name, "sizeBytes": bytes, "connections": conns})
			}
		}
	}
	activity := []map[string]any{}
	rows, _ = s.db.Query(r.Context(), `
		SELECT pid, usename, datname, state, COALESCE(round(extract(epoch FROM (now()-query_start))),0), left(query,200)
		FROM pg_stat_activity WHERE state <> 'idle' AND pid <> pg_backend_pid() ORDER BY query_start LIMIT 20
	`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var pid int
			var user, db, state, query string
			var seconds int64
			if rows.Scan(&pid, &user, &db, &state, &seconds, &query) == nil {
				activity = append(activity, map[string]any{"pid": pid, "user": user, "database": db,
					"state": state, "seconds": seconds, "query": query})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version, "databaseSizeBytes": size, "connections": connections,
		"maxConnections": maxConnections, "cacheHitPercent": cacheHit,
		"databases": databases, "activity": activity,
		"service": commandValue(r.Context(), "systemctl", "is-active", "postgresql@15-main.service"),
	})
}

func validatePGIdentifier(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' {
			continue
		}
		if index > 0 && ((char >= '0' && char <= '9') || char == '$') {
			continue
		}
		return false
	}
	return true
}

func (s *Server) handleDatabaseProvision(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Role     string `json:"role"`
		Database string `json:"database"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !validatePGIdentifier(body.Role) || !validatePGIdentifier(body.Database) {
		writeError(w, http.StatusBadRequest, "IDENTIFIER", "Role and database names must be valid PostgreSQL identifiers")
		return
	}
	password, err := randomPassword(32)
	if err != nil {
		writeFromError(w, err)
		return
	}
	escapedPassword := strings.ReplaceAll(password, "'", "''")
	roleSQL := fmt.Sprintf(`CREATE ROLE "%s" LOGIN PASSWORD '%s'`, body.Role, escapedPassword)
	dbSQL := fmt.Sprintf(`CREATE DATABASE "%s" OWNER "%s"`, body.Database, body.Role)
	if result, err := runCommandWithInput(r.Context(), 30*time.Second, "", "runuser", "-u", "postgres", "--", "psql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", roleSQL); err != nil {
		writeError(w, http.StatusBadGateway, "DATABASE", strings.TrimSpace(result.Stderr))
		return
	}
	if result, err := runCommandWithInput(r.Context(), 30*time.Second, "", "runuser", "-u", "postgres", "--", "psql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", dbSQL); err != nil {
		writeError(w, http.StatusBadGateway, "DATABASE", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "database.provisioned", body.Database, "Provisioned a PostgreSQL role and database",
		map[string]any{"role": body.Role, "database": body.Database})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "role": body.Role, "database": body.Database,
		"password": password, "message": "Copy the database password now. It is not stored in clear text."})
}

func (s *Server) handleDatabaseTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Database string `json:"database"`
		User     string `json:"user"`
		Password string `json:"password"`
		SSLMode  string `json:"sslmode"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Host == "" {
		body.Host = "127.0.0.1"
	}
	if body.Port == 0 {
		body.Port = 5432
	}
	if body.SSLMode == "" {
		body.SSLMode = "disable"
	}
	if !validatePGIdentifier(body.Database) || !validatePGIdentifier(body.User) {
		writeError(w, http.StatusBadRequest, "IDENTIFIER", "Database and user are invalid")
		return
	}
	connection := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=%s",
		url.QueryEscape(body.User), url.QueryEscape(body.Password), net.JoinHostPort(body.Host, strconv.Itoa(body.Port)), body.Database, body.SSLMode)
	start := time.Now()
	pool, err := OpenDatabase(r.Context(), connection)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer pool.Close()
	var version string
	_ = pool.QueryRow(r.Context(), `SELECT version()`).Scan(&version)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version, "elapsedMs": time.Since(start).Milliseconds()})
}

func (s *Server) handleDatabaseMigrate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		TargetDatabase string `json:"targetDatabase"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !validatePGIdentifier(body.TargetDatabase) || body.TargetDatabase == "a8m" {
		writeError(w, http.StatusBadRequest, "DATABASE", "Choose a different valid target database")
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.database_migrations (target_database,status,created_by)
		VALUES ($1,'queued',$2) RETURNING id::text
	`, body.TargetDatabase, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	go s.runDatabaseMigration(id, body.TargetDatabase)
	s.actorAudit(r, sess, "database.migration_started", body.TargetDatabase, "Started a background database copy", nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": id, "status": "queued"})
}

func (s *Server) runDatabaseMigration(id, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	log := strings.Builder{}
	set := func(progress int, status, line string) {
		log.WriteString(line + "\n")
		_, _ = s.db.Exec(ctx, `UPDATE control.database_migrations SET status=$2,progress=$3,log=$4,started_at=COALESCE(started_at,now()) WHERE id=$1`, id, status, progress, log.String())
	}
	set(5, "running", "Creating dump")
	temp, err := os.CreateTemp("", "alt8mail-migrate-*.dump")
	if err != nil {
		set(0, "failed", err.Error())
		return
	}
	tempPath := temp.Name()
	temp.Close()
	defer os.Remove(tempPath)
	if err := runCommandToFile(ctx, tempPath, "runuser", "-u", "postgres", "--", "pg_dump", "-Fc", "a8m"); err != nil {
		set(10, "failed", "pg_dump: "+err.Error())
		return
	}
	set(40, "running", "Dump complete; restoring into "+target)
	if result, err := runCommand(ctx, 90*time.Minute, "runuser", "-u", "postgres", "--", "pg_restore", "--clean", "--if-exists", "--no-owner", "--no-privileges", "-d", target, tempPath); err != nil {
		set(60, "failed", "pg_restore: "+strings.TrimSpace(result.Stderr))
		return
	}
	set(90, "running", "Restore complete; verifying")
	var count int64
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains`).Scan(&count)
	set(100, "completed", fmt.Sprintf("Migration complete (%d source domains checked)", count))
}

func (s *Server) handleDatabaseMigrateList(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,target_database,status,progress,log,created_at,started_at,finished_at
		FROM control.database_migrations ORDER BY created_at DESC LIMIT 20
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, target, status, log string
		var progress int
		var created time.Time
		var started, finished *time.Time
		if rows.Scan(&id, &target, &status, &progress, &log, &created, &started, &finished) == nil {
			item := map[string]any{"id": id, "target": target, "status": status, "progress": progress,
				"log": log, "createdAt": created.UTC().Format(time.RFC3339)}
			if started != nil {
				item["startedAt"] = started.UTC().Format(time.RFC3339)
			}
			if finished != nil {
				item["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"migrations": items})
}

func (s *Server) handleDatabaseRestart(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if strings.TrimSpace(body.Confirm) != "RESTART" {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type RESTART to confirm")
		return
	}
	result, err := runCommand(r.Context(), 60*time.Second, "systemctl", "restart", "postgresql@15-main.service")
	if err != nil {
		writeError(w, http.StatusBadGateway, "DATABASE", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "database.service_restarted", "PostgreSQL", "Restarted PostgreSQL", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
