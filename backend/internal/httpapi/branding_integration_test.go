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

// TestBrandingHostIntegration requires A8M_SCIM_TEST_DSN with all migrations
// applied (including 0062_tenant_webmail_hosts).
func TestBrandingHostIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the white-label host integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	hostA := "brand-a-" + suffix + ".example.test"
	hostB := "brand-b-" + suffix + ".example.test"
	hostC := "brand-c-" + suffix + ".example.test"
	slugA := "brandingtest-a-" + suffix
	slugB := "brandingtest-b-" + suffix
	tenantA := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1000000000000)
	tenantB := fmt.Sprintf("00000000-0000-4001-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM control.webnet_routes WHERE lower(hostname::text) IN (lower($1), lower($2), lower($3))`, hostA, hostB, hostC)
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE slug IN ($1, $2)`, slugA, slugB)
	}
	defer cleanup()
	cleanup()

	settingsA := []byte(`{"branding":{"productName":"Alpha","webmailHosts":["` + hostA + `"],"logoUrl":"https://cdn.example.test/alpha.png","hideAttribution":true}}`)
	settingsB := []byte(`{"branding":{"productName":"Beta"}}`)
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name, status, settings) VALUES ($1,$2,'Alpha','active',$3)`, tenantA, slugA, settingsA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name, status, settings) VALUES ($1,$2,'Beta','active',$3)`, tenantB, slugB, settingsB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.mail_domains (tenant_id, domain, is_primary, status) VALUES ($1,$2,true,'verified')`, tenantB, hostB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO control.webnet_routes (service, hostname, certificate_name, enabled) VALUES ('webmail',$1,'brand-cert',true)`, hostA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenant_webmail_hosts (hostname, tenant_id) VALUES ($1,$2)`, hostA, tenantA); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		store: &store.Store{DB: pool},
		license: &licensing.License{
			Version: licensing.Version, ID: "lic_branding_integration", Edition: "enterprise",
			Seats: 10, Features: []string{"white_label"}, ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	policy, found := s.tenantBrandingForHost(ctx, strings.ToUpper(hostA)+":443")
	if !found || policy.Branding.ProductName != "Alpha" || policy.Branding.LogoURL == "" {
		t.Fatalf("custom-host branding = %#v found=%v", policy.Branding, found)
	}
	policy, found = s.tenantBrandingForHost(ctx, hostB)
	if !found || policy.Branding.ProductName != "Beta" {
		t.Fatalf("mail-domain fallback = %#v found=%v", policy.Branding, found)
	}
	if _, found := s.tenantBrandingForHost(ctx, hostC); found {
		t.Fatal("unknown host resolved to a tenant")
	}

	if errs := s.validateBrandingHostsDB(ctx, tenantA, []string{hostA}); len(errs) != 0 {
		t.Fatalf("valid host rejected: %v", errs)
	}
	if errs := s.validateBrandingHostsDB(ctx, tenantB, []string{hostA}); len(errs) == 0 {
		t.Fatal("cross-tenant hostname was accepted")
	}
	if errs := s.validateBrandingHostsDB(ctx, tenantA, []string{hostC}); len(errs) == 0 {
		t.Fatal("missing Web Net route was accepted")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.syncTenantWebmailHosts(ctx, tx, tenantA, []string{hostA}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRow(ctx, `SELECT tenant_id::text FROM public.tenant_webmail_hosts WHERE hostname=$1`, hostA).Scan(&owner); err != nil || owner != tenantA {
		t.Fatalf("host sync owner = %q err=%v", owner, err)
	}
}
