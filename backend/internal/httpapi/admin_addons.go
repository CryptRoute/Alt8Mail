package httpapi

import (
	"net/http"
	"strings"
	"time"
)

// adminListAddons gives the customer administration console a tenant-scoped
// view of the curated catalog. Credentials are configured in Webmail; this
// surface controls workspace availability, install policy and approvals.
func (s *Server) adminListAddons(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	tenantID := sess.ActiveTenantID
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT a.id, a.name, a.vendor, a.category, a.description, a.tagline, a.logo_asset,
		       a.connection_scope, a.auth_type, a.requires_tenant_setup,
		       a.support_url, a.docs_url, a.website_url, a.privacy_url, a.connector_version,
		       a.enabled,
		       COALESCE(t.enabled, a.connection_scope = 'user') AS tenant_enabled,
		       COALESCE(t.install_policy, 'self_service') AS install_policy,
		       COALESCE((SELECT c.account_label FROM addon_connections c
		                 WHERE c.tenant_id = $1 AND c.addon_id = a.id
		                   AND c.connection_scope = 'tenant' AND c.status = 'active'
		                 ORDER BY c.updated_at DESC LIMIT 1), '') AS connection_label,
		       (SELECT count(*) FROM addon_installs i
		         WHERE i.tenant_id = $1 AND i.addon_id = a.id AND i.status = 'active') AS install_count,
		       (SELECT count(*) FROM addon_requests rq
		         WHERE rq.tenant_id = $1 AND rq.addon_id = a.id AND rq.status = 'pending') AS request_count
		FROM addon_system_catalog a
		LEFT JOIN tenant_addons t ON t.tenant_id = $1 AND t.addon_id = a.id
		ORDER BY a.sort_order, a.name
	`, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDONS", "Could not load the add-on catalog")
		return
	}
	defer rows.Close()
	addons := []map[string]any{}
	for rows.Next() {
		var id, name, vendor, category, description, tagline, logoAsset, scope, authType string
		var supportURL, docsURL, websiteURL, privacyURL, connectorVersion, policy, connectionLabel string
		var requiresSetup, systemEnabled, tenantEnabled bool
		var installCount, requestCount int
		if rows.Scan(&id, &name, &vendor, &category, &description, &tagline, &logoAsset,
			&scope, &authType, &requiresSetup, &supportURL, &docsURL, &websiteURL,
			&privacyURL, &connectorVersion, &systemEnabled, &tenantEnabled, &policy,
			&connectionLabel, &installCount, &requestCount) != nil {
			continue
		}
		if policy == "" {
			policy = "self_service"
		}
		addons = append(addons, map[string]any{
			"id": id, "name": name, "vendor": vendor, "category": category,
			"description": description, "tagline": tagline, "logo": addonLogoURL(logoAsset),
			"connectionScope": scope, "authType": authType,
			"requiresTenantSetup": requiresSetup,
			"supportUrl":          supportURL, "docsUrl": docsURL,
			"websiteUrl": websiteURL, "privacyUrl": privacyURL,
			"connectorVersion": connectorVersion,
			"systemEnabled":    systemEnabled, "tenantEnabled": tenantEnabled,
			"installPolicy": policy, "connectionLabel": connectionLabel,
			"installCount": installCount, "requestCount": requestCount,
		})
	}
	requests := []map[string]any{}
	requestRows, err := s.store.DB.Query(r.Context(), `
		SELECT r.id, r.addon_id, a.name, a.logo_asset, r.user_id::text,
		       u.name, u.email::text, r.request_type, r.note, r.created_at
		FROM addon_requests r
		JOIN addon_system_catalog a ON a.id = r.addon_id
		JOIN users u ON u.id = r.user_id
		WHERE r.tenant_id = $1 AND r.status = 'pending'
		ORDER BY r.created_at ASC
	`, tenantID)
	if err == nil {
		defer requestRows.Close()
		for requestRows.Next() {
			var id, addonID, addonName, logoAsset, userID, userName, userEmail, requestType, note string
			var createdAt time.Time
			if requestRows.Scan(&id, &addonID, &addonName, &logoAsset, &userID,
				&userName, &userEmail, &requestType, &note, &createdAt) == nil {
				requests = append(requests, map[string]any{
					"id": id, "addonId": addonID, "addonName": addonName,
					"addonLogo": addonLogoURL(logoAsset),
					"userId":    userID, "userName": userName, "userEmail": userEmail,
					"requestType": requestType, "note": note,
					"createdAt": createdAt.UTC().Format(time.RFC3339),
				})
			}
		}
	}
	webmailAddonsURL := "/#/Settings/addons"
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/"); base != "" {
		webmailAddonsURL = base + "/#/Settings/addons"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"addons": addons, "requests": requests,
		"webmailAddonsUrl": webmailAddonsURL,
	})
}

func (s *Server) adminUpdateAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "ADDON_ID", "Add-on id is required")
		return
	}
	var body struct {
		Enabled       *bool   `json:"enabled"`
		InstallPolicy *string `json:"installPolicy"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Enabled == nil && body.InstallPolicy == nil {
		writeError(w, http.StatusBadRequest, "ADDON_POLICY", "Choose a workspace policy change")
		return
	}
	var systemEnabled bool
	if err := s.store.DB.QueryRow(r.Context(), `SELECT enabled FROM addon_system_catalog WHERE id = $1`, id).Scan(&systemEnabled); err != nil {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	if !systemEnabled && body.Enabled != nil && *body.Enabled {
		writeError(w, http.StatusConflict, "ADDON_DISABLED", "A system administrator has disabled this add-on")
		return
	}
	enabled := true
	policy := "self_service"
	var storedEnabled bool
	var storedPolicy string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT enabled, install_policy FROM tenant_addons WHERE tenant_id = $1 AND addon_id = $2
	`, sess.ActiveTenantID, id).Scan(&storedEnabled, &storedPolicy); err == nil {
		enabled = storedEnabled
		if storedPolicy != "" {
			policy = storedPolicy
		}
	}
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.InstallPolicy != nil {
		policy = strings.TrimSpace(*body.InstallPolicy)
		if policy != "self_service" && policy != "approval_required" {
			writeError(w, http.StatusBadRequest, "ADDON_POLICY", "Choose self-service or approval-required installation")
			return
		}
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO tenant_addons (id, tenant_id, addon_id, enabled, install_policy, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
		ON CONFLICT (tenant_id, addon_id) DO UPDATE
		SET enabled=EXCLUDED.enabled, install_policy=EXCLUDED.install_policy,
		    updated_at=now(), updated_by=EXCLUDED.updated_by
	`, newID("tadd"), sess.ActiveTenantID, id, enabled, policy, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_POLICY", "Could not update the add-on policy")
		return
	}
	detail := "Workspace add-on policy updated"
	if body.Enabled != nil {
		detail = "Workspace add-on disabled"
		if enabled {
			detail = "Workspace add-on enabled"
		}
	}
	if body.InstallPolicy != nil {
		detail += " · install policy " + policy
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "addon.policy", id, detail, map[string]any{"addonId": id, "enabled": enabled, "installPolicy": policy})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "installPolicy": policy})
}

