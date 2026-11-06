package control

import (
	"net/http"
	"strings"
)

func (s *Server) registerAddonRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/addons", s.require("server.read", s.handleAddons))
	mux.HandleFunc("PATCH /control/api/addons/{id}", s.require("server.manage", s.handleAddonUpdate))
}

func (s *Server) handleAddons(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT a.id, a.name, a.vendor, a.category, a.description, a.tagline,
		       a.connection_scope, a.auth_type, a.self_service, a.requires_tenant_setup,
		       a.enabled, a.sort_order, a.website_url, a.support_url, a.privacy_url,
		       a.docs_url, a.logo_asset, a.connector_version,
		       (SELECT count(*) FROM public.tenant_addons t WHERE t.addon_id = a.id AND t.enabled) AS workspace_count,
		       (SELECT count(*) FROM public.addon_installs i WHERE i.addon_id = a.id AND i.status = 'active') AS install_count
		FROM public.addon_system_catalog a
		ORDER BY a.sort_order, a.name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDONS", "Could not load the add-on catalog")
		return
	}
	defer rows.Close()
	addons := []map[string]any{}
	for rows.Next() {
		var id, name, vendor, category, description, tagline, scope, authType string
		var websiteURL, supportURL, privacyURL, docsURL, logoAsset, connectorVersion string
		var selfService, requiresSetup, enabled bool
		var sortOrder, workspaceCount, installCount int
		if rows.Scan(&id, &name, &vendor, &category, &description, &tagline,
			&scope, &authType, &selfService, &requiresSetup, &enabled, &sortOrder,
			&websiteURL, &supportURL, &privacyURL, &docsURL, &logoAsset, &connectorVersion,
			&workspaceCount, &installCount) != nil {
			continue
		}
		addons = append(addons, map[string]any{
			"id": id, "name": name, "vendor": vendor, "category": category,
			"description": description, "tagline": tagline,
			"connectionScope": scope, "authType": authType,
			"selfService": selfService, "requiresTenantSetup": requiresSetup,
			"enabled": enabled, "sortOrder": sortOrder,
			"websiteUrl": websiteURL, "supportUrl": supportURL, "privacyUrl": privacyURL,
			"docsUrl": docsURL, "logoAsset": logoAsset, "connectorVersion": connectorVersion,
			"workspaceCount": workspaceCount, "installCount": installCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"addons": addons, "count": len(addons),
		"enabled": countEnabledAddons(addons),
	})
}

func countEnabledAddons(addons []map[string]any) int {
	total := 0
	for _, addon := range addons {
		if enabled, _ := addon["enabled"].(bool); enabled {
			total++
		}
	}
	return total
}

func (s *Server) handleAddonUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "ADDON_ID", "Add-on id is required")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "ADDON_POLICY", "Choose whether the add-on is enabled")
		return
	}
	tag, err := s.db.Exec(r.Context(), `
		UPDATE public.addon_system_catalog SET enabled = $2, updated_at = now() WHERE id = $1
	`, id, *body.Enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDONS", "Could not update the add-on catalog")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	state := "disabled"
	if *body.Enabled {
		state = "enabled"
	}
	s.actorAudit(r, sess, "addon.system_policy", id, "System-wide add-on "+state, map[string]any{"addonId": id, "enabled": *body.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
}
