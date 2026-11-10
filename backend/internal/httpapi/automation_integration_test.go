package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAutomationValidationAndSieve(t *testing.T) {
	conditions, err := validateAutomationConditions(json.RawMessage(`{"subjectContains":"urgent","hasAttachment":true}`))
	if err != nil || conditions.SubjectContains != "urgent" || conditions.HasAttachment == nil || !*conditions.HasAttachment {
		t.Fatalf("conditions = %#v err=%v", conditions, err)
	}
	if _, err := validateAutomationConditions(json.RawMessage(`{"unknown":"x"}`)); err == nil {
		t.Fatal("unknown condition was accepted")
	}
	actions, err := validateAutomationActions(json.RawMessage(`[{"type":"webhook","url":"http://insecure.example"},{"type":"createTask","title":"Review"}]`))
	if err == nil {
		t.Fatal("insecure webhook was accepted")
	}
	actions, err = validateAutomationActions(json.RawMessage(`[{"type":"move","folder":"Archive"},{"type":"createTask","title":"Review"}]`))
	if err != nil || len(actions) != 2 || actions[1].Priority != "normal" {
		t.Fatalf("actions = %#v err=%v", actions, err)
	}
	sieve := compileAutomationSieve(automationConditions{SubjectContains: "urgent"}, actions)
	if !strings.Contains(sieve, `fileinto "Archive"`) || !strings.Contains(sieve, `header :contains "subject" "urgent"`) {
		t.Fatalf("sieve = %q", sieve)
	}
}

// TestAutomationIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0067_automation.
func TestAutomationIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the automation integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4024-8000-%012d", time.Now().UnixNano()%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4025-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	agent := fmt.Sprintf("00000000-0000-4026-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	group := fmt.Sprintf("00000000-0000-4027-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@automation.example.test" }
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Automation Test')`, tenant, "automation-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES
		($1,$2,$3,'Owner','owner'), ($4,$2,$5,'Agent','member')`, owner, tenant, email("owner"), agent, email("agent"))
	mustExec(`INSERT INTO public.groups (id, tenant_id, name, email, group_type) VALUES ($1,$2,'Automation Team',$3,'shared')`, group, tenant, email("team"))
	mustExec(`INSERT INTO public.group_members (group_id, user_id, member_role) VALUES ($1,$2,'owner')`, group, agent)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'automation.example.test',$4,$5)`,
		tenant, owner, email("owner"), "owner-"+suffix, t.TempDir())
	teamID := ""
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes (tenant_id, name, address, owner_user_id, group_id)
		VALUES ($1,'Automation',$2,$3,$4) RETURNING id::text`, tenant, email("team"), owner, group).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	messageID := "msg_" + suffix
	mustExec(`INSERT INTO public.mail_messages
		(id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, to_text, snippet, sent_at, categories)
		SELECT $1,$2,$3,m.id,'inbox','thread-auto','Urgent renewal','customer@example.test','team@example.test','Please renew',now(),'{}'::text[]
		FROM public.mailboxes m WHERE m.user_id=$3`, messageID, tenant, owner)
	server := &Server{
		store: &store.Store{DB: pool},
		license: &licensing.License{Version: licensing.Version, ID: "automation-test", Edition: "enterprise", Seats: 10,
			Features: []string{"automation"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	var webhookCalls int32
	var webhookBody []byte
	server.automationPost = func(_ context.Context, _ string, payload []byte) error {
		atomic.AddInt32(&webhookCalls, 1)
		webhookBody = append([]byte(nil), payload...)
		return nil
	}
	team, err := server.teamMailboxRecord(ctx, tenant, teamID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := server.automationMessageByID(ctx, tenant, owner, messageID)
	if err != nil {
		t.Fatal(err)
	}
	rule := automationRuleRecord{
		ID: "rule_" + suffix, TenantID: tenant, TeamID: teamID, Name: "Urgent route", Enabled: true,
		Conditions: automationConditions{SubjectContains: "urgent"},
		Actions: []automationAction{
			{Type: "label", Value: "urgent"},
			{Type: "createTask", Title: "Handle urgent renewal", Priority: "high"},
			{Type: "webhook", URL: "https://hooks.example.test/automation"},
		},
	}
	mustExec(`INSERT INTO public.automation_rules
		(id, tenant_id, team_mailbox_id, name, conditions, actions, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7,$7)`,
		rule.ID, tenant, teamID, rule.Name, `{"subjectContains":"urgent"}`,
		`[{"type":"label","value":"urgent"},{"type":"createTask","title":"Handle urgent renewal","priority":"high"},{"type":"webhook","url":"https://hooks.example.test/automation"}]`, agent)
	rule, err = server.automationRuleRecord(ctx, tenant, teamID, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.evaluateAutomationRule(ctx, team, rule, message, false)
	if err != nil || result["matched"] != true {
		t.Fatalf("evaluation = %#v err=%v", result, err)
	}
	var tasks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.tasks WHERE source='automation' AND linked_thread_id='thread-auto'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("automation tasks = %d err=%v", tasks, err)
	}
	var categories []string
	if err := pool.QueryRow(ctx, `SELECT categories FROM public.mail_messages WHERE id=$1`, messageID).Scan(&categories); err != nil || len(categories) != 1 || categories[0] != "urgent" {
		t.Fatalf("categories = %#v err=%v", categories, err)
	}
	if atomic.LoadInt32(&webhookCalls) != 1 || !strings.Contains(string(webhookBody), messageID) {
		t.Fatalf("webhook calls = %d body=%s", webhookCalls, webhookBody)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.automation_runs WHERE rule_id=$1 AND status='completed'`, rule.ID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("completed runs = %d err=%v", runs, err)
	}
	// Re-evaluation is idempotent: no second task, label append or webhook.
	if _, err := server.evaluateAutomationRule(ctx, team, rule, message, false); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&webhookCalls) != 1 {
		t.Fatal("duplicate evaluation fired the webhook twice")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.tasks WHERE source='automation' AND linked_thread_id='thread-auto'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("duplicate tasks = %d err=%v", tasks, err)
	}
}