func (s *Server) adminResolveAddonRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "approved" && status != "denied" {
		writeError(w, http.StatusBadRequest, "ADDON_REQUEST", "Choose approved or denied")
		return
	}
	var addonID, requestUserID, requestType string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE addon_requests
		SET status=$3, resolved_by=$4, resolved_at=now(), updated_at=now()
		WHERE id=$1 AND tenant_id=$2 AND status='pending'
		RETURNING addon_id, user_id::text, request_type
	`, r.PathValue("id"), sess.ActiveTenantID, status, sess.User.ID).Scan(&addonID, &requestUserID, &requestType)
	if err != nil {
		writeError(w, http.StatusNotFound, "ADDON_REQUEST", "Request not found")
		return
	}
	installed := false
	if status == "approved" && requestType == "install" {
		if definition, ok := addonDefinitionByID(addonID); ok {
			if connection, connected := s.addonActiveConnection(r.Context(), sess.ActiveTenantID, addonID, requestUserID, definition.ConnectionScope); connected {
				_, _ = s.store.DB.Exec(r.Context(), `
					INSERT INTO addon_installs (id, tenant_id, addon_id, user_id, connection_id, status)
					VALUES ($1,$2,$3,$4,$5,'active')
					ON CONFLICT (tenant_id, addon_id, user_id) DO UPDATE
					SET status='active', connection_id=EXCLUDED.connection_id, updated_at=now()
				`, newID("ain"), sess.ActiveTenantID, addonID, requestUserID, connection.ID)
				installed = true
			}
		}
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "addon.request_resolved", addonID, "Add-on request "+status+" · "+requestType, map[string]any{
		"addonId": addonID, "requestType": requestType, "status": status, "installed": installed,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status, "requestType": requestType, "installed": installed})
}
