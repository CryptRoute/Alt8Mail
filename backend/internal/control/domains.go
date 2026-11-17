package control

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) registerDomainRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/domains", s.require("domains.read", s.handleDomains))
	mux.HandleFunc("POST /control/api/domains", s.require("mail.manage", s.handleDomainCreate))
	mux.HandleFunc("GET /control/api/domains/{id}", s.require("domains.read", s.handleDomainDetail))
	mux.HandleFunc("PATCH /control/api/domains/{id}", s.require("mail.manage", s.handleDomainUpdate))
	mux.HandleFunc("GET /control/api/domains/{id}/delete-preview", s.require("mail.manage", s.handleDomainDeletePreview))
	mux.HandleFunc("DELETE /control/api/domains/{id}", s.require("mail.manage", s.handleDomainDelete))
	mux.HandleFunc("POST /control/api/domains/{id}/primary", s.require("mail.manage", s.handleDomainPrimary))
	mux.HandleFunc("POST /control/api/domains/{id}/dkim", s.require("mail.manage", s.handleDomainDKIM))
	mux.HandleFunc("GET /control/api/domains/{id}/dns", s.require("domains.read", s.handleDomainDNS))
	mux.HandleFunc("POST /control/api/domains/{id}/dns/check", s.require("domains.read", s.handleDomainDNS))
	mux.HandleFunc("GET /control/api/domains/{id}/dns/export", s.require("domains.read", s.handleDomainDNSExport))
	mux.HandleFunc("GET /control/api/tenants", s.require("domains.read", s.handleTenantList))
}

func (s *Server) handleTenantList(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `SELECT id::text, name, slug, status FROM public.tenants WHERE status='active' ORDER BY name`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, slug, status string
		if rows.Scan(&id, &name, &slug, &status) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "slug": slug, "status": status})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": items})
}

