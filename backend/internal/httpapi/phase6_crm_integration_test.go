package httpapi

import (
	"context"
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

func TestPhase6CRMSyncIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the Phase 6 CRM test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4604-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4605-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "crm-" + suffix + "@gov.example.test"
	alice := "alice-" + suffix + "@client.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Phase 6 CRM')`, tenant, "phase6-crm-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'CRM User','owner')`, user, tenant, email)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gov.example.test',$4,$5)`,
		tenant, user, email, "crm-"+suffix, t.TempDir())
	mustExec(`INSERT INTO public.mail_messages (id, tenant_id, user_id, mailbox_id, folder, thread_id, subject, from_email, body_text, sent_at)
		SELECT $1,$2,$3,m.id,'inbox','crm-thread','CRM context',$4,'Alice context',now()
		FROM public.mailboxes m WHERE m.user_id=$3`, "crm-msg-"+suffix, tenant, user, alice)
	server := &Server{store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{}, cfg: config.Config{SessionSecret: "phase6-crm-secret"}}
	sealed, err := server.sealWorkspaceToken(`{"accessToken":"pat-test"}`)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(`INSERT INTO public.addon_connections (id, tenant_id, addon_id, connection_scope, credentials_sealed, status)
		VALUES ($1,$2,'hubspot','tenant',$3,'active')`, "crm-conn-"+suffix, tenant, sealed)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/crm/v3/objects/contacts":
			_, _ = io.WriteString(w, `{"results":[{"id":"1","properties":{"email":"`+alice+`","firstname":"Alice","lastname":"Ng","company":"Acme","jobtitle":"CTO","lifecyclestage":"lead"}},{"id":"2","properties":{"email":"bob-`+suffix+`@client.example.test","firstname":"Bob","lastname":"Ray","company":"Beta","jobtitle":"Buyer"}}]}`)
		case "/crm/v3/objects/deals":
			_, _ = io.WriteString(w, `{"results":[{"id":"deal-1","properties":{"dealname":"Acme renewal","amount":"42000","dealstage":"negotiation"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	server.hubspotBaseURL = fake.URL
	var connectionCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.addon_connections WHERE tenant_id=$1 AND addon_id='hubspot' AND connection_scope='tenant' AND status='active'`, tenant).Scan(&connectionCount)
	if connectionCount != 1 {
		t.Fatalf("hubspot connection rows = %d", connectionCount)
	}
	if _, ok := server.addonActiveConnection(ctx, tenant, "hubspot", "", "tenant"); !ok {
		var id, owner, label, sealedValue string
		var raw []byte
		scanErr := pool.QueryRow(ctx, `
			SELECT id, COALESCE(owner_user_id::text, ''), account_label, credentials_sealed, config
			FROM addon_connections
			WHERE tenant_id = $1 AND addon_id = $2 AND connection_scope = $3
			  AND status = 'active' AND ($3 = 'tenant' OR owner_user_id = $4)
			ORDER BY updated_at DESC LIMIT 1
		`, tenant, "hubspot", "tenant", "").Scan(&id, &owner, &label, &sealedValue, &raw)
		t.Fatalf("addonActiveConnection did not find the HubSpot connection (scan err=%v id=%q sealed=%d)", scanErr, id, len(sealedValue))
	}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{ID: user, TenantID: tenant, Email: email, Name: "CRM User", Role: "owner"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/api/crm/sync?provider=hubspot", nil)
	request = request.WithContext(context.WithValue(request.Context(), adminSessionContextKey{}, adminSess))
	recorder := httptest.NewRecorder()
	server.adminSyncCRM(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"records":3`) {
		t.Fatalf("sync = %d %s", recorder.Code, recorder.Body.String())
	}
	var records, deals int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.crm_records WHERE tenant_id=$1`, tenant).Scan(&records)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM public.crm_records WHERE tenant_id=$1 AND object_type='deal'`, tenant).Scan(&deals)
	if records != 3 || deals != 1 {
		t.Fatalf("crm records=%d deals=%d", records, deals)
	}
	snapshot, err := server.senderSnapshotFor(ctx, tenant, user, alice)
	if err != nil || snapshot.CRMCompany != "Acme" || snapshot.CRMTitle != "CTO" {
		t.Fatalf("snapshot crm = %#v err=%v", snapshot, err)
	}
	recordsRequest := httptest.NewRequest(http.MethodGet, "/admin/api/crm/records?email="+alice, nil)
	recordsRequest = recordsRequest.WithContext(context.WithValue(recordsRequest.Context(), adminSessionContextKey{}, adminSess))
	recorder = httptest.NewRecorder()
	server.adminCRMRecords(recorder, recordsRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Acme") {
		t.Fatalf("records = %d %s", recorder.Code, recorder.Body.String())
	}
	var syncStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM public.crm_sync_state WHERE tenant_id=$1 AND provider='hubspot'`, tenant).Scan(&syncStatus)
	if syncStatus != "success" {
		t.Fatalf("sync status = %s", syncStatus)
	}
}
