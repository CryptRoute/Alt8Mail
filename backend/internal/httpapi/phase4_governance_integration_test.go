package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase4MCPGovernanceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 MCP governance test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4400-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4401-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	other := fmt.Sprintf("00000000-0000-4402-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	clientID := "mcpclient_phase4_" + suffix
	connectionID := "mcpconn_phase4_" + suffix
	addonID := "addon_crm_" + suffix
	email := "phase4-" + suffix + "@mcp.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 MCP')`, tenant, "phase4-mcp-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'MCP Owner','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Other User','member')`, other, tenant, "other-"+suffix+"@mcp.example.test")
	mustExec(`INSERT INTO public.mcp_clients (id, client_name, redirect_uris) VALUES ($1,'Phase 4 client',ARRAY['https://client.example/callback'])`, clientID)
	mustExec(`INSERT INTO public.mcp_connections (id, client_id, tenant_id, user_id, scopes) VALUES ($1,$2,$3,$4,ARRAY['tasks.write','calendar.write','crm.read'])`, connectionID, clientID, tenant, user)
	mustExec(`INSERT INTO public.addon_system_catalog (id, name, category, connection_scope, auth_type) VALUES ($1,'Phase CRM','CRM','user','api_key')`, addonID)
	mustExec(`INSERT INTO public.addon_installs (id, tenant_id, addon_id, user_id, status) VALUES ($1,$2,$3,$4,'active')`, "install_"+suffix, tenant, addonID, user)

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{},
		license: &licensing.License{Version: licensing.Version, ID: "mcp-governance-test", Edition: "enterprise", Seats: 10,
			Features: []string{FeatureMCPGovernance}, ExpiresAt: time.Now().Add(time.Hour)}}
	userValue := userRecord{ID: user, TenantID: tenant, Email: email, Name: "MCP Owner", Status: "active"}
	token := mcpTokenRecord{ConnectionID: connectionID, UserID: user, TenantID: tenant, User: userValue,
		Scopes: []string{"tasks.write", "calendar.write", "crm.read"}}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)

	dry, err := server.mcpRequestWrite(request, token, "tasks_write", map[string]any{"title": "Dry run task"}, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if stateString(dry, "structuredContent") == "" {
		t.Fatalf("dry run result = %#v", dry)
	}
	structured, _ := dry["structuredContent"].(map[string]any)
	if stateString(structured, "status") != "pending_approval" || stateString(structured, "dryRun") != "true" {
		t.Fatalf("dry run structured = %#v", structured)
	}
	var pending int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.mcp_action_requests WHERE user_id=$1`, user).Scan(&pending)
	if pending != 0 {
		t.Fatalf("dry run wrote %d rows", pending)
	}
	if _, err := server.mcpRequestWrite(request, token, "tasks_write", map[string]any{}, false); err == nil {
		t.Fatal("missing title accepted")
	}

	result, err := server.mcpRequestWrite(request, token, "tasks_write", map[string]any{"title": "Approve me", "priority": "high"}, false)
	if err != nil {
		t.Fatalf("request write: %v", err)
	}
	structured, _ = result["structuredContent"].(map[string]any)
	actionID := stateString(structured, "requestId")
	if actionID == "" {
		t.Fatalf("action result = %#v", result)
	}

	// A different user cannot see or decide this action.
	otherSession := &sessionRecord{UserID: other, TenantID: tenant, User: userRecord{ID: other, TenantID: tenant, Email: "other-" + suffix + "@mcp.example.test"}}
	otherRequest := httptest.NewRequest(http.MethodPost, "/api/mcp/action-requests/"+actionID+"/decide", strings.NewReader(`{"decision":"approve"}`))
	otherRequest.Header.Set("Content-Type", "application/json")
	otherRequest.SetPathValue("id", actionID)
	otherRequest = otherRequest.WithContext(context.WithValue(otherRequest.Context(), sessionContextKey{}, otherSession))
	recorder := httptest.NewRecorder()
	server.decideMCPAction(recorder, otherRequest)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-user decision status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	ownerSession := &sessionRecord{UserID: user, TenantID: tenant, User: userValue}
	approve := httptest.NewRequest(http.MethodPost, "/api/mcp/action-requests/"+actionID+"/decide", strings.NewReader(`{"decision":"approve"}`))
	approve.Header.Set("Content-Type", "application/json")
	approve.SetPathValue("id", actionID)
	approve = approve.WithContext(context.WithValue(approve.Context(), sessionContextKey{}, ownerSession))
	recorder = httptest.NewRecorder()
	server.decideMCPAction(recorder, approve)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"approved"`) {
		t.Fatalf("approve = %d %s", recorder.Code, recorder.Body.String())
	}
	var taskTitle string
	if err := pool.QueryRow(ctx, `SELECT title FROM public.tasks WHERE user_id=$1 AND source='mcp' ORDER BY created_at DESC LIMIT 1`, user).Scan(&taskTitle); err != nil || taskTitle != "Approve me" {
		t.Fatalf("task = %q err=%v", taskTitle, err)
	}

	denyResult, err := server.mcpRequestWrite(request, token, "tasks_write", map[string]any{"title": "Deny me"}, false)
	if err != nil {
		t.Fatalf("request deny: %v", err)
	}
	structured, _ = denyResult["structuredContent"].(map[string]any)
	denyID := stateString(structured, "requestId")
	deny := httptest.NewRequest(http.MethodPost, "/api/mcp/action-requests/"+denyID+"/decide", strings.NewReader(`{"decision":"deny","note":"not now"}`))
	deny.Header.Set("Content-Type", "application/json")
	deny.SetPathValue("id", denyID)
	deny = deny.WithContext(context.WithValue(deny.Context(), sessionContextKey{}, ownerSession))
	recorder = httptest.NewRecorder()
	server.decideMCPAction(recorder, deny)
	var status string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.mcp_action_requests WHERE id=$1`, denyID).Scan(&status)
	if recorder.Code != http.StatusOK || status != "denied" {
		t.Fatalf("deny = %d status=%s body=%s", recorder.Code, status, recorder.Body.String())
	}

	// Scope catalog and policy checks.
	tools := mcpToolsForScopes([]string{"mail.read"})
	for _, raw := range tools {
		if tool, _ := raw.(map[string]any); stateString(tool, "name") == "tasks_write" {
			t.Fatal("write tool exposed without a write scope")
		}
	}
	if mcpToolScope("draft_send") != "draft.send" || mcpToolScope("crm_read") != "crm.read" {
		t.Fatal("new scope mapping missing")
	}
	settings := mustJSON(map[string]any{"mcp": map[string]any{"allowedScopes": []string{"mail.read"}, "maxRequestsPerHour": 2, "writeApprovalRequired": true}})
	mustExec(`UPDATE public.tenants SET settings=$2 WHERE id=$1`, tenant, settings)
	if err := server.mcpCheckScopePolicy(ctx, tenant, []string{"tasks.write"}); err == nil {
		t.Fatal("organization scope policy did not reject tasks.write")
	}
	if err := server.mcpCheckScopePolicy(ctx, tenant, []string{"mail.read"}); err != nil {
		t.Fatalf("allowed scope rejected: %v", err)
	}

	// Rate limiting counts recent MCP audit rows.
	mustExec(`INSERT INTO public.audit_log (tenant_id, user_id, actor, action) VALUES ($1,$2,$3,'mcp.tool_called')`, tenant, user, email)
	mustExec(`INSERT INTO public.audit_log (tenant_id, user_id, actor, action) VALUES ($1,$2,$3,'mcp.tool_called')`, tenant, user, email)
	if allowed, err := server.mcpWithinRateLimit(ctx, tenant, user); err != nil || allowed {
		t.Fatalf("rate limit allowed=%v err=%v", allowed, err)
	}

	// CRM read only returns the tenant's active connectors.
	crm, err := server.mcpCallTool(httptest.NewRequest(http.MethodPost, "/mcp", nil), token, "crm_read", map[string]any{})
	if err != nil {
		t.Fatalf("crm read: %v", err)
	}
	crmText := fmt.Sprint(crm["content"])
	if !strings.Contains(crmText, "Phase CRM") {
		t.Fatalf("crm result = %#v", crm)
	}

	// Revocation removes the connection from the inventory.
	revoke := httptest.NewRequest(http.MethodDelete, "/api/mcp/connections/"+connectionID, nil)
	revoke.SetPathValue("id", connectionID)
	revoke = revoke.WithContext(context.WithValue(revoke.Context(), sessionContextKey{}, ownerSession))
	recorder = httptest.NewRecorder()
	server.revokeMCPConnection(recorder, revoke)
	var connectionStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.mcp_connections WHERE id=$1`, connectionID).Scan(&connectionStatus)
	if recorder.Code != http.StatusOK || connectionStatus != "revoked" {
		t.Fatalf("revoke = %d status=%s body=%s", recorder.Code, connectionStatus, recorder.Body.String())
	}
}

func TestPhase4AIBudgetIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 4 AI budget test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4403-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4404-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "budget-" + suffix + "@ai.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 4 Budget')`, tenant, "phase4-budget-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Budget User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.tenant_ai_settings (tenant_id, enabled, provider, endpoint, model, monthly_budget_cents)
		VALUES ($1,true,'openai','http://127.0.0.1:9','test-model',1)`, tenant)
	mustExec(`INSERT INTO public.ai_usage (tenant_id, month, requests, input_chars, output_chars, cost_micros)
		VALUES ($1, date_trunc('month', now())::date, 1, 10, 10, 10000)
		ON CONFLICT (tenant_id, month) DO UPDATE SET cost_micros=10000`, tenant)

	var hits int32
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello from the provider"}}]}`)
	}))
	defer aiServer.Close()

	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), stateLocks: map[string]*sync.Mutex{}}
	settings, err := server.aiSettingsFor(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	settings.Endpoint = aiServer.URL
	if _, err := server.callAI(ctx, settings, "system", "user", 64, 0); !errors.Is(err, errAIBudget) {
		t.Fatalf("exhausted budget err = %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("provider called %d times with an exhausted budget", hits)
	}

	mustExec(`UPDATE public.tenant_ai_settings SET monthly_budget_cents=0 WHERE tenant_id=$1`, tenant)
	reply, err := server.callAI(ctx, settings, "system", "user", 64, 0)
	if err != nil || reply != "hello from the provider" {
		t.Fatalf("callAI reply=%q err=%v", reply, err)
	}
	var requests, cost int64
	if err := pool.QueryRow(ctx, `SELECT requests, cost_micros FROM public.ai_usage WHERE tenant_id=$1 AND month=date_trunc('month', now())::date`, tenant).Scan(&requests, &cost); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || cost <= 10000 {
		t.Fatalf("usage requests=%d cost=%d", requests, cost)
	}
	snapshot, err := server.aiUsage(ctx, tenant)
	if err != nil || snapshot.Requests != 2 || snapshot.BudgetCents != 0 {
		t.Fatalf("snapshot = %#v err=%v", snapshot, err)
	}

	mustExec(`UPDATE public.tenant_ai_settings SET monthly_budget_cents=1 WHERE tenant_id=$1`, tenant)
	sess := &sessionRecord{UserID: user, TenantID: tenant, User: userRecord{ID: user, TenantID: tenant, Email: email}}
	usageRequest := httptest.NewRequest(http.MethodGet, "/api/ai/usage", nil)
	usageRequest = usageRequest.WithContext(context.WithValue(usageRequest.Context(), sessionContextKey{}, sess))
	recorder := httptest.NewRecorder()
	server.aiUsageHandler(recorder, usageRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"budgetExhausted":true`) {
		t.Fatalf("usage handler = %d %s", recorder.Code, recorder.Body.String())
	}
}