func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := s.db.Query(r.Context(), `
		SELECT d.id::text, d.domain::text, d.is_primary, d.status, d.dkim_selector,
		       COALESCE(d.dkim_record,''), d.created_at, t.name,
		       (SELECT count(*) FROM public.mailboxes m WHERE m.domain=d.domain AND m.active),
		       (SELECT count(*) FROM public.users u WHERE u.email LIKE '%@'||d.domain::text AND u.deleted_at IS NULL),
		       (SELECT count(*) FROM public.user_aliases a WHERE a.email LIKE '%@'||d.domain::text),
		       (SELECT count(*) FROM control.mail_aliases ca WHERE ca.domain=d.domain AND ca.active),
		       COALESCE(cs.caps_enabled,false), COALESCE(cs.cap_hour,0), COALESCE(cs.cap_day,0),
		       COALESCE(cs.cap_month,0), COALESCE(cs.burst,0), COALESCE(cs.user_daily_cap,200),
		       COALESCE(d.dmarc_policy,'none')
		FROM public.mail_domains d
		JOIN public.tenants t ON t.id=d.tenant_id
		LEFT JOIN control.domain_settings cs ON cs.domain_id=d.id
		WHERE ($1='' OR d.domain::text ILIKE '%'||$1||'%')
		ORDER BY d.is_primary DESC, d.domain
	`, query)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, domain, status, selector, record, tenantName, dmarcPolicy string
		var primary, capsEnabled bool
		var createdAt time.Time
		var mailboxCount, userCount, aliasCount, controlAliases int
		var capHour, capDay, capMonth, burst, userDailyCap int
		if rows.Scan(&id, &domain, &primary, &status, &selector, &record, &createdAt, &tenantName,
			&mailboxCount, &userCount, &aliasCount, &controlAliases,
			&capsEnabled, &capHour, &capDay, &capMonth, &burst, &userDailyCap, &dmarcPolicy) == nil {
			items = append(items, map[string]any{
				"id": id, "domain": domain, "primary": primary, "status": status,
				"dkimSelector": selector, "dkimReady": record != "", "createdAt": createdAt.UTC().Format(time.RFC3339),
				"tenant": tenantName, "mailboxes": mailboxCount, "users": userCount,
				"aliases": aliasCount + controlAliases, "capsEnabled": capsEnabled,
				"capHour": capHour, "capDay": capDay, "capMonth": capMonth, "burst": burst,
				"userDailyCap": userDailyCap,
				"dmarcPolicy":  dmarcPolicy,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": items})
}

func (s *Server) handleDomainCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Domain     string `json:"domain"`
		TenantID   string `json:"tenantId"`
		IsPrimary  bool   `json:"isPrimary"`
		Selector   string `json:"selector"`
		CreatePost bool   `json:"createPostmaster"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	domain := normalizeDomain(body.Domain)
	if !validDomain(domain) {
		writeError(w, http.StatusBadRequest, "DOMAIN", "Enter a valid domain name")
		return
	}
	if body.TenantID == "" {
		_ = s.db.QueryRow(r.Context(), `SELECT id::text FROM public.tenants WHERE status='active' ORDER BY created_at LIMIT 1`).Scan(&body.TenantID)
	}
	if !validUUID(body.TenantID) {
		writeError(w, http.StatusBadRequest, "TENANT", "Choose a tenant for this domain")
		return
	}
	selector := normalizeSelector(body.Selector)
	if !validSelector(selector) {
		writeError(w, http.StatusBadRequest, "SELECTOR", "DKIM selector is invalid")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var isPrimary bool
	_ = tx.QueryRow(r.Context(), `SELECT NOT EXISTS (SELECT 1 FROM public.mail_domains WHERE is_primary)`).Scan(&isPrimary)
	if body.IsPrimary {
		isPrimary = true
	}
	if isPrimary {
		_, _ = tx.Exec(r.Context(), `UPDATE public.mail_domains SET is_primary=false WHERE is_primary`)
	}
	var id string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO public.mail_domains (tenant_id, domain, is_primary, dkim_selector, status,
			verification_token, dmarc_policy)
		VALUES ($1,$2,$3,$4,'manual',encode(gen_random_bytes(18),'hex'),'none')
		RETURNING id::text
	`, body.TenantID, domain, isPrimary, selector).Scan(&id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			writeError(w, http.StatusConflict, "DOMAIN", "That domain already exists")
			return
		}
		writeFromError(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO control.domain_settings (domain_id) VALUES ($1) ON CONFLICT (domain_id) DO NOTHING`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	warnings := []string{}
	if body.CreatePost {
		if err := s.createPostmaster(r.Context(), body.TenantID, domain); err != nil {
			warnings = append(warnings, "postmaster mailbox: "+err.Error())
		}
	}
	if record, err := s.generateDKIMKey(r.Context(), id, domain, selector, false); err != nil {
		warnings = append(warnings, "DKIM: "+err.Error())
	} else {
		_ = record
	}
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "mail.domain_created", domain, "Created a mail domain",
		map[string]any{"tenant": body.TenantID, "primary": isPrimary, "warnings": warnings})
	s.notifyEvent(r.Context(), "domain.created", map[string]any{"domain": domain})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "domain": domain, "warnings": warnings})
}

func (s *Server) handleDomainDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var domain, tenantID, status, selector, record, dkimError, dmarcPolicy, notes string
	var primary bool
	var createdAt time.Time
	var capEnabled bool
	var capHour, capDay, capMonth, burst, userDailyCap int
	err := s.db.QueryRow(r.Context(), `
		SELECT d.id::text, d.domain::text, d.tenant_id::text, d.is_primary, d.status,
		       COALESCE(d.dkim_selector,''), COALESCE(d.dkim_record,''), COALESCE(d.dkim_key_error,''),
		       COALESCE(d.dmarc_policy,'none'), d.created_at,
		       COALESCE(cs.caps_enabled,false), COALESCE(cs.cap_hour,0), COALESCE(cs.cap_day,0),
		       COALESCE(cs.cap_month,0), COALESCE(cs.burst,0), COALESCE(cs.user_daily_cap,200), COALESCE(cs.notes,'')
		FROM public.mail_domains d
		LEFT JOIN control.domain_settings cs ON cs.domain_id=d.id
		WHERE d.id=$1
	`, id).Scan(&id, &domain, &tenantID, &primary, &status, &selector, &record, &dkimError,
		&dmarcPolicy, &createdAt, &capEnabled, &capHour, &capDay, &capMonth, &burst, &userDailyCap, &notes)
	if err != nil {
		writeFromError(w, err)
		return
	}
	mailboxes := []map[string]any{}
	rows, _ := s.db.Query(r.Context(), `
		SELECT m.id::text, m.email::text, m.local_part::text, m.active, m.provision_status, mb.used_bytes
		FROM public.mailboxes m
		JOIN public.users u ON u.id=m.user_id
		LEFT JOIN LATERAL (
		  SELECT GREATEST(u.used_bytes, COALESCE(SUM(mm.size_bytes)::bigint, 0)) AS used_bytes
		  FROM public.mail_messages mm WHERE mm.user_id=u.id
		) mb ON true
		WHERE m.domain=$1 ORDER BY m.local_part
	`, domain)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var mid, email, local, provision string
			var active bool
			var used int64
			if rows.Scan(&mid, &email, &local, &active, &provision, &used) == nil {
				mailboxes = append(mailboxes, map[string]any{"id": mid, "email": email, "localPart": local,
					"active": active, "provisionStatus": provision, "usedBytes": used})
			}
		}
	}
	usage := s.domainUsage(r.Context(), domain)
	var userAliases, controlAliases, groups int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.user_aliases WHERE email::text ILIKE '%@'||$1`, domain).Scan(&userAliases)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM control.mail_aliases WHERE domain=$1`, domain).Scan(&controlAliases)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.groups WHERE email::text ILIKE '%@'||$1`, domain).Scan(&groups)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "domain": domain, "tenantId": tenantID, "primary": primary, "status": status,
		"dkimSelector": selector, "dkimRecord": record, "dkimError": dkimError,
		"dmarcPolicy": dmarcPolicy, "createdAt": createdAt.UTC().Format(time.RFC3339),
		"caps":  map[string]any{"enabled": capEnabled, "hour": capHour, "day": capDay, "month": capMonth, "burst": burst, "userDailyCap": userDailyCap},
		"usage": usage, "notes": notes, "mailboxes": mailboxes,
		"aliasCounts": map[string]any{"identities": userAliases, "forwarders": controlAliases, "groups": groups},
	})
}

