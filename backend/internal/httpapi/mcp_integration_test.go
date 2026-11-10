package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMCPHelpers(t *testing.T) {
	if _, err := mcpScopeSet("mail.read nope"); err == nil {
		t.Fatal("unknown scope accepted")
	}
	scopes, err := mcpScopeSet("mail.read mail.search mail.read")
	if err != nil || len(scopes) != 2 {
		t.Fatalf("scopes = %#v err=%v", scopes, err)
	}
	if !validMCPRedirect("https://client.example/callback") || !validMCPRedirect("http://127.0.0.1:9999/cb") {
		t.Fatal("valid redirect rejected")
	}
	if validMCPRedirect("http://evil.example/callback") || validMCPRedirect("https://client.example/cb#fragment") {
		t.Fatal("unsafe redirect accepted")
	}
	tools := mcpToolsForScopes([]string{"mail.search"})
	tool, _ := tools[0].(map[string]any)
	if len(tools) != 1 || stateString(tool, "name") != "mail_search" {
		t.Fatalf("tools = %#v", tools)
	}
	if mcpHash("a") == "a" || mcpToolScope("mail_draft") != "mail.draft" {
		t.Fatal("MCP hash/scope mapping is wrong")
	}
}

func TestMCPOAuthTokenIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the MCP OAuth integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4030-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4031-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	clientID := "mcpclient_oauth_" + suffix
	connectionID := "mcpconn_oauth_" + suffix
	code := "mcpcode_" + suffix
	verifier := strings.Repeat("a", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'MCP OAuth Test')`, tenant, "mcp-oauth-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'OAuth User','member')`, user, tenant, "oauth-"+suffix+"@mcp.example.test")
	mustExec(`INSERT INTO public.mcp_clients (id, client_name, redirect_uris) VALUES ($1,'OAuth client',ARRAY['https://client.example/callback'])`, clientID)
	mustExec(`INSERT INTO public.mcp_connections (id, client_id, tenant_id, user_id, scopes) VALUES ($1,$2,$3,$4,ARRAY['mail.search'])`, connectionID, clientID, tenant, user)
	mustExec(`INSERT INTO public.mcp_authorization_codes (code_hash, connection_id, user_id, tenant_id, scopes, redirect_uri, code_challenge, expires_at)
		VALUES ($1,$2,$3,$4,ARRAY['mail.search'],'https://client.example/callback',$5,now()+interval '5 minutes')`, mcpHash(code), connectionID, user, tenant, challenge)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mcpLimit: newLoginLimiter(120, time.Minute), stateLocks: map[string]*sync.Mutex{}}
	exchange := func(codeValue, verifierValue string) *httptest.ResponseRecorder {
		form := url.Values{
			"grant_type": {"authorization_code"}, "code": {codeValue}, "code_verifier": {verifierValue},
			"client_id": {clientID}, "redirect_uri": {"https://client.example/callback"},
		}
		request := httptest.NewRequest(http.MethodPost, "/mcp/oauth/token", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		server.mcpOAuthToken(recorder, request)
		return recorder
	}
	if recorder := exchange(code, strings.Repeat("b", 64)); recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong verifier status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder := exchange(code, verifier)
	if recorder.Code != http.StatusOK {
		t.Fatalf("token status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	if stateString(response, "access_token") == "" || stateString(response, "scope") != "mail.search" {
		t.Fatalf("token response = %#v", response)
	}
	if replay := exchange(code, verifier); replay.Code != http.StatusBadRequest {
		t.Fatalf("code replay status = %d", replay.Code)
	}
}

// TestMCPJSONRPCIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0069_mcp_seed.
func TestMCPJSONRPCIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the MCP integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4028-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4029-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	clientID := "mcpclient_" + suffix
	connectionID := "mcpconn_" + suffix
	token := "mcptoken_" + suffix
	email := "mcp-" + suffix + "@mcp.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'MCP Test')`, tenant, "mcp-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'MCP User','member')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'mcp.example.test',$4,$5)`,
		tenant, user, email, "mcp-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_name, from_email, snippet, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','mcp-thread','Quarterly plan','Alice','alice@example.test','Please review the plan','The plan is attached.',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "mcpmsg_"+suffix, tenant, user)
	mustExec(`INSERT INTO public.mcp_clients (id, client_name, redirect_uris) VALUES ($1,'Test client',ARRAY['https://client.example/callback'])`, clientID)
	mustExec(`INSERT INTO public.mcp_connections (id, client_id, tenant_id, user_id, scopes) VALUES ($1,$2,$3,$4,ARRAY['mail.read','mail.search'])`, connectionID, clientID, tenant, user)
	mustExec(`INSERT INTO public.mcp_tokens (token_hash, connection_id, user_id, tenant_id, scopes, expires_at) VALUES ($1,$2,$3,$4,ARRAY['mail.read','mail.search'],now()+interval '1 hour')`, mcpHash(token), connectionID, user, tenant)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mcpLimit: newLoginLimiter(120, time.Minute), stateLocks: map[string]*sync.Mutex{}}

	call := func(method string, params any, rawToken string) map[string]any {
		payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
		if params != nil {
			payload["params"] = params
		}
		body, _ := json.Marshal(payload)
		request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+rawToken)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.mcpJSONRPC(recorder, request)
		var response map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return response
	}
	init := call("initialize", nil, token)
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] == nil || !strings.Contains(fmt.Sprint(result["instructions"]), "untrusted") {
		t.Fatalf("initialize = %#v", init)
	}
	list := call("tools/list", nil, token)
	result, _ = list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", tools)
	}
	search := call("tools/call", map[string]any{"name": "mail_search", "arguments": map[string]any{"query": "Quarterly"}}, token)
	result, _ = search["result"].(map[string]any)
	if result["isError"] == true || !strings.Contains(fmt.Sprint(result["content"]), "mcpmsg_"+suffix) {
		t.Fatalf("search = %#v", search)
	}
	meta, _ := result["_meta"].(map[string]any)
	if meta["untrustedContent"] != true {
		t.Fatalf("search meta = %#v", meta)
	}
	denied := call("tools/call", map[string]any{"name": "mail_read", "arguments": map[string]any{"messageId": "mcpmsg_" + suffix}}, "mcptoken_missing")
	if denied["error"] == nil {
		t.Fatalf("invalid token was accepted: %#v", denied)
	}
	// Remove mail.read from the token and prove per-tool scope denial.
	mustExec(`UPDATE public.mcp_tokens SET scopes=ARRAY['mail.search'] WHERE token_hash=$1`, mcpHash(token))
	denied = call("tools/call", map[string]any{"name": "mail_read", "arguments": map[string]any{"messageId": "mcpmsg_" + suffix}}, token)
	errObj, _ := denied["error"].(map[string]any)
	if errObj == nil || !strings.Contains(fmt.Sprint(errObj["message"]), "mail.read") {
		t.Fatalf("scope denial = %#v", denied)
	}
	mustExec(`UPDATE public.mcp_tokens SET scopes=ARRAY['mail.draft'] WHERE token_hash=$1`, mcpHash(token))
	draft := call("tools/call", map[string]any{"name": "mail_draft", "arguments": map[string]any{
		"to": "customer@example.test", "subject": "MCP draft", "bodyText": "Prepared by MCP",
	}}, token)
	result, _ = draft["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	draftID := stateString(structured, "draftId")
	if draftID == "" {
		t.Fatalf("draft = %#v", draft)
	}
	var folder string
	var draftCount int
	if err := pool.QueryRow(ctx, `SELECT folder FROM public.mail_messages WHERE id=$1`, draftID).Scan(&folder); err != nil || folder != "drafts" {
		t.Fatalf("draft folder = %q err=%v", folder, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE user_id=$1`, user).Scan(&draftCount); err != nil || draftCount != 0 {
		t.Fatalf("MCP draft created outbox rows: %d err=%v", draftCount, err)
	}
	mustExec(`UPDATE public.mcp_connections SET status='revoked' WHERE id=$1`, connectionID)
	revoked := call("tools/list", nil, token)
	if revoked["error"] == nil {
		t.Fatalf("revoked connection token was accepted: %#v", revoked)
	}
}
