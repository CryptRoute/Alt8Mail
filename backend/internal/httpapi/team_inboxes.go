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

type teamMemberRecord struct {
	ID    string
	Name  string
	Email string
	Role  string
}

func (s *Server) requireSharedInboxEntitlement(w http.ResponseWriter) bool {
	if s.entitled("shared_inbox") {
		return true
	}
	writeError(w, http.StatusForbidden, "FEATURE_REQUIRED", "Shared inboxes require an Alt8Mail license with the shared_inbox feature")
	return false
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

func (s *Server) teamMembers(ctx context.Context, team teamMailboxRecord) ([]teamMemberRecord, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT u.id::text, u.name, u.email::text, gm.member_role
		FROM public.group_members gm
		JOIN public.users u ON u.id = gm.user_id
		WHERE gm.group_id = $1 AND u.tenant_id = $2
		  AND u.status = 'active' AND u.deleted_at IS NULL
		ORDER BY u.name, u.email
	`, team.GroupID, team.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []teamMemberRecord{}
	for rows.Next() {
		var member teamMemberRecord
		if rows.Scan(&member.ID, &member.Name, &member.Email, &member.Role) == nil {
			members = append(members, member)
		}
	}
	return members, rows.Err()
}

func (s *Server) teamThreadBelongs(ctx context.Context, team teamMailboxRecord, threadID string) bool {
	var exists bool
	_ = s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.mail_messages WHERE user_id=$1 AND thread_id=$2)
	`, team.OwnerID, strings.TrimSpace(threadID)).Scan(&exists)
	return exists
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

func (s *Server) registerTeamInboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/team-inboxes", s.requireUser(s.getTeamInboxes))
	mux.HandleFunc("GET /api/team-inboxes/{id}", s.requireUser(s.getTeamInbox))
	mux.HandleFunc("GET /api/team-inboxes/{id}/queue", s.requireUser(s.getTeamQueue))
	mux.HandleFunc("POST /api/team-inboxes/{id}/threads/{threadId}/assign", s.requireUser(s.assignTeamThread))
	mux.HandleFunc("POST /api/team-inboxes/{id}/threads/{threadId}/touch", s.requireUser(s.touchTeamThread))
	mux.HandleFunc("GET /api/team-inboxes/{id}/threads/{threadId}/presence", s.requireUser(s.getTeamPresence))
	mux.HandleFunc("POST /api/team-inboxes/{id}/threads/{threadId}/presence", s.requireUser(s.setTeamPresence))
	mux.HandleFunc("DELETE /api/team-inboxes/{id}/threads/{threadId}/presence", s.requireUser(s.clearTeamPresence))
	mux.HandleFunc("GET /api/team-inboxes/{id}/threads/{threadId}/watchers", s.requireUser(s.listTeamWatchers))
	mux.HandleFunc("POST /api/team-inboxes/{id}/threads/{threadId}/watchers", s.requireUser(s.addTeamWatcher))
	mux.HandleFunc("DELETE /api/team-inboxes/{id}/threads/{threadId}/watchers", s.requireUser(s.removeTeamWatcher))
}

func (s *Server) getTeamInboxes(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{"teamInboxes": s.teamMailboxesForUser(r.Context(), sess.User)})
}

func (s *Server) getTeamInbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"teamInbox": teamPayload(team)})
}

