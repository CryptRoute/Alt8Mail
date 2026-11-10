package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTeamInboxHelpers(t *testing.T) {
	if validTeamSLA(-1) || !validTeamSLA(0) || !validTeamSLA(10080) || validTeamSLA(10081) {
		t.Fatal("SLA bounds are not enforced")
	}
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	team := teamMailboxRecord{SLAFirst: 30, SLAResolution: 120}
	if state := (&Server{}).teamSLAState(team, teamQueueEntry{FirstDue: &future}); state["state"] != "on_track" {
		t.Fatalf("future SLA = %#v", state)
	}
	if state := (&Server{}).teamSLAState(team, teamQueueEntry{FirstDue: &past}); state["state"] != "breached" {
		t.Fatalf("past SLA = %#v", state)
	}
	if state := (&Server{}).teamSLAState(team, teamQueueEntry{FirstDue: &future, FirstAt: &now, ResolutionDue: &past}); state["state"] != "breached" {
		t.Fatalf("past resolution SLA = %#v", state)
	}
	if state := (&Server{}).teamSLAState(teamMailboxRecord{}, teamQueueEntry{}); state["state"] != "none" {
		t.Fatalf("disabled SLA = %#v", state)
	}
}

// TestTeamInboxIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0064_shared_inboxes.
func TestTeamInboxIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the shared inbox integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantA := fmt.Sprintf("00000000-0000-4005-8000-%012d", time.Now().UnixNano()%1000000000000)
	tenantB := fmt.Sprintf("00000000-0000-4006-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	ownerA := fmt.Sprintf("00000000-0000-4007-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	agent1 := fmt.Sprintf("00000000-0000-4008-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	agent2 := fmt.Sprintf("00000000-0000-4009-8000-%012d", (time.Now().UnixNano()+4)%1000000000000)
	outsider := fmt.Sprintf("00000000-0000-4010-8000-%012d", (time.Now().UnixNano()+5)%1000000000000)
	ownerB := fmt.Sprintf("00000000-0000-4011-8000-%012d", (time.Now().UnixNano()+6)%1000000000000)
	groupA := fmt.Sprintf("00000000-0000-4012-8000-%012d", (time.Now().UnixNano()+7)%1000000000000)
	groupB := fmt.Sprintf("00000000-0000-4013-8000-%012d", (time.Now().UnixNano()+8)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@team.example.test" }
	maildirA := t.TempDir()
	maildirB := t.TempDir()
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
	}
	defer cleanup()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Team A'), ($3,$4,'Team B')`, tenantA, "teama-"+suffix, tenantB, "teamb-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES
		($1,$2,$3,'Owner A','owner'), ($4,$2,$5,'Agent One','member'), ($6,$2,$7,'Agent Two','member'),
		($8,$2,$9,'Outsider','member'), ($10,$11,$12,'Owner B','owner')`,
		ownerA, tenantA, email("owner"), agent1, email("agent1"), agent2, email("agent2"),
		outsider, email("outsider"), ownerB, tenantB, email("ownerb"))
	mustExec(`INSERT INTO public.groups (id, tenant_id, name, email, group_type) VALUES
		($1,$2,'Support A',$3,'shared'), ($4,$5,'Support B',$6,'shared')`,
		groupA, tenantA, email("support"), groupB, tenantB, email("supportb"))
	mustExec(`INSERT INTO public.group_members (group_id, user_id) VALUES ($1,$2), ($1,$3)`, groupA, agent1, agent2)
	mustExec(`INSERT INTO public.group_members (group_id, user_id) VALUES ($1,$2)`, groupB, ownerB)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES
		($1,$2,$3,'team.example.test',$4,$5), ($6,$7,$8,'team.example.test',$9,$10)`,
		tenantA, ownerA, email("owner"), "owner-"+suffix, maildirA,
		tenantB, ownerB, email("ownerb"), "ownerb-"+suffix, maildirB)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','thread-a','Help A','customer@example.test',now() FROM public.mailboxes m WHERE m.user_id=$3`,
		"msg-a-"+suffix, tenantA, ownerA)
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','thread-b','Help B','customer@example.test',now() FROM public.mailboxes m WHERE m.user_id=$3`,
		"msg-b-"+suffix, tenantB, ownerB)
	var teamA, teamB string
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes
		(tenant_id, name, address, owner_user_id, group_id, assignment_strategy, sla_first_response_minutes, sla_resolution_minutes)
		VALUES ($1,'Support A',$2,$3,$4,'round_robin',30,120) RETURNING id::text`,
		tenantA, email("team"), ownerA, groupA).Scan(&teamA); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes
		(tenant_id, name, address, owner_user_id, group_id, assignment_strategy)
		VALUES ($1,'Support B',$2,$3,$4,'manual') RETURNING id::text`,
		tenantB, email("teamb"), ownerB, groupB).Scan(&teamB); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store: &store.Store{DB: pool},
		license: &licensing.License{Version: licensing.Version, ID: "team-test", Edition: "enterprise", Seats: 10,
			Features: []string{"shared_inbox"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	user := func(id, tenant, mail, name string) userRecord {
		return userRecord{ID: id, TenantID: tenant, Email: mail, Name: name, Status: "active"}
	}
	one := user(agent1, tenantA, email("agent1"), "Agent One")
	two := user(agent2, tenantA, email("agent2"), "Agent Two")
	out := user(outsider, tenantA, email("outsider"), "Outsider")
	other := user(ownerB, tenantB, email("ownerb"), "Owner B")

	teams := server.teamMailboxesForUser(ctx, one)
	if len(teams) != 1 || stateString(teams[0], "id") != teamA {
		t.Fatalf("agent team list = %#v", teams)
	}
	if len(server.teamMailboxesForUser(ctx, out)) != 0 {
		t.Fatal("a non-member can see a team inbox")
	}
	if otherTeams := server.teamMailboxesForUser(ctx, other); len(otherTeams) != 1 || stateString(otherTeams[0], "id") != teamB {
		t.Fatalf("tenant B member list = %#v", otherTeams)
	}
	if _, err := server.teamMailboxForUser(ctx, one, teamB); err == nil {
		t.Fatal("cross-tenant team inbox was readable")
	}
	if _, err := server.teamMailboxForUser(ctx, out, teamA); err == nil {
		t.Fatal("non-member team inbox was readable")
	}
	if _, err := server.teamMailboxForUser(ctx, two, teamA); err != nil {
		t.Fatalf("member cannot read team inbox: %v", err)
	}

	team, err := server.teamMailboxRecord(ctx, tenantA, teamA)
	if err != nil {
		t.Fatal(err)
	}
	members, err := server.teamMembers(ctx, team)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %#v err=%v", members, err)
	}
	first, err := server.nextRoundRobinMember(ctx, team, members)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.nextRoundRobinMember(ctx, team, members)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("round-robin repeated %s before visiting both agents", first.ID)
	}

	// Concurrent assignment must not hand the same cursor slot to two callers.
	const workers = 8
	results := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			member, err := server.nextRoundRobinMember(ctx, team, members)
			if err != nil {
				results <- "error"
				return
			}
			results <- member.ID
		}()
	}
	wg.Wait()
	close(results)
	counts := map[string]int{}
	for id := range results {
		counts[id]++
	}
	if len(counts) != 2 {
		t.Fatalf("concurrent round-robin used %d agents: %#v", len(counts), counts)
	}
	for id, count := range counts {
		if count != workers/2 {
			t.Fatalf("agent %s received %d of %d assignments", id, count, workers)
		}
	}

	queue := server.teamQueue(ctx, team, "", one.ID)
	if len(queue) != 1 {
		t.Fatalf("team queue = %#v", queue)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.team_thread_state
		(team_mailbox_id, thread_id, first_response_due_at, resolution_due_at)
		VALUES ($1,'thread-a',now()+interval '30 minutes',now()+interval '2 hours')`, teamA); err != nil {
		t.Fatal(err)
	}
	queue = server.teamQueue(ctx, team, "", one.ID)
	if len(queue) != 1 || stateString(queue[0], "threadId") != "thread-a" {
		t.Fatalf("team queue = %#v", queue)
	}
	sla, _ := queue[0]["sla"].(map[string]any)
	if sla["state"] != "on_track" {
		t.Fatalf("new queue SLA = %#v", sla)
	}
	// Force the first-response target into the past and prove the breach is
	// recorded exactly once while the queue read stays tenant-scoped.
	if _, err := pool.Exec(ctx, `UPDATE public.team_thread_state SET first_response_due_at=now()-interval '1 minute' WHERE team_mailbox_id=$1 AND thread_id='thread-a'`, teamA); err != nil {
		t.Fatal(err)
	}
	queue = server.teamQueue(ctx, team, "breached", one.ID)
	if len(queue) != 1 {
		t.Fatalf("breached queue = %#v", queue)
	}
	server.markTeamSLABreaches(ctx, team, queue)
	var breached int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.team_thread_state WHERE team_mailbox_id=$1 AND thread_id='thread-a' AND breached_at IS NOT NULL`, teamA).Scan(&breached); err != nil || breached != 1 {
		t.Fatalf("breach count = %d err=%v", breached, err)
	}
	server.markTeamSLABreaches(ctx, team, queue)
	var notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.mail_notifications WHERE kind='sla_breach' AND thread_id='thread-a'`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("SLA notification count = %d err=%v", notifications, err)
	}

	// Presence is advisory, tenant-scoped and expires automatically. A second
	// agent in "replying" state raises the collision flag for the first agent.
	if _, err := pool.Exec(ctx, `INSERT INTO public.team_thread_presence
		(team_mailbox_id, thread_id, user_id, state, expires_at)
		VALUES ($1,'thread-a',$2,'viewing',now()+interval '2 minutes'),
		       ($1,'thread-a',$3,'replying',now()+interval '2 minutes'),
		       ($1,'thread-b',$2,'replying',now()-interval '1 minute')`, teamA, agent1, agent2); err != nil {
		t.Fatal(err)
	}
	agents, collision := server.teamPresenceSnapshot(ctx, team, "thread-a", agent1)
	if !collision || len(agents) != 2 {
		t.Fatalf("presence = %#v collision=%v", agents, collision)
	}
	if _, selfCollision := server.teamPresenceSnapshot(ctx, team, "thread-a", agent2); selfCollision {
		t.Fatal("an agent's own reply presence was treated as a collision")
	}
	expired, expiredCollision := server.teamPresenceSnapshot(ctx, team, "thread-b", agent1)
	if len(expired) != 0 || expiredCollision {
		t.Fatalf("expired presence = %#v collision=%v", expired, expiredCollision)
	}

	// Watching is team-scoped and idempotent.
	if _, err := pool.Exec(ctx, `INSERT INTO public.team_thread_watchers (team_mailbox_id, thread_id, user_id)
		VALUES ($1,'thread-a',$2) ON CONFLICT DO NOTHING`, teamA, agent1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.team_thread_watchers (team_mailbox_id, thread_id, user_id)
		VALUES ($1,'thread-a',$2) ON CONFLICT DO NOTHING`, teamA, agent1); err != nil {
		t.Fatal(err)
	}
	var watchers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.team_thread_watchers WHERE team_mailbox_id=$1 AND thread_id='thread-a'`, teamA).Scan(&watchers); err != nil || watchers != 1 {
		t.Fatalf("watcher count = %d err=%v", watchers, err)
	}
}
