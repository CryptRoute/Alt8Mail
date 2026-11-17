package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var transportPattern = regexp.MustCompile(`^smtp-[a-z0-9-]{1,24}$`)

type outboundIP struct {
	ID           string `json:"id"`
	Address      string `json:"address"`
	Label        string `json:"label"`
	Mode         string `json:"mode"`
	Priority     int    `json:"priority"`
	Interface    string `json:"interface"`
	HELO         string `json:"heloName"`
	Active       bool   `json:"active"`
	Notes        string `json:"notes"`
	LastListedAt any    `json:"lastListedAt"`
}

func (s *Server) handleOutboundIPs(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, host(address), label, mode, priority, interface_name, helo_name,
		       active, notes, last_listed_at
		FROM control.outbound_ips ORDER BY active DESC, priority, address
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []outboundIP{}
	for rows.Next() {
		var item outboundIP
		if rows.Scan(&item.ID, &item.Address, &item.Label, &item.Mode, &item.Priority,
			&item.Interface, &item.HELO, &item.Active, &item.Notes, &item.LastListedAt) == nil {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ips": items})
}

func (s *Server) handleOutboundIPCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body outboundIP
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	ip := net.ParseIP(strings.TrimSpace(body.Address))
	if ip == nil {
		writeError(w, http.StatusBadRequest, "IP", "Enter a valid IPv4 or IPv6 address")
		return
	}
	if body.Mode != "primary" && body.Mode != "backup" && body.Mode != "disabled" {
		body.Mode = "backup"
	}
	if body.Priority <= 0 {
		body.Priority = 100
	}
	if body.HELO != "" && !validDomain(normalizeDomain(body.HELO)) {
		writeError(w, http.StatusBadRequest, "HELO", "HELO name is invalid")
		return
	}
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.outbound_ips (address, label, mode, priority, interface_name, helo_name, active, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text
	`, ip.String(), strings.TrimSpace(body.Label), body.Mode, body.Priority,
		strings.TrimSpace(body.Interface), normalizeDomain(body.HELO), body.Mode != "disabled", strings.TrimSpace(body.Notes)).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.outbound_ip_created", ip.String(), "Registered an outbound IP", map[string]any{"mode": body.Mode, "priority": body.Priority})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleOutboundIPUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Label     *string `json:"label"`
		Mode      *string `json:"mode"`
		Priority  *int    `json:"priority"`
		Interface *string `json:"interfaceName"`
		HELO      *string `json:"heloName"`
		Active    *bool   `json:"active"`
		Notes     *string `json:"notes"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	current, err := s.db.Exec(r.Context(), `
		UPDATE control.outbound_ips SET
		  label=COALESCE($2,label), mode=COALESCE($3,mode), priority=COALESCE($4,priority),
		  interface_name=COALESCE($5,interface_name), helo_name=COALESCE($6,helo_name),
		  active=COALESCE($7,active), notes=COALESCE($8,notes), updated_at=now()
		WHERE id=$1
	`, id, body.Label, body.Mode, body.Priority, body.Interface,
		nullNormalizeDomain(body.HELO), body.Active, body.Notes)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if current.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Outbound IP not found")
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.outbound_ip_updated", id, "Updated an outbound IP", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleOutboundIPDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.outbound_ips WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Outbound IP not found")
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.outbound_ip_deleted", id, "Removed an outbound IP", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func nullNormalizeDomain(value *string) any {
	if value == nil {
		return nil
	}
	return normalizeDomain(*value)
}

func (s *Server) handleOutboundIPDiscover(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 10*time.Second, "ip", "-j", "addr", "show")
	if err != nil {
		writeError(w, http.StatusBadGateway, "NETWORK", "Could not inspect network interfaces")
		return
	}
	type addrInfo struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	}
	type iface struct {
		Name     string     `json:"ifname"`
		AddrInfo []addrInfo `json:"addr_info"`
	}
	var interfaces []iface
	if err := json.Unmarshal([]byte(result.Stdout), &interfaces); err != nil {
		writeError(w, http.StatusInternalServerError, "NETWORK", "Could not parse interface inventory")
		return
	}
	added := []string{}
	for _, iface := range interfaces {
		for _, address := range iface.AddrInfo {
			ip := net.ParseIP(address.Local)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsPrivate() {
				continue
			}
			tag, err := s.db.Exec(r.Context(), `
				INSERT INTO control.outbound_ips (address, label, mode, priority, interface_name, active, notes)
				VALUES ($1,$2,'backup',200,$3,true,'Discovered from network interfaces')
				ON CONFLICT (address) DO NOTHING
			`, ip.String(), iface.Name, iface.Name)
			if err == nil && tag.RowsAffected() > 0 {
				added = append(added, ip.String())
			}
		}
	}
	s.actorAudit(r, sess, "delivery.outbound_ip_discovered", "Network interfaces", "Discovered public outbound addresses", map[string]any{"added": added})
	if len(added) > 0 {
		s.notifyEvent(r.Context(), "outbound.ip_discovered", map[string]any{"addresses": added})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}