func teamPayload(team teamMailboxRecord) map[string]any {
	return map[string]any{
		"id": team.ID, "name": team.Name, "address": team.Address,
		"ownerId": team.OwnerID, "ownerName": team.OwnerName, "ownerEmail": team.OwnerEmail,
		"groupId": team.GroupID, "groupName": team.GroupName,
		"strategy": team.Strategy, "slaFirstResponseMinutes": team.SLAFirst,
		"slaResolutionMinutes": team.SLAResolution, "canSend": team.CanSend,
		"members": team.Members, "createdAt": team.CreatedAt.UTC().Format(time.RFC3339),
		"updatedAt": team.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type teamQueueEntry struct {
	ThreadID      string
	Subject       string
	Snippet       string
	FromName      string
	FromEmail     string
	SentAt        time.Time
	Unread        bool
	Status        string
	Closed        bool
	AssignedID    string
	AssignedName  string
	AssignedMail  string
	FirstAt       *time.Time
	FirstDue      *time.Time
	ResolutionDue *time.Time
	BreachedAt    *time.Time
	LastAgentAt   *time.Time
}

func (s *Server) teamQueue(ctx context.Context, team teamMailboxRecord, filter string, userID string) []map[string]any {
	rows, err := s.store.DB.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (m.thread_id)
			       m.thread_id, m.subject, m.snippet, m.from_name, m.from_email, m.sent_at, m.unread
			FROM public.mail_messages m
			WHERE m.user_id = $1 AND m.folder = 'inbox'
			ORDER BY m.thread_id, m.sent_at DESC
		)
		SELECT l.thread_id, l.subject, l.snippet, l.from_name, l.from_email::text, l.sent_at, l.unread,
		       COALESCE(meta.status, 'new'), (meta.closed_at IS NOT NULL),
		       COALESCE(meta.assigned_user_id::text, ''), COALESCE(a.name, ''), COALESCE(a.email::text, ''),
		       st.first_response_at, st.first_response_due_at, st.resolution_due_at, st.breached_at, st.last_agent_at
		FROM latest l
		LEFT JOIN public.mail_thread_meta meta
		  ON meta.owner_user_id = $1 AND meta.thread_id = l.thread_id
		LEFT JOIN public.users a ON a.id = meta.assigned_user_id
		LEFT JOIN public.team_thread_state st
		  ON st.team_mailbox_id = $2 AND st.thread_id = l.thread_id
		ORDER BY l.sent_at DESC
		LIMIT 300
	`, team.OwnerID, team.ID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	entries := []teamQueueEntry{}
	for rows.Next() {
		var entry teamQueueEntry
		if rows.Scan(&entry.ThreadID, &entry.Subject, &entry.Snippet, &entry.FromName, &entry.FromEmail,
			&entry.SentAt, &entry.Unread, &entry.Status, &entry.Closed, &entry.AssignedID,
			&entry.AssignedName, &entry.AssignedMail, &entry.FirstAt, &entry.FirstDue,
			&entry.ResolutionDue, &entry.BreachedAt, &entry.LastAgentAt) != nil {
			continue
		}
		entries = append(entries, entry)
	}
	out := []map[string]any{}
	for _, entry := range entries {
		if filter == "mine" && entry.AssignedID != userID {
			continue
		}
		if filter == "unassigned" && entry.AssignedID != "" {
			continue
		}
		sla := s.teamSLAState(team, entry)
		if strings.EqualFold(filter, "breached") && sla["state"] != "breached" {
			continue
		}
		out = append(out, map[string]any{
			"threadId": entry.ThreadID, "subject": entry.Subject, "snippet": entry.Snippet,
			"from":   map[string]any{"name": entry.FromName, "email": entry.FromEmail},
			"sentAt": entry.SentAt.UTC().Format(time.RFC3339), "unread": entry.Unread,
			"status": entry.Status, "closed": entry.Closed,
			"assignedTo": teamAssigneePayload(entry), "sla": sla,
			"firstResponseAt": formatOptionalTime(entry.FirstAt),
			"lastAgentAt":     formatOptionalTime(entry.LastAgentAt),
		})
	}
	return out
}

func teamAssigneePayload(entry teamQueueEntry) any {
	if entry.AssignedID == "" {
		return nil
	}
	return map[string]any{"id": entry.AssignedID, "name": entry.AssignedName, "email": entry.AssignedMail}
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func (s *Server) teamSLAState(team teamMailboxRecord, entry teamQueueEntry) map[string]any {
	state := map[string]any{
		"state": "none", "firstResponseDueAt": formatOptionalTime(entry.FirstDue),
		"resolutionDueAt": formatOptionalTime(entry.ResolutionDue),
	}
	if team.SLAFirst == 0 && team.SLAResolution == 0 {
		return state
	}
	now := time.Now()
	due := entry.FirstDue
	if entry.FirstAt != nil {
		due = entry.ResolutionDue
	}
	if due == nil {
		state["state"] = "met"
		return state
	}
	if entry.BreachedAt != nil || now.After(*due) {
		state["state"] = "breached"
		return state
	}
	if due.Sub(now) <= 15*time.Minute {
		state["state"] = "at_risk"
		return state
	}
	state["state"] = "on_track"
	return state
}

func (s *Server) getTeamQueue(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("filter")))
	if filter != "" && filter != "all" && filter != "mine" && filter != "unassigned" && filter != "breached" {
		writeError(w, http.StatusBadRequest, "TEAM_FILTER", "Filter must be all, mine, unassigned or breached")
		return
	}
	queue := s.teamQueue(r.Context(), team, filter, sess.User.ID)
	s.markTeamSLABreaches(r.Context(), team, queue)
	writeJSON(w, http.StatusOK, map[string]any{
		"teamInbox": teamPayload(team), "queue": queue, "count": len(queue),
	})
}

func (s *Server) markTeamSLABreaches(ctx context.Context, team teamMailboxRecord, queue []map[string]any) {
	for _, entry := range queue {
		sla, _ := entry["sla"].(map[string]any)
		if sla == nil || sla["state"] != "breached" {
			continue
		}
		threadID := stateString(entry, "threadId")
		var changed bool
		err := s.store.DB.QueryRow(ctx, `
			UPDATE public.team_thread_state SET breached_at = now(), updated_at = now()
			WHERE team_mailbox_id = $1 AND thread_id = $2 AND breached_at IS NULL
			  AND (
			    (first_response_at IS NULL AND first_response_due_at IS NOT NULL AND first_response_due_at < now())
			    OR (first_response_at IS NOT NULL AND resolution_due_at IS NOT NULL AND resolution_due_at < now())
			  )
			RETURNING true
		`, team.ID, threadID).Scan(&changed)
		if err != nil || !changed {
			continue
		}
		_, _ = s.store.DB.Exec(ctx, `
			INSERT INTO public.mail_notifications
			  (id, tenant_id, user_id, kind, title, body, detail, thread_id, owner_user_id)
			VALUES ($1, $2, $3, 'sla_breach', $4, $5, $6, $7, $8)
		`, newID("notification"), team.TenantID, team.OwnerID, "SLA breach · "+team.Name,
			firstNonEmpty(stateString(entry, "subject"), "(no subject)"),
			"An SLA target passed for this conversation", threadID, team.OwnerID)
	}
}

func (s *Server) assignTeamThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" || !s.teamThreadBelongs(r.Context(), team, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	var body struct {
		Assignee string `json:"assignee"`
		Strategy string `json:"strategy"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	members, err := s.teamMembers(r.Context(), team)
	if err != nil || len(members) == 0 {
		writeError(w, http.StatusConflict, "TEAM_EMPTY", "This team inbox has no active agents")
		return
	}
	strategy := strings.ToLower(strings.TrimSpace(body.Strategy))
	if strategy != "" && strategy != "manual" && strategy != "round_robin" {
		writeError(w, http.StatusBadRequest, "TEAM_STRATEGY", "Strategy must be manual or round_robin")
		return
	}
	if strategy == "" {
		strategy = team.Strategy
	}
	var assignee teamMemberRecord
	if email := normalizeEmailValue(body.Assignee); email != "" {
		for _, member := range members {
			if normalizeEmailValue(member.Email) == email {
				assignee = member
				break
			}
		}
		if assignee.ID == "" {
			writeError(w, http.StatusBadRequest, "TEAM_ASSIGNEE", "Choose an active agent in this team inbox")
			return
		}
		strategy = "manual"
	} else if strategy == "round_robin" {
		assignee, err = s.nextRoundRobinMember(r.Context(), team, members)
		if err != nil {
			writeFromError(w, err)
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "TEAM_ASSIGNEE", "Choose an agent or select round-robin assignment")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.mail_thread_meta
		  (owner_user_id, thread_id, status, assigned_user_id, assigned_by, assigned_at, updated_at)
		VALUES ($1, $2, 'new', $3, $4, now(), now())
		ON CONFLICT (owner_user_id, thread_id) DO UPDATE SET
		  assigned_user_id = $3, assigned_by = $4, assigned_at = now(), updated_at = now()
	`, team.OwnerID, threadID, assignee.ID, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEAM_ASSIGN", "Could not assign the conversation")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_assignment_log
		  (team_mailbox_id, thread_id, assigned_to, assigned_by, strategy)
		VALUES ($1, $2, $3, $4, $5)
	`, team.ID, threadID, assignee.ID, sess.User.ID, strategy)
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_thread_state
		  (team_mailbox_id, thread_id, first_response_due_at, resolution_due_at)
		VALUES ($1, $2,
		        CASE WHEN $3 > 0 THEN now() + make_interval(mins => $3) END,
		        CASE WHEN $4 > 0 THEN now() + make_interval(mins => $4) END)
		ON CONFLICT (team_mailbox_id, thread_id) DO UPDATE SET
		  first_response_due_at = COALESCE(public.team_thread_state.first_response_due_at, EXCLUDED.first_response_due_at),
		  resolution_due_at = COALESCE(public.team_thread_state.resolution_due_at, EXCLUDED.resolution_due_at),
		  updated_at = now()
	`, team.ID, threadID, team.SLAFirst, team.SLAResolution)
	if assignee.ID != sess.User.ID {
		s.createAssignmentNotification(r.Context(), sess.User, userRecord{ID: team.OwnerID, TenantID: team.TenantID, Name: team.OwnerName, Email: team.OwnerEmail}, threadID, assignee.ID, assignee.Name, "new")
	}
	s.audit(r, sess.User, "team.thread_assigned", threadID, "Assigned "+assignee.Email+" from "+team.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "assignedTo": map[string]any{
		"id": assignee.ID, "name": assignee.Name, "email": assignee.Email,
	}, "strategy": strategy, "teamInbox": teamPayload(team), "meta": s.loadThreadMeta(r.Context(), team.OwnerID, threadID)})
}

func (s *Server) nextRoundRobinMember(ctx context.Context, team teamMailboxRecord, members []teamMemberRecord) (teamMemberRecord, error) {
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return teamMemberRecord{}, err
	}
	defer tx.Rollback(ctx)
	var cursor int
	if err := tx.QueryRow(ctx, `SELECT assignment_cursor FROM public.team_mailboxes WHERE id=$1 AND tenant_id=$2 AND active FOR UPDATE`, team.ID, team.TenantID).Scan(&cursor); err != nil {
		return teamMemberRecord{}, err
	}
	if len(members) == 0 {
		return teamMemberRecord{}, fail(http.StatusConflict, "TEAM_EMPTY", "This team inbox has no active agents")
	}
	index := cursor % len(members)
	next := members[index]
	if _, err := tx.Exec(ctx, `UPDATE public.team_mailboxes SET assignment_cursor=$2, updated_at=now() WHERE id=$1`, team.ID, (index+1)%len(members)); err != nil {
		return teamMemberRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return teamMemberRecord{}, err
	}
	return next, nil
}

func (s *Server) touchTeamThread(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if !s.teamThreadBelongs(r.Context(), team, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	_ = s.decodeBody(w, r, &body, 8*1024)
	if body.Kind == "" {
		body.Kind = "reply"
	}
	if body.Kind != "reply" && body.Kind != "view" {
		writeError(w, http.StatusBadRequest, "TEAM_TOUCH", "Kind must be reply or view")
		return
	}
	firstAt := any(nil)
	if body.Kind == "reply" {
		firstAt = time.Now().UTC()
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_thread_state
		  (team_mailbox_id, thread_id, first_response_at, last_agent_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (team_mailbox_id, thread_id) DO UPDATE SET
		  first_response_at = COALESCE(public.team_thread_state.first_response_at, EXCLUDED.first_response_at),
		  last_agent_at = now(), updated_at = now()
	`, team.ID, threadID, firstAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEAM_TOUCH", "Could not record the team response")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) getTeamPresence(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if !s.teamThreadBelongs(r.Context(), team, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT p.user_id::text, u.name, u.email::text, p.state, p.expires_at
		FROM public.team_thread_presence p
		JOIN public.users u ON u.id = p.user_id
		WHERE p.team_mailbox_id=$1 AND p.thread_id=$2 AND p.expires_at > now()
		ORDER BY p.updated_at DESC
	`, team.ID, threadID)
	agents := []map[string]any{}
	collision := false
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, email, state string
			var expires time.Time
			if rows.Scan(&id, &name, &email, &state, &expires) != nil {
				continue
			}
			agents = append(agents, map[string]any{
				"id": id, "name": name, "email": email, "state": state,
				"expiresAt": expires.UTC().Format(time.RFC3339), "self": id == sess.User.ID,
			})
			if id != sess.User.ID && state == "replying" {
				collision = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "collision": collision})
}

func (s *Server) setTeamPresence(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if !s.teamThreadBelongs(r.Context(), team, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	var body struct {
		State      string `json:"state"`
		TTLSeconds int    `json:"ttlSeconds"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	state := strings.ToLower(strings.TrimSpace(body.State))
	if state == "" {
		state = "viewing"
	}
	if state != "viewing" && state != "replying" {
		writeError(w, http.StatusBadRequest, "TEAM_PRESENCE", "State must be viewing or replying")
		return
	}
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = 120
	}
	if ttl < 15 || ttl > 300 {
		writeError(w, http.StatusBadRequest, "TEAM_PRESENCE", "Presence TTL must be 15–300 seconds")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_thread_presence
		  (team_mailbox_id, thread_id, user_id, state, expires_at, updated_at)
		VALUES ($1,$2,$3,$4,now() + make_interval(secs => $5),now())
		ON CONFLICT (team_mailbox_id, thread_id, user_id) DO UPDATE SET
		  state=EXCLUDED.state, expires_at=EXCLUDED.expires_at, updated_at=now()
	`, team.ID, threadID, sess.User.ID, state, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEAM_PRESENCE", "Could not update team presence")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state, "expiresIn": ttl})
}

func (s *Server) clearTeamPresence(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		DELETE FROM public.team_thread_presence
		WHERE team_mailbox_id=$1 AND thread_id=$2 AND user_id=$3
	`, team.ID, strings.TrimSpace(r.PathValue("threadId")), sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listTeamWatchers(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT u.id::text, u.name, u.email::text
		FROM public.team_thread_watchers w
		JOIN public.users u ON u.id = w.user_id
		WHERE w.team_mailbox_id=$1 AND w.thread_id=$2
		ORDER BY u.name
	`, team.ID, threadID)
	watchers := []map[string]any{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, email string
			if rows.Scan(&id, &name, &email) == nil {
				watchers = append(watchers, map[string]any{"id": id, "name": name, "email": email, "self": id == sess.User.ID})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"watchers": watchers})
}

func (s *Server) addTeamWatcher(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if !s.teamThreadBelongs(r.Context(), team, threadID) {
		writeError(w, http.StatusNotFound, "THREAD_NOT_FOUND", "Conversation not found")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.team_thread_watchers (team_mailbox_id, thread_id, user_id)
		VALUES ($1,$2,$3) ON CONFLICT DO NOTHING
	`, team.ID, threadID, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TEAM_WATCH", "Could not watch the conversation")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) removeTeamWatcher(w http.ResponseWriter, r *http.Request) {
	if !s.requireSharedInboxEntitlement(w) {
		return
	}
	sess := s.requireSessionFromContext(r)
	team, err := s.teamMailboxForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeFromError(w, err)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		DELETE FROM public.team_thread_watchers
		WHERE team_mailbox_id=$1 AND thread_id=$2 AND user_id=$3
	`, team.ID, strings.TrimSpace(r.PathValue("threadId")), sess.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// teamMailboxForOwner returns the active team inbox backed by a mailbox owner.
// It is used to attribute thread status updates to team SLA state.
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