func (s *Server) domainUsage(ctx context.Context, domain string) map[string]any {
	var hour, day, month int64
	_ = s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE event_at > now()-interval '1 hour'),
		       count(*) FILTER (WHERE event_at > now()-interval '24 hours'),
		       count(*) FILTER (WHERE event_at > now()-interval '30 days')
		FROM control.mail_events
		WHERE direction='outbound' AND status='sent' AND split_part(sender::text,'@',2)=lower($1)
	`, domain).Scan(&hour, &day, &month)
	return map[string]any{"hour": hour, "day": day, "month": month}
}

func (s *Server) handleDomainUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Domain       *string `json:"domain"`
		IsPrimary    *bool   `json:"isPrimary"`
		DMARCPolicy  *string `json:"dmarcPolicy"`
		CapsEnabled  *bool   `json:"capsEnabled"`
		CapHour      *int    `json:"capHour"`
		CapDay       *int    `json:"capDay"`
		CapMonth     *int    `json:"capMonth"`
		Burst        *int    `json:"burst"`
		UserDailyCap *int    `json:"userDailyCap"`
		Notes        *string `json:"notes"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var oldDomain string
	if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id=$1`, id).Scan(&oldDomain); err != nil {
		writeFromError(w, err)
		return
	}
	if body.Domain != nil {
		newDomain := normalizeDomain(*body.Domain)
		if !validDomain(newDomain) {
			writeError(w, http.StatusBadRequest, "DOMAIN", "Enter a valid domain name")
			return
		}
		if newDomain != oldDomain {
			sum, err := s.renameDomain(r.Context(), id, oldDomain, newDomain)
			if err != nil {
				writeFromError(w, err)
				return
			}
			s.actorAudit(r, sess, "mail.domain_renamed", oldDomain+" → "+newDomain, "Renamed a mail domain and every mailbox address", sum)
			oldDomain = newDomain
		}
	}
	if body.DMARCPolicy != nil {
		policy := strings.ToLower(strings.TrimSpace(*body.DMARCPolicy))
		if policy != "none" && policy != "quarantine" && policy != "reject" {
			writeError(w, http.StatusBadRequest, "DMARC", "DMARC policy must be none, quarantine or reject")
			return
		}
		_, _ = s.db.Exec(r.Context(), `UPDATE public.mail_domains SET dmarc_policy=$2 WHERE id=$1`, id, policy)
	}
	if body.IsPrimary != nil && *body.IsPrimary {
		_, _ = s.db.Exec(r.Context(), `UPDATE public.mail_domains SET is_primary=false WHERE is_primary`)
		_, _ = s.db.Exec(r.Context(), `UPDATE public.mail_domains SET is_primary=true WHERE id=$1`, id)
	}
	if body.CapsEnabled != nil || body.CapHour != nil || body.CapDay != nil || body.CapMonth != nil || body.Burst != nil || body.UserDailyCap != nil || body.Notes != nil {
		capHour, capDay, capMonth, burst, userDailyCap := 0, 0, 0, 0, 200
		var capsEnabled bool
		var notes string
		_ = s.db.QueryRow(r.Context(), `SELECT caps_enabled, cap_hour, cap_day, cap_month, burst, COALESCE(user_daily_cap,200), notes FROM control.domain_settings WHERE domain_id=$1`, id).
			Scan(&capsEnabled, &capHour, &capDay, &capMonth, &burst, &userDailyCap, &notes)
		if body.CapsEnabled != nil {
			capsEnabled = *body.CapsEnabled
		}
		if body.CapHour != nil {
			capHour = *body.CapHour
		}
		if body.CapDay != nil {
			capDay = *body.CapDay
		}
		if body.CapMonth != nil {
			capMonth = *body.CapMonth
		}
		if body.Burst != nil {
			burst = *body.Burst
		}
		if body.UserDailyCap != nil {
			userDailyCap = *body.UserDailyCap
		}
		if body.Notes != nil {
			notes = truncateText(strings.TrimSpace(*body.Notes), 2000)
		}
		if capHour < 0 || capDay < 0 || capMonth < 0 || burst < 0 || userDailyCap < 0 {
			writeError(w, http.StatusBadRequest, "CAPS", "Outgoing caps cannot be negative")
			return
		}
		_, err := s.db.Exec(r.Context(), `
			INSERT INTO control.domain_settings (domain_id, caps_enabled, cap_hour, cap_day, cap_month, burst, user_daily_cap, notes, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
			ON CONFLICT (domain_id) DO UPDATE SET caps_enabled=EXCLUDED.caps_enabled, cap_hour=EXCLUDED.cap_hour,
			  cap_day=EXCLUDED.cap_day, cap_month=EXCLUDED.cap_month, burst=EXCLUDED.burst,
			  user_daily_cap=EXCLUDED.user_daily_cap, notes=EXCLUDED.notes, updated_at=now()
		`, id, capsEnabled, capHour, capDay, capMonth, burst, userDailyCap, notes)
		if err != nil {
			writeFromError(w, err)
			return
		}
	}
	s.actorAudit(r, sess, "mail.domain_updated", oldDomain, "Updated domain settings", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "domain": oldDomain})
}

type domainMailboxHome struct {
	Local      string
	OldHome    string
	NewHome    string
	VolumeRoot string
}

func (s *Server) domainMailboxHomes(ctx context.Context, domain string) ([]domainMailboxHome, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.local_part::text, COALESCE(m.mail_home, ''), COALESCE(v.mount_path, '')
		FROM public.mailboxes m
		LEFT JOIN control.storage_volumes v ON v.id = m.storage_volume_id
		WHERE m.domain = $1
		ORDER BY m.local_part
	`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	homes := []domainMailboxHome{}
	for rows.Next() {
		var local, home, volumeRoot string
		if err := rows.Scan(&local, &home, &volumeRoot); err != nil {
			return nil, err
		}
		if strings.TrimSpace(volumeRoot) == "" {
			volumeRoot = s.cfg.VmailRoot
		}
		homes = append(homes, domainMailboxHome{
			Local: local, OldHome: home, VolumeRoot: filepath.Clean(volumeRoot),
		})
	}
	return homes, rows.Err()
}

