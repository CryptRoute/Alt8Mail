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

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGalleryAndPaymentIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the gallery integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4043-8000-%012d", time.Now().UnixNano()%1000000000000)
	owner := fmt.Sprintf("00000000-0000-4044-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	group := fmt.Sprintf("00000000-0000-4045-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	email := func(prefix string) string { return prefix + "-" + suffix + "@gallery.example.test" }
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name, settings) VALUES ($1,$2,'Gallery Test','{"mail":{"paymentLinksEnabled":true,"paymentLinkDomains":["buy.stripe.com"]}}'::jsonb)`, tenant, "gallery-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Gallery Owner','owner')`, owner, tenant, email("owner"))
	mustExec(`INSERT INTO public.groups (id, tenant_id, name, email, group_type) VALUES ($1,$2,'Gallery Team',$3,'shared')`, group, tenant, email("team"))
	mustExec(`INSERT INTO public.group_members (group_id, user_id, member_role) VALUES ($1,$2,'owner')`, group, owner)
	mustExec(`INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path) VALUES ($1,$2,$3,'gallery.example.test',$4,$5)`,
		tenant, owner, email("owner"), "owner-"+suffix, t.TempDir())
	teamID := ""
	if err := pool.QueryRow(ctx, `INSERT INTO public.team_mailboxes (tenant_id, name, address, owner_user_id, group_id)
		VALUES ($1,'Gallery',$2,$3,$4) RETURNING id::text`, tenant, email("team"), owner, group).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{},
		license: &licensing.License{Version: licensing.Version, ID: "gallery-test", Edition: "enterprise", Seats: 10,
			Features: []string{"team_templates"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	user := userRecord{ID: owner, TenantID: tenant, Email: email("owner"), Name: "Gallery Owner", Role: "owner", Status: "active"}
	withSession := func(request *http.Request) *http.Request {
		return request.WithContext(context.WithValue(request.Context(), sessionContextKey{}, &sessionRecord{UserID: owner, TenantID: tenant, User: user}))
	}
	recorder := httptest.NewRecorder()
	server.listTemplateGallery(recorder, withSession(httptest.NewRequest(http.MethodGet, "/api/template-gallery", nil)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "gallery-client-intro") {
		t.Fatalf("gallery = %d %s", recorder.Code, recorder.Body.String())
	}
	installRequest := httptest.NewRequest(http.MethodPost, "/api/template-gallery/gallery-client-intro/install", bytes.NewBufferString(`{"teamId":"`+teamID+`"}`))
	installRequest.Header.Set("Content-Type", "application/json")
	installRequest.SetPathValue("id", "gallery-client-intro")
	recorder = httptest.NewRecorder()
	server.installGalleryTemplate(recorder, withSession(installRequest))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("install = %d %s", recorder.Code, recorder.Body.String())
	}
	repeatRequest := httptest.NewRequest(http.MethodPost, "/api/template-gallery/gallery-client-intro/install", bytes.NewBufferString(`{"teamId":"`+teamID+`"}`))
	repeatRequest.Header.Set("Content-Type", "application/json")
	repeatRequest.SetPathValue("id", "gallery-client-intro")
	recorder = httptest.NewRecorder()
	server.installGalleryTemplate(recorder, withSession(repeatRequest))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate install = %d %s", recorder.Code, recorder.Body.String())
	}
	paymentBody, _ := json.Marshal(map[string]any{"url": "https://buy.stripe.com/test_link"})
	paymentRequest := httptest.NewRequest(http.MethodPost, "/api/payment-links/validate", bytes.NewReader(paymentBody))
	paymentRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.validatePaymentLink(recorder, withSession(paymentRequest))
	if recorder.Code != http.StatusOK {
		t.Fatalf("payment link = %d %s", recorder.Code, recorder.Body.String())
	}
	badBody, _ := json.Marshal(map[string]any{"url": "https://evil.example/pay"})
	badRequest := httptest.NewRequest(http.MethodPost, "/api/payment-links/validate", bytes.NewReader(badBody))
	badRequest.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.validatePaymentLink(recorder, withSession(badRequest))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("blocked payment host = %d %s", recorder.Code, recorder.Body.String())
	}
}
