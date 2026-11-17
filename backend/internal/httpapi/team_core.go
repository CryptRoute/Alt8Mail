package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type teamMailboxRecord struct {
	ID            string
	TenantID      string
	Name          string
	Address       string
	OwnerID       string
	OwnerName     string
	OwnerEmail    string
	GroupID       string
	GroupName     string
	Strategy      string
	Cursor        int
	SLAFirst      int
	SLAResolution int
	CanSend       bool
	Active        bool
	Members       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (s *Server) teamMailboxRecord(ctx context.Context, tenantID, teamID string) (teamMailboxRecord, error) {
	var team teamMailboxRecord
	err := s.store.DB.QueryRow(ctx, `
		SELECT tm.id::text, tm.tenant_id::text, tm.name, tm.address::text,
		       tm.owner_user_id::text, owner.name, owner.email::text,
		       tm.group_id::text, grp.name, tm.assignment_strategy, tm.assignment_cursor,
		       tm.sla_first_response_minutes, tm.sla_resolution_minutes,
		       tm.can_send, tm.active, tm.created_at, tm.updated_at,
		       (SELECT count(*) FROM public.group_members gm WHERE gm.group_id = tm.group_id)
		FROM public.team_mailboxes tm
		JOIN public.users owner ON owner.id = tm.owner_user_id AND owner.tenant_id = tm.tenant_id
		JOIN public.groups grp ON grp.id = tm.group_id AND grp.tenant_id = tm.tenant_id
		WHERE tm.id::text = $1 AND tm.tenant_id = $2 AND tm.active
	`, strings.TrimSpace(teamID), tenantID).Scan(
		&team.ID, &team.TenantID, &team.Name, &team.Address,
		&team.OwnerID, &team.OwnerName, &team.OwnerEmail,
		&team.GroupID, &team.GroupName, &team.Strategy, &team.Cursor,
		&team.SLAFirst, &team.SLAResolution, &team.CanSend, &team.Active,
		&team.CreatedAt, &team.UpdatedAt, &team.Members)
	if err != nil {
		return team, err
	}
	return team, nil
}

func (s *Server) teamMailboxForUser(ctx context.Context, user userRecord, teamID string) (teamMailboxRecord, error) {
	team, err := s.teamMailboxRecord(ctx, user.TenantID, teamID)
	if err != nil {
		return team, fail(http.StatusNotFound, "TEAM_NOT_FOUND", "Team inbox not found")
	}
	var member bool
	err = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM public.group_members gm
			JOIN public.groups g ON g.id = gm.group_id AND g.tenant_id = $3
			WHERE gm.group_id = $1 AND gm.user_id = $2 AND g.status = 'active'
		)
	`, team.GroupID, user.ID, user.TenantID).Scan(&member)
	if err != nil || !member {
		return team, fail(http.StatusNotFound, "TEAM_NOT_FOUND", "Team inbox not found")
	}
	return team, nil
}

func (s *Server) teamMailboxForOwner(ctx context.Context, tenantID, ownerID string) (teamMailboxRecord, bool) {
	var teamID string
	err := s.store.DB.QueryRow(ctx, `
		SELECT id::text FROM public.team_mailboxes
		WHERE tenant_id=$1 AND owner_user_id=$2 AND active LIMIT 1
	`, tenantID, ownerID).Scan(&teamID)
	if err != nil || teamID == "" {
		return teamMailboxRecord{}, false
	}
	team, err := s.teamMailboxRecord(ctx, tenantID, teamID)
	return team, err == nil
}

func (s *Server) markTeamThreadResponded(ctx context.Context, tenantID, ownerID, threadID, userID string) {
	team, ok := s.teamMailboxForOwner(ctx, tenantID, ownerID)
	if !ok {
		return
	}
	var member bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.group_members WHERE group_id=$1 AND user_id=$2)
	`, team.GroupID, userID).Scan(&member)
	if !member {
		return
	}
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.team_thread_state
		  (team_mailbox_id, thread_id, first_response_at, last_agent_at)
		VALUES ($1,$2,now(),now())
		ON CONFLICT (team_mailbox_id, thread_id) DO UPDATE SET
		  first_response_at = COALESCE(public.team_thread_state.first_response_at, now()),
		  last_agent_at = now(), updated_at = now()
	`, team.ID, threadID)
}

// teamCanManage reports whether a user can administer a team inbox.
func (s *Server) teamCanManage(ctx context.Context, team teamMailboxRecord, user userRecord) bool {
	if user.Role == "owner" || user.Role == "admin" {
		return true
	}
	var owner bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.group_members
		WHERE group_id=$1 AND user_id=$2 AND member_role='owner')
	`, team.GroupID, user.ID).Scan(&owner)
	return owner
}

func (s *Server) teamMailboxesForUser(ctx context.Context, user userRecord) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT tm.id::text, tm.name, tm.address::text, tm.assignment_strategy,
		       tm.sla_first_response_minutes, tm.sla_resolution_minutes, tm.can_send,
		       tm.owner_user_id::text, owner.name, owner.email::text,
		       tm.group_id::text, grp.name,
		       (SELECT count(*) FROM public.group_members gm2 WHERE gm2.group_id = tm.group_id)
		FROM public.team_mailboxes tm
		JOIN public.group_members gm ON gm.group_id = tm.group_id AND gm.user_id = $1
		JOIN public.groups grp ON grp.id = tm.group_id AND grp.status = 'active'
		JOIN public.users owner ON owner.id = tm.owner_user_id AND owner.status = 'active'
		WHERE tm.tenant_id = $2 AND tm.active
		ORDER BY tm.name
	`, user.ID, user.TenantID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, address, strategy, ownerID, ownerName, ownerEmail, groupID, groupName string
		var first, resolution, members int
		var canSend bool
		if rows.Scan(&id, &name, &address, &strategy, &first, &resolution, &canSend,
			&ownerID, &ownerName, &ownerEmail, &groupID, &groupName, &members) != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "address": address, "strategy": strategy,
			"slaFirstResponseMinutes": first, "slaResolutionMinutes": resolution,
			"canSend": canSend, "ownerId": ownerID, "ownerName": ownerName,
			"ownerEmail": ownerEmail, "groupId": groupID, "groupName": groupName,
			"members": members, "agents": s.teamGroupAgents(ctx, user.TenantID, groupID),
		})
	}
	return out
}

func (s *Server) teamGroupAgents(ctx context.Context, tenantID, groupID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.name, u.email::text
		FROM public.group_members gm
		JOIN public.users u ON u.id = gm.user_id
		WHERE gm.group_id::text = $1 AND u.tenant_id = $2
		  AND u.status = 'active' AND u.deleted_at IS NULL
		ORDER BY u.name, u.email
	`, groupID, tenantID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	agents := []map[string]any{}
	for rows.Next() {
		var id, name, email string
		if rows.Scan(&id, &name, &email) == nil {
			agents = append(agents, map[string]any{"id": id, "name": name, "email": email})
		}
	}
	return agents
}