func (s *Server) renameDomainHomes(homes []domainMailboxHome, oldDomain, newDomain string) ([]domainMailboxHome, error) {
	type move struct {
		index    int
		from, to string
	}
	moves := []move{}
	for i := range homes {
		if strings.TrimSpace(homes[i].OldHome) == "" {
			homes[i].OldHome = filepath.Join(homes[i].VolumeRoot, oldDomain, homes[i].Local)
		}
		homes[i].OldHome = filepath.Clean(homes[i].OldHome)
		homes[i].NewHome = filepath.Join(homes[i].VolumeRoot, newDomain, homes[i].Local)
		if !pathWithin(homes[i].VolumeRoot, homes[i].OldHome) || !pathWithin(homes[i].VolumeRoot, homes[i].NewHome) {
			return nil, fail(500, "MAILDIR", "A mailbox home is outside its registered volume")
		}
		info, err := os.Lstat(homes[i].OldHome)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fail(500, "MAILDIR", "Could not inspect the mailbox home: "+err.Error())
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fail(409, "MAILDIR", "A mailbox home is not a real directory")
		}
		if _, err := os.Lstat(homes[i].NewHome); err == nil {
			return nil, fail(409, "MAILDIR", "The target mailbox directory already exists: "+homes[i].NewHome)
		}
		moves = append(moves, move{index: i, from: homes[i].OldHome, to: homes[i].NewHome})
	}
	uid, gid, err := mailOwnerLookup()
	if err != nil {
		return nil, fail(500, "MAILDIR", "Could not resolve the vmail account: "+err.Error())
	}
	created := map[string]bool{}
	done := []move{}
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			_ = os.Rename(done[i].to, done[i].from)
		}
	}
	for _, item := range moves {
		parent := filepath.Dir(item.to)
		if !created[parent] {
			if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
				rollback()
				return nil, fail(409, "MAILDIR", "The target domain path contains a symlink")
			}
			if err := os.MkdirAll(parent, 0o750); err != nil {
				rollback()
				return nil, fail(500, "MAILDIR", "Could not create the target domain directory: "+err.Error())
			}
			if uid >= 0 {
				if err := os.Chown(parent, uid, gid); err != nil {
					rollback()
					return nil, fail(500, "MAILDIR", "Could not own the target domain directory: "+err.Error())
				}
			}
			if err := os.Chmod(parent, 0o750); err != nil {
				rollback()
				return nil, fail(500, "MAILDIR", "Could not set the target domain mode: "+err.Error())
			}
			created[parent] = true
		}
		if err := os.Rename(item.from, item.to); err != nil {
			rollback()
			return nil, fail(500, "MAILDIR", "Could not rename a mailbox home: "+err.Error())
		}
		done = append(done, item)
	}
	movedHomes := make([]domainMailboxHome, 0, len(done))
	for _, item := range done {
		movedHomes = append(movedHomes, homes[item.index])
	}
	return movedHomes, nil
}

func (s *Server) rollbackDomainHomes(homes []domainMailboxHome) {
	for i := len(homes) - 1; i >= 0; i-- {
		if homes[i].OldHome == "" || homes[i].NewHome == "" {
			continue
		}
		if _, err := os.Lstat(homes[i].NewHome); err == nil {
			_ = os.Rename(homes[i].NewHome, homes[i].OldHome)
		}
	}
}

