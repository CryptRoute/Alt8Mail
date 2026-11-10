package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSharedDraftPayloadValidation(t *testing.T) {
	if _, _, err := sharedDraftPayload(json.RawMessage(`[]`)); err == nil {
		t.Fatal("array payload was accepted")
	}
	payload, subject, err := sharedDraftPayload(json.RawMessage(`{"subject":"  Hello  ","bodyText":"Body"}`))
	if err != nil || subject != "Hello" || !strings.Contains(string(payload), `"bodyText":"Body"`) {
		t.Fatalf("payload = %s subject=%q err=%v", payload, subject, err)
	}
	if _, _, err := sharedDraftPayload(json.RawMessage(make([]byte, sharedDraftMaxPayload+1))); err == nil {
		t.Fatal("oversized payload was accepted")
	}
}

// TestSharedDraftIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0065_shared_drafts.
func TestSharedDraftIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the shared draft integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4014-8000-%012d", time.Now().UnixNano()%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4015-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	agent := fmt.Sprintf("00000000-0000-4016-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	outsider := fmt.Sprintf("00000000-0000-4017-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	group := fmt.Sprintf("00000000-0000-4018-8000-%012d", (time.Now().UnixNano()+4)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@draft.example.test" }
	cleanup := func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }
	defer cleanup()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Draft Test')`, tenant, "draft-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES
		($1,$2,$3,'Owner','owner'), ($4,$2,$5,'Agent','member'), ($6,$2,$7,'Outsider','member')`,
		owner, tenant, email("owner"), agent, email("agent"), outsider, email("outsider"))
	mustExec(`INSERT INTO public.groups (id, tenant_id, name, email, group_type) VALUES ($1,$2,'Draft Team',$3,'shared')`, group, tenant, email("team"))
	mustExec(`INSERT INTO public.group_members (group_id, user_id) VALUES ($1,$2)`, group, agent)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'draft.example.test',$4,$5)`,
		tenant, owner, email("owner"), "owner-"+suffix, t.TempDir())
	teamID := ""
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes (tenant_id, name, address, owner_user_id, group_id)
		VALUES ($1,'Drafts',$2,$3,$4) RETURNING id::text`, tenant, email("team"), owner, group).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store: &store.Store{DB: pool},
		license: &licensing.License{Version: licensing.Version, ID: "draft-test", Edition: "enterprise", Seats: 10,
			Features: []string{"shared_drafts", "shared_inbox"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	agentUser := userRecord{ID: agent, TenantID: tenant, Email: email("agent"), Name: "Agent", Status: "active"}
	outsiderUser := userRecord{ID: outsider, TenantID: tenant, Email: email("outsider"), Name: "Outsider", Status: "active"}
	draftID := "sdraft_" + suffix
	token := "lease_" + suffix
	if _, err := pool.Exec(ctx, `INSERT INTO public.shared_drafts
		(id, tenant_id, team_mailbox_id, subject, payload, version, created_by, updated_by, lease_owner, lease_token, lease_expires_at)
		VALUES ($1,$2,$3,'Initial','{"subject":"Initial","bodyText":"One"}'::jsonb,1,$4,$4,$4,$5,now()+interval '5 minutes')`,
		draftID, tenant, teamID, agent, token); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.shared_draft_versions (shared_draft_id, version, payload, author_user_id)
		VALUES ($1,1,'{"subject":"Initial","bodyText":"One"}'::jsonb,$2)`, draftID, agent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.sharedDraftForUser(ctx, outsiderUser, draftID); err == nil {
		t.Fatal("a non-member can read a shared draft")
	}
	if _, _, err := server.sharedDraftForUser(ctx, agentUser, draftID); err != nil {
		t.Fatalf("member cannot read shared draft: %v", err)
	}
	if _, err := server.updateSharedDraft(ctx, agentUser, draftID, 1, "wrong", json.RawMessage(`{"bodyText":"Nope"}`)); err != errSharedDraftLease {
		t.Fatalf("wrong lease error = %v", err)
	}
	updated, err := server.updateSharedDraft(ctx, agentUser, draftID, 1, token, json.RawMessage(`{"subject":"Second","bodyText":"Two"}`))
	if err != nil || updated.Version != 2 || updated.Subject != "Second" {
		t.Fatalf("update = %#v err=%v", updated, err)
	}
	if _, err := server.updateSharedDraft(ctx, agentUser, draftID, 1, token, json.RawMessage(`{"bodyText":"Stale"}`)); err != errSharedDraftConflict {
		t.Fatalf("stale update error = %v", err)
	}
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.shared_draft_versions WHERE shared_draft_id=$1`, draftID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("version count = %d err=%v", versions, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.shared_drafts SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, draftID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.updateSharedDraft(ctx, agentUser, draftID, 2, token, json.RawMessage(`{"bodyText":"Expired"}`)); err != errSharedDraftLease {
		t.Fatalf("expired lease error = %v", err)
	}
}
