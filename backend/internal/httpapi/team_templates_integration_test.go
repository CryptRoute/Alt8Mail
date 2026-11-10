package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTeamTemplateVariablesAndEscaping(t *testing.T) {
	variables := extractTeamTemplateVariables("Hi {{name}}", "Order {{ order_id }} for {{name}}", "<p>{{name}}</p>")
	if len(variables) != 2 || variables[0] != "name" || variables[1] != "order_id" {
		t.Fatalf("variables = %#v", variables)
	}
	if got := htmlEscapeTemplateValue(`<b>"x"&</b>`); got != "&lt;b&gt;&quot;x&quot;&amp;&lt;/b&gt;" {
		t.Fatalf("html escape = %q", got)
	}
}

// TestTeamTemplateIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0066_team_templates.
func TestTeamTemplateIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the team template integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4019-8000-%012d", time.Now().UnixNano()%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4020-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	agent := fmt.Sprintf("00000000-0000-4021-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	outsider := fmt.Sprintf("00000000-0000-4022-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	group := fmt.Sprintf("00000000-0000-4023-8000-%012d", (time.Now().UnixNano()+4)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@template.example.test" }
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Template Test')`, tenant, "template-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES
		($1,$2,$3,'Owner','owner'), ($4,$2,$5,'Agent','member'), ($6,$2,$7,'Outsider','member')`,
		owner, tenant, email("owner"), agent, email("agent"), outsider, email("outsider"))
	mustExec(`INSERT INTO public.groups (id, tenant_id, name, email, group_type) VALUES ($1,$2,'Template Team',$3,'shared')`, group, tenant, email("team"))
	mustExec(`INSERT INTO public.group_members (group_id, user_id, member_role) VALUES ($1,$2,'owner')`, group, agent)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'template.example.test',$4,$5)`,
		tenant, owner, email("owner"), "owner-"+suffix, t.TempDir())
	teamID := ""
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes (tenant_id, name, address, owner_user_id, group_id)
		VALUES ($1,'Templates',$2,$3,$4) RETURNING id::text`, tenant, email("team"), owner, group).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	templateID := "template_" + suffix
	mustExec(`INSERT INTO public.team_templates
		(id, tenant_id, team_mailbox_id, name, category, subject, body_text, body_html, variables, created_by, updated_by)
		VALUES ($1,$2,$3,'Welcome','Onboarding','Hello {{name}}','Hi {{name}},','<p>Hi {{name}}</p>',ARRAY['name'],$4,$4)`,
		templateID, tenant, teamID, agent)
	mustExec(`INSERT INTO public.team_template_versions (template_id, version, snapshot, author_user_id)
		VALUES ($1,1,'{"name":"Welcome"}'::jsonb,$2)`, templateID, agent)
	server := &Server{
		store: &store.Store{DB: pool},
		license: &licensing.License{Version: licensing.Version, ID: "template-test", Edition: "enterprise", Seats: 10,
			Features: []string{"team_templates"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	agentUser := userRecord{ID: agent, TenantID: tenant, Email: email("agent"), Name: "Agent", Status: "active", Role: "member"}
	outsiderUser := userRecord{ID: outsider, TenantID: tenant, Email: email("outsider"), Name: "Outsider", Status: "active", Role: "member"}
	team, err := server.teamMailboxForUser(ctx, agentUser, teamID)
	if err != nil {
		t.Fatal(err)
	}
	if !server.teamCanManage(ctx, team, agentUser) {
		t.Fatal("group owner cannot manage templates")
	}
	if server.teamCanManage(ctx, team, outsiderUser) {
		t.Fatal("non-member can manage templates")
	}
	if _, _, err := server.teamTemplateForUser(ctx, outsiderUser, templateID); err == nil {
		t.Fatal("non-member can read a template")
	}
	record, _, err := server.teamTemplateForUser(ctx, agentUser, templateID)
	if err != nil || record.Name != "Welcome" || len(record.Variables) != 1 {
		t.Fatalf("template = %#v err=%v", record, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.team_templates SET body_text='Updated', version=version+1 WHERE id=$1`, templateID); err != nil {
		t.Fatal(err)
	}
	server.snapshotTeamTemplate(ctx, templateID, agent)
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.team_template_versions WHERE template_id=$1`, templateID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("template versions = %d err=%v", versions, err)
	}
}