func (s *Server) renameDomain(ctx context.Context, id, oldDomain, newDomain string) (map[string]any, error) {
	var existing int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains WHERE domain=$1 AND id<>$2`, newDomain, id).Scan(&existing)
	if existing > 0 {
		return nil, fail(409, "DOMAIN", "The new domain already exists")
	}
	homes, err := s.domainMailboxHomes(ctx, oldDomain)
	if err != nil {
		return nil, err
	}
	moved, err := s.renameDomainHomes(homes, oldDomain, newDomain)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.rollbackDomainHomes(moved)
		return nil, err
	}
	defer tx.Rollback(ctx)
	rollbackDir := func() {
		s.rollbackDomainHomes(moved)
	}
	// Update child tables first so no unique conflict remains on the domain row.
	if _, err := tx.Exec(ctx, `
		UPDATE public.mailboxes SET
		  domain=$2::text,
		  email = lower(local_part::text) || '@' || $2::text,
		  mail_home = CASE WHEN mail_home IS NULL THEN NULL
		                   ELSE replace(mail_home, '/' || $1::text || '/', '/' || $2::text || '/') END,
		  maildir_path = replace(maildir_path, '/' || $1::text || '/', '/' || $2::text || '/'),
		  provision_status='ready', provisioned_at=now()
		WHERE domain=$1::text
	`, oldDomain, newDomain); err != nil {
		rollbackDir()
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE public.users SET email = split_part(email::text,'@',1) || '@' || $2::text, updated_at=now()
		WHERE email::text ILIKE '%@' || $1::text
	`, oldDomain, newDomain); err != nil {
		rollbackDir()
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE public.user_aliases SET email = split_part(email::text,'@',1) || '@' || $2::text
		WHERE email::text ILIKE '%@' || $1::text
	`, oldDomain, newDomain); err != nil {
		rollbackDir()
		return nil, err
	}
	_, _ = tx.Exec(ctx, `
		UPDATE public.groups SET email = split_part(email::text,'@',1) || '@' || $2::text, updated_at=now()
		WHERE email::text ILIKE '%@' || $1::text
	`, oldDomain, newDomain)
	_, _ = tx.Exec(ctx, `
		UPDATE public.mail_identities SET email = split_part(email::text,'@',1) || '@' || $2::text
		WHERE email::text ILIKE '%@' || $1::text
	`, oldDomain, newDomain)
	if _, err := tx.Exec(ctx, `
		UPDATE control.mail_aliases SET
		  domain=$2::text,
		  address = CASE WHEN address::text ILIKE '%@'||$1::text THEN split_part(address::text,'@',1) || '@' || $2::text ELSE address END,
		  targets = ARRAY(SELECT CASE WHEN lower(t) LIKE '%@'||$1::text THEN split_part(lower(t),'@',1) || '@' || $2::text ELSE t END FROM unnest(targets) AS t),
		  updated_at=now()
		WHERE domain=$1::text OR address::text ILIKE '%@'||$1::text
	`, oldDomain, newDomain); err != nil {
		rollbackDir()
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE public.mail_domains SET domain=$2::text WHERE id=$1`, id, newDomain); err != nil {
		rollbackDir()
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		rollbackDir()
		return nil, err
	}
	_, _ = s.generateDKIMKey(ctx, id, newDomain, "a8m", false)
	_, _ = s.writeSelectorMap(ctx)
	_ = reloadPostfix(ctx)
	var mailboxes, aliases int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE domain=$1`, newDomain).Scan(&mailboxes)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_aliases WHERE domain=$1`, newDomain).Scan(&aliases)
	return map[string]any{"mailboxes": mailboxes, "aliases": aliases, "maildirRenamed": len(moved) > 0, "homesMoved": len(moved)}, nil
}

