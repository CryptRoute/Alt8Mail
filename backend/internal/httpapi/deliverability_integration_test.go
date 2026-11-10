package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sampleDMARC = `<?xml version="1.0"?>
<feedback>
  <report_metadata><org_name>Example Receiver</org_name><report_id>report-1</report_id><date_range><begin>1760000000</begin><end>1760086400</end></date_range></report_metadata>
  <policy_published><domain>example.test</domain><p>reject</p></policy_published>
  <record><row><source_ip>192.0.2.1</source_ip><count>10</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>pass</spf></policy_evaluated></row><identifiers><header_from>example.test</header_from></identifiers><auth_results><dkim><result>pass</result></dkim><spf><result>pass</result></spf></auth_results></record>
  <record><row><source_ip>192.0.2.2</source_ip><count>4</count><policy_evaluated><disposition>reject</disposition><dkim>fail</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>example.test</header_from></identifiers><auth_results><dkim><result>fail</result></dkim><spf><result>fail</result></spf></auth_results></record>
</feedback>`

func TestDMARCAndCampaignIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the deliverability integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4046-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4047-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	domain := "deliver-" + suffix + ".example.test"
	email := "deliver-" + suffix + "@deliver.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Deliverability Test')`, tenant, "deliver-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Deliverability User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mail_domains (tenant_id, domain, is_primary) VALUES ($1,$2,true)`, tenant, domain)
	server := &Server{
		store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{},
		license: &licensing.License{Version: licensing.Version, ID: "deliver-test", Edition: "enterprise", Seats: 10,
			Features: []string{"mail_merge"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	sample := strings.ReplaceAll(sampleDMARC, "1760000000", fmt.Sprintf("%d", time.Now().Add(-48*time.Hour).Unix()))
	sample = strings.ReplaceAll(sample, "1760086400", fmt.Sprintf("%d", time.Now().Add(-24*time.Hour).Unix()))
	sample = strings.ReplaceAll(sample, "example.test", domain)
	feedback, err := parseDMARCReport([]byte(sample))
	if err != nil || len(feedback.Records) != 2 {
		t.Fatalf("parse = %#v err=%v", feedback, err)
	}
	first, err := server.ingestDMARCReport(ctx, tenant, []byte(sample))
	if err != nil || first.Messages != 14 || first.Pass != 10 || first.Fail != 4 {
		t.Fatalf("ingest = %#v err=%v", first, err)
	}
	duplicate, err := server.ingestDMARCReport(ctx, tenant, []byte(sample))
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("duplicate = %#v err=%v", duplicate, err)
	}
	adminSession := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "Deliverability User"}}
	request := httptest.NewRequest(http.MethodGet, "/dmarc/summary?days=90", nil)
	request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSession))
	recorder := httptest.NewRecorder()
	server.adminDMARCSummary(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"messages":14`) || !strings.Contains(recorder.Body.String(), `"fail":4`) {
		t.Fatalf("summary = %d %s", recorder.Code, recorder.Body.String())
	}

	// Campaign tracking is opt-in and wraps links plus a transparent pixel.
	userRecord := userRecord{ID: user, TenantID: tenant, Email: email, Name: "Deliverability User", Status: "active"}
	htmlBody, trackingID, err := server.addCampaignTracking(ctx, userRecord, "", "", "recipient@example.test", `<p><a href="https://example.com/offer">Offer</a></p>`, "https://mail.example.test")
	if err != nil || trackingID == "" || !strings.Contains(htmlBody, "/t/o/") || !strings.Contains(htmlBody, "/t/c/") {
		t.Fatalf("tracking html = %q id=%q err=%v", htmlBody, trackingID, err)
	}
	openToken := regexp.MustCompile(`/t/o/([^./]+)\.gif`).FindStringSubmatch(htmlBody)
	clickToken := regexp.MustCompile(`/t/c/([^"]+)`).FindStringSubmatch(htmlBody)
	if len(openToken) != 2 || len(clickToken) != 2 {
		t.Fatalf("tracking tokens = %#v %#v", openToken, clickToken)
	}
	openRequest := httptest.NewRequest(http.MethodGet, "/t/o/"+openToken[1]+".gif", nil)
	openRequest.SetPathValue("token", openToken[1])
	recorder = httptest.NewRecorder()
	server.recordCampaignOpen(recorder, openRequest)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/gif" {
		t.Fatalf("open = %d %s", recorder.Code, recorder.Body.String())
	}
	clickRequest := httptest.NewRequest(http.MethodGet, "/t/c/"+clickToken[1], nil)
	clickRequest.SetPathValue("token", clickToken[1])
	recorder = httptest.NewRecorder()
	server.recordCampaignClick(recorder, clickRequest)
	if recorder.Code != http.StatusTemporaryRedirect || recorder.Header().Get("Location") != "https://example.com/offer" {
		t.Fatalf("click = %d %s", recorder.Code, recorder.Body.String())
	}
	var opens, clicks int
	if err := pool.QueryRow(ctx, `SELECT open_count, click_count FROM public.campaign_tracking WHERE id=$1`, trackingID).Scan(&opens, &clicks); err != nil || opens != 1 || clicks != 1 {
		t.Fatalf("tracking counts = %d/%d err=%v", opens, clicks, err)
	}
}
