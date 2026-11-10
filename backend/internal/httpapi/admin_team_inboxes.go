package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type adminTeamInboxBody struct {
	Name                    *string `json:"name"`
	Address                 *string `json:"address"`
	OwnerEmail              *string `json:"ownerEmail"`
	GroupID                 *string `json:"groupId"`
	AssignmentStrategy      *string `json:"assignmentStrategy"`
	SLAFirstResponseMinutes *int    `json:"slaFirstResponseMinutes"`
	SLAResolutionMinutes    *int    `json:"slaResolutionMinutes"`
	CanSend                 *bool   `json:"canSend"`
	Active                  *bool   `json:"active"`
}

func (s *Server) adminRequireSharedInbox(w http.ResponseWriter) bool {
	if s.entitled("shared_inbox") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Shared inboxes require a license with the shared_inbox feature")
	return false
}

func (s *Server) adminTeamInboxDetail(ctx context.Context, tenantID, id string) (map[string]any, bool) {
	var team teamMailboxRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT tm.id::text, tm.tenant_id::text, tm.name, tm.address::text,
		       tm.owner_user_id::text, owner.name, owner.email::text,
		       tm.group_id::text, grp.name, tm.assignment_strategy, tm.assignment_cursor,
		       tm.sla_first_response_minutes, tm.sla_resolution_minutes,
		       tm.can_send, tm.active, tm.created_at, tm.updated_at,
		       (SELECT count(*) FROM public.group_members gm WHERE gm.group_id = tm.group_id)
		FROM public.team_mailboxes tm
		JOIN public.users owner ON owner.id = tm.owner_user_id
		JOIN public.groups grp ON grp.id = tm.group_id
		WHERE tm.id::text=$1 AND tm.tenant_id=$2
	`, strings.TrimSpace(id), tenantID).Scan(
		&team.ID, &team.TenantID, &team.Name, &team.Address,
		&team.OwnerID, &team.OwnerName, &team.OwnerEmail,
		&team.GroupID, &team.GroupName, &team.Strategy, &team.Cursor,
		&team.SLAFirst, &team.SLAResolution, &team.CanSend, &team.Active,
		&team.CreatedAt, &team.UpdatedAt, &team.Members)
	if err != nil {
		return nil, false
	}
	payload := teamPayload(team)
	payload["active"] = team.Active
	payload["assignmentCursor"] = team.Cursor
	return payload, true
}

func (s *Server) adminListTeamInboxes(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if !s.adminRequireSharedInbox(w) {
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT tm.id::text, tm.name, tm.address::text, tm.assignment_strategy,
		       tm.sla_first_response_minutes, tm.sla_resolution_minutes, tm.can_send, tm.active,
		       owner.email::text, owner.name, grp.name, tm.group_id::text,
		       (SELECT count(*) FROM public.group_members gm WHERE gm.group_id = tm.group_id),
		       tm.created_at
		FROM public.team_mailboxes tm
		JOIN public.users owner ON owner.id = tm.owner_user_id
		JOIN public.groups grp ON grp.id = tm.group_id
		WHERE tm.tenant_id = $1
		ORDER BY tm.active DESC, tm.name
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEAM_INBOXES", "Could not load team inboxes")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, address, strategy, ownerEmail, ownerName, groupName, groupID string
		var first, resolution, members int
		var canSend, active bool
		var created time.Time
		if rows.Scan(&id, &name, &address, &strategy, &first, &resolution, &canSend, &active,
			&ownerEmail, &ownerName, &groupName, &groupID, &members, &created) != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "address": address, "strategy": strategy,
			"slaFirstResponseMinutes": first, "slaResolutionMinutes": resolution,
			"canSend": canSend, "active": active, "ownerEmail": ownerEmail,
			"ownerName": ownerName, "groupName": groupName, "groupId": groupID,
			"members": members, "createdAt": formatAdminTime(created),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"teamInboxes": items, "count": len(items)})
}

func (s *Server) adminCreateTeamInbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if !s.adminRequireSharedInbox(w) {
		return
	}
	var body adminTeamInboxBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" || body.OwnerEmail == nil || body.GroupID == nil {
		writeError(w, http.StatusBadRequest, "TEAM_INBOX", "Name, owner email and group are required")
		return
	}
	name := truncate(strings.TrimSpace(*body.Name), 100)
	ownerEmail := normalizeEmailValue(*body.OwnerEmail)
	groupID := strings.TrimSpace(*body.GroupID)
	var ownerID string
	var ownerName string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT u.id::text, u.name FROM public.users u
		JOIN public.mailboxes m ON m.user_id = u.id AND m.active
		WHERE u.tenant_id=$1 AND lower(u.email::text)=$2 AND u.status='active' AND u.deleted_at IS NULL
		LIMIT 1
	`, sess.ActiveTenantID, ownerEmail).Scan(&ownerID, &ownerName); err != nil {
		writeError(w, http.StatusBadRequest, "TEAM_OWNER", "Choose an active user with a mailbox in this organization")
		return
	}
	var groupName string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name FROM public.groups WHERE id::text=$1 AND tenant_id=$2 AND status='active'
	`, groupID, sess.ActiveTenantID).Scan(&groupName); err != nil {
		writeError(w, http.StatusBadRequest, "TEAM_GROUP", "Choose an active group in this organization")
		return
	}
	address := ownerEmail
	if body.Address != nil && strings.TrimSpace(*body.Address) != "" {
		address = normalizeEmailValue(*body.Address)
	}
	if !looksLikeEmail(address) {
		writeError(w, http.StatusBadRequest, "TEAM_ADDRESS", "Team inbox address must be a valid email address")
		return
	}
	_, domain := splitEmail(address)
	if !s.adminDomainVerified(r.Context(), sess.ActiveTenantID, domain) {
		writeError(w, http.StatusBadRequest, "TEAM_ADDRESS", "Team inbox address must use a verified organization domain")
		return
	}
	strategy := "manual"
	if body.AssignmentStrategy != nil && strings.TrimSpace(*body.AssignmentStrategy) != "" {
		strategy = strings.ToLower(strings.TrimSpace(*body.AssignmentStrategy))
	}
	if strategy != "manual" && strategy != "round_robin" {
		writeError(w, http.StatusBadRequest, "TEAM_STRATEGY", "Assignment strategy must be manual or round_robin")
		return
	}
	first, resolution := int64(0), int64(0)
	if body.SLAFirstResponseMinutes != nil {
		first = int64(*body.SLAFirstResponseMinutes)
	}
	if body.SLAResolutionMinutes != nil {
		resolution = int64(*body.SLAResolutionMinutes)
	}
	if !validTeamSLA(first) || !validTeamSLA(resolution) {
		writeError(w, http.StatusBadRequest, "TEAM_SLA", "SLA minutes must be between 0 and 10080")
		return
	}
	canSend := true
	if body.CanSend != nil {
		canSend = *body.CanSend
	}
	var id string
	err := s.store.DB.QueryRow(r.Context(), `
		INSERT INTO public.team_mailboxes
		  (tenant_id, name, address, owner_user_id, group_id, assignment_strategy,
		   sla_first_response_minutes, sla_resolution_minutes, can_send, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id::text
	`, sess.ActiveTenantID, name, address, ownerID, groupID, strategy, first, resolution, canSend, sess.User.ID).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "TEAM_EXISTS", "That address, owner or group already has a team inbox")
			return
		}
		writeError(w, http.StatusInternalServerError, "TEAM_INBOX", "Could not create the team inbox")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.team_inbox_created", address, "Created shared inbox "+name, map[string]any{
		"teamInboxId": id, "ownerEmail": ownerEmail, "groupId": groupID,
	})
	detail, _ := s.adminTeamInboxDetail(r.Context(), sess.ActiveTenantID, id)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "teamInbox": detail})
}

func (s *Server) adminUpdateTeamInbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if !s.adminRequireSharedInbox(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	current, ok := s.adminTeamInboxDetail(r.Context(), sess.ActiveTenantID, id)
	if !ok {
		writeError(w, http.StatusNotFound, "TEAM_NOT_FOUND", "Team inbox not found")
		return
	}
	var body adminTeamInboxBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	name := stateString(current, "name")
	address := stateString(current, "address")
	strategy := stateString(current, "strategy")
	first := stateInt(current, "slaFirstResponseMinutes")
	resolution := stateInt(current, "slaResolutionMinutes")
	canSend := stateBool(current, "canSend")
	active := true
	if value, exists := current["active"]; exists {
		if parsed, ok := value.(bool); ok {
			active = parsed
		}
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = truncate(strings.TrimSpace(*body.Name), 100)
	}
	if body.Address != nil {
		address = normalizeEmailValue(*body.Address)
		if !looksLikeEmail(address) {
			writeError(w, http.StatusBadRequest, "TEAM_ADDRESS", "Team inbox address must be a valid email address")
			return
		}
		_, domain := splitEmail(address)
		if !s.adminDomainVerified(r.Context(), sess.ActiveTenantID, domain) {
			writeError(w, http.StatusBadRequest, "TEAM_ADDRESS", "Team inbox address must use a verified organization domain")
			return
		}
	}
	if body.AssignmentStrategy != nil {
		strategy = strings.ToLower(strings.TrimSpace(*body.AssignmentStrategy))
		if strategy != "manual" && strategy != "round_robin" {
			writeError(w, http.StatusBadRequest, "TEAM_STRATEGY", "Assignment strategy must be manual or round_robin")
			return
		}
	}
	if body.SLAFirstResponseMinutes != nil {
		first = int64(*body.SLAFirstResponseMinutes)
	}
	if body.SLAResolutionMinutes != nil {
		resolution = int64(*body.SLAResolutionMinutes)
	}
	if !validTeamSLA(first) || !validTeamSLA(resolution) {
		writeError(w, http.StatusBadRequest, "TEAM_SLA", "SLA minutes must be between 0 and 10080")
		return
	}
	if body.CanSend != nil {
		canSend = *body.CanSend
	}
	if body.Active != nil {
		active = *body.Active
	}
	if body.OwnerEmail != nil || body.GroupID != nil {
		writeError(w, http.StatusBadRequest, "TEAM_IMMUTABLE", "Delete and recreate the team inbox to change its owner or group")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		UPDATE public.team_mailboxes SET name=$3, address=$4, assignment_strategy=$5,
		  sla_first_response_minutes=$6, sla_resolution_minutes=$7, can_send=$8,
		  active=$9, updated_at=now()
		WHERE id::text=$1 AND tenant_id=$2
	`, id, sess.ActiveTenantID, name, address, strategy, first, resolution, canSend, active)
	if err != nil || tag.RowsAffected() == 0 {
		if err != nil && isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "TEAM_EXISTS", "That address is already used by another team inbox")
			return
		}
		writeError(w, http.StatusInternalServerError, "TEAM_INBOX", "Could not update the team inbox")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.team_inbox_updated", address, "Updated shared inbox "+name, map[string]any{"teamInboxId": id})
	detail, _ := s.adminTeamInboxDetail(r.Context(), sess.ActiveTenantID, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "teamInbox": detail})
}

func (s *Server) adminDeleteTeamInbox(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	if !s.adminRequireSharedInbox(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	current, ok := s.adminTeamInboxDetail(r.Context(), sess.ActiveTenantID, id)
	if !ok {
		writeError(w, http.StatusNotFound, "TEAM_NOT_FOUND", "Team inbox not found")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM public.team_mailboxes WHERE id::text=$1 AND tenant_id=$2
	`, id, sess.ActiveTenantID)
	if err != nil || tag.RowsAffected() == 0 {
		writeError(w, http.StatusInternalServerError, "TEAM_INBOX", "Could not delete the team inbox")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.team_inbox_deleted",
		stateString(current, "address"), "Deleted shared inbox "+stateString(current, "name"),
		map[string]any{"teamInboxId": id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func validTeamSLA(minutes int64) bool {
	return minutes >= 0 && minutes <= 10080
}