func (s *Server) handleDomainPrimary(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `UPDATE public.mail_domains SET is_primary=false WHERE is_primary`); err != nil {
		writeFromError(w, err)
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE public.mail_domains SET is_primary=true WHERE id=$1`, id)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "DOMAIN", "Domain not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "mail.primary_domain", id, "Selected the primary mail domain", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDomainDKIM(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Selector string `json:"selector"`
	}
	_ = decodeJSON(w, r, &body, 16<<10)
	var domain, selector string
	if err := s.db.QueryRow(r.Context(), `SELECT domain::text, COALESCE(dkim_selector,'a8m') FROM public.mail_domains WHERE id=$1`, id).Scan(&domain, &selector); err != nil {
		writeFromError(w, err)
		return
	}
	if strings.TrimSpace(body.Selector) != "" {
		selector = normalizeSelector(body.Selector)
	}
	record, err := s.generateDKIMKey(r.Context(), id, domain, selector, true)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "mail.dkim_regenerated", domain, "Regenerated DKIM signing key", map[string]any{"selector": selector})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "selector": selector, "record": record})
}

type domainArchiveMove struct {
	From, To string
	Orphan   bool
}

func (s *Server) archiveDomainHomes(ctx context.Context, domain string) (string, []domainArchiveMove, error) {
	homes, err := s.domainMailboxHomes(ctx, domain)
	if err != nil {
		return "", nil, err
	}
	backupDir := filepath.Join(s.cfg.BackupDir, "deleted-domains")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", nil, err
	}
	archive, err := os.MkdirTemp(backupDir, domain+"-")
	if err != nil {
		return "", nil, err
	}
	moves := []domainArchiveMove{}
	for _, home := range homes {
		source := strings.TrimSpace(home.OldHome)
		if source == "" {
			source = filepath.Join(home.VolumeRoot, domain, home.Local)
		}
		source = filepath.Clean(source)
		if !pathWithin(home.VolumeRoot, source) || sameStoragePath(home.VolumeRoot, source) {
			_ = os.RemoveAll(archive)
			s.rollbackArchiveMoves(moves)
			return "", nil, fmt.Errorf("mailbox home %s is outside its registered volume", source)
		}
		if !pathExists(source) {
			continue
		}
		volumeName := "legacy"
		if !sameStoragePath(home.VolumeRoot, s.cfg.VmailRoot) {
			volumeName = storageNameFromPath(home.VolumeRoot)
		}
		destination := filepath.Join(archive, volumeName, home.Local)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			s.rollbackArchiveMoves(moves)
			_ = os.RemoveAll(archive)
			return "", nil, err
		}
		if err := movePath(ctx, source, destination); err != nil {
			s.rollbackArchiveMoves(moves)
			_ = os.RemoveAll(archive)
			return "", nil, err
		}
		moves = append(moves, domainArchiveMove{From: source, To: destination})
	}
	return archive, moves, nil
}

func (s *Server) rollbackArchiveMoves(moves []domainArchiveMove) {
	for i := len(moves) - 1; i >= 0; i-- {
		if pathExists(moves[i].To) {
			_ = movePath(context.Background(), moves[i].To, moves[i].From)
		}
	}
}

func (s *Server) handleDomainDeletePreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var domain string
	if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id=$1`, id).Scan(&domain); err != nil {
		writeFromError(w, err)
		return
	}
	var mailboxes, users, aliases, controlAliases, groups, messages int64
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.mailboxes WHERE domain=$1`, domain).Scan(&mailboxes)
	_ = s.db.QueryRow(r.Context(), `SELECT count(DISTINCT user_id) FROM public.mailboxes WHERE domain=$1`, domain).Scan(&users)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.user_aliases WHERE email::text ILIKE '%@'||$1`, domain).Scan(&aliases)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM control.mail_aliases WHERE domain=$1`, domain).Scan(&controlAliases)
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.groups WHERE email::text ILIKE '%@'||$1`, domain).Scan(&groups)
	_ = s.db.QueryRow(r.Context(), `
		SELECT count(*) FROM public.mail_messages WHERE user_id IN (
		  SELECT DISTINCT user_id FROM public.mailboxes WHERE domain=$1
		)
	`, domain).Scan(&messages)
	path := filepath.Join(s.cfg.VmailRoot, domain)
	maildirBytes := int64(0)
	homes, _ := s.domainMailboxHomes(r.Context(), domain)
	roots := map[string]bool{filepath.Clean(s.cfg.VmailRoot): true}
	for _, home := range homes {
		roots[home.VolumeRoot] = true
	}
	for root := range roots {
		if result, err := runCommand(r.Context(), 15*time.Second, "du", "-sb", filepath.Join(root, domain)); err == nil {
			fields := strings.Fields(result.Stdout)
			if len(fields) > 0 {
				var bytes int64
				fmt.Sscan(fields[0], &bytes)
				maildirBytes += bytes
			}
		}
	}
	keys, _ := filepath.Glob(filepath.Join(s.cfg.DKIMDir, domain+".*.key"))
	writeJSON(w, http.StatusOK, map[string]any{
		"domain": domain, "mailboxes": mailboxes, "users": users, "aliases": aliases,
		"controlAliases": controlAliases, "groups": groups, "messages": messages,
		"maildirPath": path, "maildirBytes": maildirBytes, "dkimKeys": keys,
		"confirmToken": s.signCSRF("delete-domain:" + domain),
		"warning":      "Deleting the domain archives its Maildir, removes its users, mailboxes, aliases, groups, DKIM material and message copies, then removes the domain from Postfix.",
	})
}