type senderRoute struct {
	ID            string `json:"id"`
	SenderDomain  string `json:"senderDomain"`
	SourceIPID    string `json:"sourceIpId"`
	SourceAddress string `json:"sourceAddress"`
	HELO          string `json:"heloName"`
	TransportName string `json:"transportName"`
	Active        bool   `json:"active"`
}

func (s *Server) handleSenderRoutes(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT r.id::text, r.sender_domain::text, COALESCE(r.source_ip_id::text,''),
		       COALESCE(host(i.address),''), r.helo_name, r.transport_name, r.active
		FROM control.sender_routes r
		LEFT JOIN control.outbound_ips i ON i.id = r.source_ip_id
		ORDER BY r.sender_domain
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []senderRoute{}
	for rows.Next() {
		var item senderRoute
		if rows.Scan(&item.ID, &item.SenderDomain, &item.SourceIPID, &item.SourceAddress, &item.HELO, &item.TransportName, &item.Active) == nil {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": items})
}

func (s *Server) handleSenderRouteCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		SenderDomain  string `json:"senderDomain"`
		SourceIPID    string `json:"sourceIpId"`
		HELO          string `json:"heloName"`
		TransportName string `json:"transportName"`
		Active        *bool  `json:"active"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	domain := normalizeDomain(body.SenderDomain)
	if !validDomain(domain) {
		writeError(w, http.StatusBadRequest, "DOMAIN", "Sender domain is invalid")
		return
	}
	if body.SourceIPID != "" && !validUUID(body.SourceIPID) {
		writeError(w, http.StatusBadRequest, "IP", "Source IP id is invalid")
		return
	}
	transport := strings.ToLower(strings.TrimSpace(body.TransportName))
	if transport != "" && !transportPattern.MatchString(transport) {
		writeError(w, http.StatusBadRequest, "TRANSPORT", "Transport name must start with smtp- and use only letters, digits and hyphens")
		return
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.sender_routes (sender_domain, source_ip_id, helo_name, transport_name, active)
		VALUES ($1,NULLIF($2,'')::uuid,$3,$4,$5) RETURNING id::text
	`, domain, body.SourceIPID, normalizeDomain(body.HELO), transport, active).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.sender_route_created", domain, "Added an outbound sender route", nil)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleSenderRouteUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		SourceIPID    *string `json:"sourceIpId"`
		HELO          *string `json:"heloName"`
		TransportName *string `json:"transportName"`
		Active        *bool   `json:"active"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.TransportName != nil && *body.TransportName != "" && !transportPattern.MatchString(strings.ToLower(*body.TransportName)) {
		writeError(w, http.StatusBadRequest, "TRANSPORT", "Invalid transport name")
		return
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.sender_routes SET
		  source_ip_id=COALESCE(NULLIF($2,'')::uuid,source_ip_id),
		  helo_name=COALESCE($3,helo_name),
		  transport_name=COALESCE($4,transport_name),
		  active=COALESCE($5,active), updated_at=now()
		WHERE id=$1
	`, id, body.SourceIPID, body.HELO, body.TransportName, body.Active)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Sender route not found")
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.sender_route_updated", id, "Updated an outbound sender route", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSenderRouteDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.sender_routes WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Sender route not found")
		return
	}
	_, _ = s.writeSenderTransport(r.Context())
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "delivery.sender_route_deleted", id, "Removed an outbound sender route", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) writeSenderTransport(ctx context.Context) (bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id::text, r.sender_domain::text, COALESCE(r.source_ip_id::text,''),
		       COALESCE(host(i.address),''), COALESCE(NULLIF(r.helo_name,''), NULLIF(i.helo_name,''), $1),
		       r.transport_name, i.mode, i.active
		FROM control.sender_routes r
		LEFT JOIN control.outbound_ips i ON i.id=r.source_ip_id
		WHERE r.active
		ORDER BY r.sender_domain
	`, s.mailHostname(ctx))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	transportLines := []string{}
	blocks := []string{}
	for rows.Next() {
		var routeID, domain, ipID, address, helo, transportName, ipMode string
		var ipActive bool
		if rows.Scan(&routeID, &domain, &ipID, &address, &helo, &transportName, &ipMode, &ipActive) != nil {
			continue
		}
		if ipID == "" || address == "" || !ipActive || ipMode == "disabled" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(transportName))
		if name == "" || !transportPattern.MatchString(name) {
			name = "smtp-r-" + strings.ReplaceAll(routeID[:8], "-", "")
		}
		transportLines = append(transportLines, "@"+domain+" "+name+":")
		blocks = append(blocks, name+" unix - - n - - smtp",
			"    -o smtp_bind_address="+address,
			"    -o smtp_helo_name="+helo)
	}
	if len(transportLines) == 0 {
		changedMap, err := writeManagedFile(filepath.Join(s.cfg.PostfixDir, "sender_transport"), "# Managed by Alt8Mail System Control\n", 0o644, 0, 0)
		if err != nil {
			return false, err
		}
		changedMaster, err := s.writeMasterTransportBlock(ctx, nil)
		if err != nil {
			return changedMap, err
		}
		settingChanged, err := postconfEnsure(ctx, "sender_dependent_default_transport_maps", "")
		if err != nil {
			return false, err
		}
		// Choose the primary outbound address as the default source.
		primary, helo := s.primaryOutbound(ctx)
		changedPrimary, err := postconfEnsure(ctx, "smtp_bind_address", primary)
		if err != nil {
			return false, err
		}
		changedHELO, err := postconfEnsure(ctx, "smtp_helo_name", helo)
		if err != nil {
			return false, err
		}
		return changedMap || changedMaster || settingChanged || changedPrimary || changedHELO, nil
	}
	content := "# Managed by Alt8Mail System Control\n" + strings.Join(transportLines, "\n") + "\n"
	changedMap, err := writeManagedFile(filepath.Join(s.cfg.PostfixDir, "sender_transport"), content, 0o644, 0, 0)
	if err != nil {
		return false, err
	}
	if _, err := runCommand(ctx, 15*time.Second, "postmap", filepath.Join(s.cfg.PostfixDir, "sender_transport")); err != nil {
		return changedMap, fmt.Errorf("postmap sender_transport failed")
	}
	changedMaster, err := s.writeMasterTransportBlock(ctx, blocks)
	if err != nil {
		return changedMap, err
	}
	settingChanged, err := postconfEnsure(ctx, "sender_dependent_default_transport_maps", "hash:"+filepath.Join(s.cfg.PostfixDir, "sender_transport"))
	if err != nil {
		return false, err
	}
	primary, helo := s.primaryOutbound(ctx)
	changedPrimary, _ := postconfEnsure(ctx, "smtp_bind_address", primary)
	changedHELO, _ := postconfEnsure(ctx, "smtp_helo_name", helo)
	return changedMap || changedMaster || settingChanged || changedPrimary || changedHELO, nil
}

func (s *Server) primaryOutbound(ctx context.Context) (string, string) {
	var address, helo string
	_ = s.db.QueryRow(ctx, `
		SELECT host(address), COALESCE(NULLIF(helo_name,''),$1)
		FROM control.outbound_ips
		WHERE active AND mode='primary'
		ORDER BY priority, address LIMIT 1
	`, s.mailHostname(ctx)).Scan(&address, &helo)
	if address == "" {
		_ = s.db.QueryRow(ctx, `
			SELECT host(address), COALESCE(NULLIF(helo_name,''),$1)
			FROM control.outbound_ips WHERE active ORDER BY priority, address LIMIT 1
		`, s.mailHostname(ctx)).Scan(&address, &helo)
	}
	return address, helo
}

func (s *Server) writeMasterTransportBlock(ctx context.Context, lines []string) (bool, error) {
	path := filepath.Join(s.cfg.PostfixDir, "master.cf")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	const begin = "# BEGIN ALT8MAIL CONTROL OUTBOUND"
	const end = "# END ALT8MAIL CONTROL OUTBOUND"
	existing := string(raw)
	start := strings.Index(existing, begin)
	finish := strings.Index(existing, end)
	if start >= 0 && finish > start {
		finish += len(end)
		existing = strings.TrimRight(existing[:start], "\n") + "\n" + strings.TrimLeft(existing[finish:], "\n")
	}
	if len(lines) > 0 {
		block := begin + "\n" + strings.Join(lines, "\n") + "\n" + end + "\n"
		existing = strings.TrimRight(existing, "\n") + "\n\n" + block
	}
	changed, err := writeManagedFile(path, existing, 0o644, 0, 0)
	return changed, err
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
