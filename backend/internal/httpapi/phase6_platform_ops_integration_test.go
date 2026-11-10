package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase6PlatformOpsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 6 ops test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4608-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4609-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "ops-" + suffix + "@gov.example.test"
	target := "target-" + suffix + "@gov.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 6 Ops')`, tenant, "phase6-ops-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Ops User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.outbound_events (tenant_id, user_id, from_email, recipients) VALUES ($1,$2,$3,2)`, tenant, user, email)
	mustExec(`INSERT INTO public.ai_usage (tenant_id, month, requests, cost_micros) VALUES ($1,date_trunc('month',now())::date,4,9000)
		ON CONFLICT (tenant_id, month) DO UPDATE SET requests=4, cost_micros=9000`, tenant)
	mustExec(`INSERT INTO public.tasks (id, tenant_id, user_id, title) VALUES ($1,$2,$3,'Ops task')`, "ops-task-"+suffix, tenant, user)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase6-ops-secret", PublicURL: "https://mail.example.test"},
		webhookAllowPrivate: true}
	telemetryStatus := 0
	server.webhookPost = func(ctx context.Context, endpoint string, headers map[string]string, body []byte) (int, error) {
		telemetryStatus++
		return http.StatusOK, nil
	}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Ops User", Role: "owner"},
		ActiveTenant: adminTenant{ID: tenant, Name: "Phase 6 Ops", Slug: "phase6-ops-" + suffix, Plan: "enterprise", Region: "EU-WEST"}}
	withAdmin := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	}
	call := func(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != "" {
			reader = bytes.NewBufferString(body)
		}
		request := httptest.NewRequest(method, path, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler(recorder, withAdmin(request))
		return recorder
	}
	recorder := call(server.adminUsageAnalytics, http.MethodGet, "/admin/api/usage/analytics", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"sentLast30Days":1`) || !strings.Contains(recorder.Body.String(), `"aiRequestsThisMonth":4`) {
		t.Fatalf("analytics = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = call(server.adminSupportBundle, http.MethodGet, "/admin/api/support-bundle", "")
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "phase6-ops-secret") || !strings.Contains(recorder.Body.String(), `"users":1`) {
		t.Fatalf("support bundle = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(server.adminUpdateTelemetry, http.MethodPut, "/admin/api/telemetry", `{"enabled":true,"endpoint":"https://telemetry.example.test/a8m"}`); recorder.Code != http.StatusOK {
		t.Fatalf("telemetry update = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call(server.adminSendTelemetry, http.MethodPost, "/admin/api/telemetry/send", `{}`); recorder.Code != http.StatusOK || telemetryStatus != 1 {
		t.Fatalf("telemetry send = %d %s status=%d", recorder.Code, recorder.Body.String(), telemetryStatus)
	}

	// Phishing simulation: create, launch, click and report.
	campaignBody := fmt.Sprintf(`{"name":"Q4 awareness","subject":"Invoice review","bodyText":"Open {{link}} to review","landingNote":"This was a test.","targets":["%s"]}`, target)
	recorder = call(server.adminCreatePhishingCampaign, http.MethodPost, "/admin/api/phishing/campaigns", campaignBody)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create campaign = %d %s", recorder.Code, recorder.Body.String())
	}
	var campaign map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &campaign)
	campaignID := stateString(campaign, "id")
	launch := httptest.NewRequest(http.MethodPost, "/admin/api/phishing/campaigns/"+campaignID+"/launch", nil)
	launch.SetPathValue("id", campaignID)
	recorder = httptest.NewRecorder()
	server.adminLaunchPhishingCampaign(recorder, withAdmin(launch))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"sent":1`) {
		t.Fatalf("launch = %d %s", recorder.Code, recorder.Body.String())
	}
	var outbox int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.outbox WHERE user_id=$1 AND subject='Invoice review'`, user).Scan(&outbox)
	if outbox != 1 {
		t.Fatalf("phishing outbox rows = %d", outbox)
	}
	var token string
	_ = pool.QueryRow(ctx, `SELECT token FROM public.phishing_targets WHERE campaign_id=$1`, campaignID).Scan(&token)
	public := httptest.NewRequest(http.MethodGet, "/phish/"+token, nil)
	public.SetPathValue("token", token)
	recorder = httptest.NewRecorder()
	server.publicPhishingPage(recorder, public)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Security awareness simulation") {
		t.Fatalf("public phish = %d %s", recorder.Code, recorder.Body.String())
	}
	reportRequest := httptest.NewRequest(http.MethodGet, "/phish/"+token+"/report", nil)
	reportRequest.SetPathValue("token", token)
	recorder = httptest.NewRecorder()
	server.publicPhishingPage(recorder, reportRequest)
	var targetStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.phishing_targets WHERE campaign_id=$1`, campaignID).Scan(&targetStatus)
	if targetStatus != "reported" {
		t.Fatalf("target status = %s", targetStatus)
	}
	report := httptest.NewRequest(http.MethodGet, "/admin/api/phishing/campaigns/"+campaignID+"/report", nil)
	report.SetPathValue("id", campaignID)
	recorder = httptest.NewRecorder()
	server.adminPhishingReport(recorder, withAdmin(report))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"reported":1`) {
		t.Fatalf("phishing report = %d %s", recorder.Code, recorder.Body.String())
	}
}