func (s *Server) handleDomainDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		ConfirmDomain string `json:"confirmDomain"`
		ConfirmToken  string `json:"confirmToken"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	var domain string
	if err := s.db.QueryRow(r.Context(), `SELECT domain::text FROM public.mail_domains WHERE id=$1`, id).Scan(&domain); err != nil {
		writeFromError(w, err)
		return
	}
	if normalizeDomain(body.ConfirmDomain) != domain {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type the exact domain name to confirm deletion")
		return
	}
	expected := s.signCSRF("delete-domain:" + domain)
	if !constantTimeEqual(expected, body.ConfirmToken) {
		writeError(w, http.StatusForbidden, "CONFIRM", "Refresh the delete preview and try again")
		return
	}
	archive, archivedMoves, err := s.archiveDomainHomes(r.Context(), domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ARCHIVE", "Could not archive the domain mail: "+err.Error())
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.rollbackArchiveMoves(archivedMoves)
		writeFromError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		DELETE FROM public.users WHERE email::text ILIKE '%@'||$1 AND deleted_at IS NULL
	`, domain); err != nil {
		s.rollbackArchiveMoves(archivedMoves)
		writeFromError(w, err)
		return
	}
	_, _ = tx.Exec(r.Context(), `DELETE FROM public.groups WHERE email::text ILIKE '%@'||$1`, domain)
	_, _ = tx.Exec(r.Context(), `DELETE FROM control.mail_aliases WHERE domain=$1`, domain)
	tag, err := tx.Exec(r.Context(), `DELETE FROM public.mail_domains WHERE id=$1`, id)
	if err != nil || tag.RowsAffected() == 0 {
		s.rollbackArchiveMoves(archivedMoves)
		writeError(w, http.StatusNotFound, "DOMAIN", "Domain not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.rollbackArchiveMoves(archivedMoves)
		writeFromError(w, err)
		return
	}
	// The archive move leaves empty domain directories behind on each volume.
	for _, move := range archivedMoves {
		if !move.Orphan {
			_ = os.Remove(filepath.Dir(move.From))
		}
	}
	keys, _ := filepath.Glob(filepath.Join(s.cfg.DKIMDir, domain+".*.key"))
	if len(keys) > 0 {
		keyArchive := filepath.Join(s.cfg.BackupDir, "deleted-domains")
		_ = os.MkdirAll(keyArchive, 0o700)
		for _, key := range keys {
			_ = movePath(r.Context(), key, filepath.Join(keyArchive, filepath.Base(key)+"."+time.Now().UTC().Format("20060102150405")))
		}
	}
	_, _ = s.writeSelectorMap(r.Context())
	_, _ = runCommand(r.Context(), 20*time.Second, "systemctl", "reload", "rspamd")
	_ = reloadPostfix(r.Context())
	s.actorAudit(r, sess, "mail.domain_deleted", domain, "Deleted a mail domain", map[string]any{"archive": archive})
	s.notifyEvent(r.Context(), "domain.deleted", map[string]any{"domain": domain})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "domain": domain, "archive": archive})
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *Server) domainDNSPlan(ctx context.Context, id string) (map[string]any, error) {
	var domain, selector, record string
	var dmarcPolicy string
	err := s.db.QueryRow(ctx, `
		SELECT domain::text, COALESCE(dkim_selector,'a8m'), COALESCE(dkim_record,''),
		       COALESCE(dmarc_policy,'none')
		FROM public.mail_domains WHERE id=$1
	`, id).Scan(&domain, &selector, &record, &dmarcPolicy)
	if errorsIsNoRows(err) {
		return nil, fail(404, "DOMAIN", "Domain not found")
	}
	if err != nil {
		return nil, err
	}
	mailHost := s.mailHostname(ctx)
	ipv4 := s.settingString(ctx, "server.public_ipv4", "")
	ipv6 := s.settingString(ctx, "server.public_ipv6", "")
	resolver := s.blocklistResolver()
	if ipv4 == "" {
		if addresses, err := resolver.LookupHost(ctx, mailHost); err == nil {
			for _, address := range addresses {
				ip := net.ParseIP(address)
				if ip != nil && ip.To4() != nil {
					ipv4 = address
					break
				}
			}
		}
	}
	if ipv6 == "" {
		if addresses, err := resolver.LookupHost(ctx, mailHost); err == nil {
			for _, address := range addresses {
				ip := net.ParseIP(address)
				if ip != nil && ip.To4() == nil {
					ipv6 = address
					break
				}
			}
		}
	}
	records := []map[string]any{}
	add := func(recordType, name, value, purpose, actual, status string) {
		if actual == "" {
			actual = "No record returned"
		}
		records = append(records, map[string]any{
			"type": recordType, "name": name, "value": value, "purpose": purpose,
			"actual": actual, "status": status,
		})
	}
	spf := "v=spf1 mx a:" + mailHost + " -all"
	if ipv4 != "" {
		spf = "v=spf1 mx ip4:" + ipv4
		if ipv6 != "" {
			spf += " ip6:" + ipv6
		}
		spf += " -all"
	}
	dkimValue := record
	if dkimValue == "" {
		dkimValue = "v=DKIM1; k=rsa; p=<generate DKIM first>"
	}
	dmarcValue := "v=DMARC1; p=" + dmarcPolicy + "; rua=mailto:postmaster@" + domain + "; ruf=mailto:postmaster@" + domain + "; adkim=s; aspf=s; pct=100"
	tlsRPT := "v=TLSRPTv1; rua=mailto:postmaster@" + domain

	mxActual := ""
	mxStatus := "fail"
	if entries, err := resolver.LookupMX(ctx, domain); err == nil {
		names := []string{}
		for _, entry := range entries {
			names = append(names, fmt.Sprintf("%d %s", entry.Pref, strings.TrimSuffix(entry.Host, ".")))
		}
		mxActual = strings.Join(names, ", ")
		for _, entry := range entries {
			if strings.EqualFold(strings.TrimSuffix(entry.Host, "."), mailHost) {
				mxStatus = "pass"
			}
		}
	}
	add("MX", "@", "10 "+mailHost, "Receives mail for this domain", mxActual, mxStatus)
	aActual := ""
	if addresses, err := resolver.LookupHost(ctx, mailHost); err == nil {
		aActual = strings.Join(addresses, ", ")
	}
	add("A", mailHost, firstNonEmpty(ipv4, "<server public IPv4>"), "Webmail and mail host", aActual, statusIf(ipv4 != "" && strings.Contains(aActual, ipv4), "pass", "warn"))
	add("AAAA", mailHost, firstNonEmpty(ipv6, "Optional if IPv6 is not configured"), "Mail host IPv6", aActual, statusIf(ipv6 != "" && strings.Contains(aActual, ipv6), "pass", "warn"))
	spfActual := lookupTXTJoined(ctx, resolver, domain)
	spfStatus := "fail"
	if strings.HasPrefix(strings.ToLower(spfActual), "v=spf1") && (strings.Contains(spfActual, "a:"+mailHost) || strings.Contains(spfActual, "ip4:"+ipv4) || strings.Contains(spfActual, "mx")) {
		spfStatus = "pass"
	}
	add("TXT", "@", spf, "Sender Policy Framework", spfActual, spfStatus)
	dkimActual := lookupTXTJoined(ctx, resolver, selector+"._domainkey."+domain)
	dkimStatus := statusIf(record != "" && strings.Contains(dkimActual, strings.TrimPrefix(record, "v=DKIM1; k=rsa; p=")), "pass", "fail")
	add("TXT", selector+"._domainkey", dkimValue, "DKIM signing key", dkimActual, dkimStatus)
	dmarcActual := lookupTXTJoined(ctx, resolver, "_dmarc."+domain)
	dmarcStatus := "fail"
	if strings.HasPrefix(strings.ToUpper(dmarcActual), "V=DMARC1") && strings.Contains(dmarcActual, "rua=mailto:postmaster@"+domain) {
		dmarcStatus = "pass"
	}
	add("TXT", "_dmarc", dmarcValue, "DMARC policy and reporting", dmarcActual, dmarcStatus)
	tlsActual := lookupTXTJoined(ctx, resolver, "_smtp._tls."+domain)
	add("TXT", "_smtp._tls", tlsRPT, "TLS reporting", tlsActual, statusIf(strings.Contains(strings.ToUpper(tlsActual), "TLSRPTV1"), "pass", "fail"))
	ptrActual := ""
	if ipv4 != "" {
		if names, err := resolver.LookupAddr(ctx, ipv4); err == nil {
			ptrActual = strings.Join(names, ", ")
		}
	}
	ptrStatus := statusIf(strings.Contains(strings.ToLower(ptrActual), strings.ToLower(mailHost)), "pass", "provider")
	records = append(records, map[string]any{
		"type": "PTR", "name": ipv4, "value": mailHost, "purpose": "Reverse DNS",
		"actual": ptrActual, "status": ptrStatus, "providerAction": true,
		"note": "Reverse DNS is controlled by the VPS/cloud network provider, not by this mail server. Open the provider's reverse-DNS panel for the public IP and set it to " + mailHost + "; the panel can only verify the result.",
	})
	return map[string]any{
		"domain": domain, "mailHost": mailHost, "ipv4": ipv4, "ipv6": ipv6,
		"selector": selector, "plan": records, "checkedAt": nowRFC3339(),
		"export": renderDNSPlan(domain, records),
	}, nil
}

func lookupTXTJoined(ctx context.Context, resolver *net.Resolver, name string) string {
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	chunks, err := resolver.LookupTXT(lookupCtx, name)
	if err != nil {
		return ""
	}
	return strings.Join(chunks, "")
}

func statusIf(condition bool, yes, no string) string {
	if condition {
		return yes
	}
	return no
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func renderDNSPlan(domain string, records []map[string]any) string {
	var b strings.Builder
	b.WriteString("# Alt8Mail DNS plan for " + domain + "\n")
	b.WriteString("# Apply these records at the DNS provider. PTR is provider-side.\n\n")
	for _, record := range records {
		recordType := fmt.Sprint(record["type"])
		name := fmt.Sprint(record["name"])
		value := fmt.Sprint(record["value"])
		if recordType == "MX" {
			value = "10 " + strings.TrimPrefix(value, "10 ")
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", name, recordType, value)
	}
	return b.String()
}

func (s *Server) handleDomainDNS(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	plan, err := s.domainDNSPlan(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleDomainDNSExport(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	plan, err := s.domainDNSPlan(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+fmt.Sprint(plan["domain"])+"-dns.txt")
	_, _ = w.Write([]byte(fmt.Sprint(plan["export"])))
}

func errorsIsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
