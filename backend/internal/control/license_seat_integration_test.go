package control

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLicenseSeatUsageSQL is opt-in and destructive. Set
// CONTROL_LICENSE_TEST_DSN to a disposable PostgreSQL database whose name
// contains "test"; the test replaces public.tenants and public.users.
//
//	CONTROL_LICENSE_TEST_DSN='postgres://postgres:test@127.0.0.1:55432/license_test?sslmode=disable' \
//	  go test ./internal/control -run LicenseSeatUsageSQL -count=1 -v
func TestLicenseSeatUsageSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("CONTROL_LICENSE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set CONTROL_LICENSE_TEST_DSN to run the license seat SQL test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing to rewrite non-test database %q", database)
	}

	_, err = pool.Exec(ctx, `
		DROP TABLE IF EXISTS public.users CASCADE;
		DROP TABLE IF EXISTS public.tenants CASCADE;
		CREATE TABLE public.tenants (
			id uuid PRIMARY KEY,
			name text NOT NULL
		);
		CREATE TABLE public.users (
			id uuid PRIMARY KEY,
			tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			status text NOT NULL,
			deleted_at timestamptz,
			is_system_admin boolean NOT NULL DEFAULT false
		);
		INSERT INTO public.tenants (id, name) VALUES
			('11111111-1111-1111-1111-111111111111', 'Alpha'),
			('22222222-2222-2222-2222-222222222222', 'Beta'),
			('33333333-3333-3333-3333-333333333333', 'Gamma');
		INSERT INTO public.users (id, tenant_id, status, deleted_at, is_system_admin) VALUES
			('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1', '11111111-1111-1111-1111-111111111111', 'active',    NULL, false),
			('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2', '11111111-1111-1111-1111-111111111111', 'active',    NULL, false),
			('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3', '11111111-1111-1111-1111-111111111111', 'active',    NULL, true),
			('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa4', '11111111-1111-1111-1111-111111111111', 'suspended', NULL, false),
			('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa5', '11111111-1111-1111-1111-111111111111', 'active',    now(), false),
			('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1', '22222222-2222-2222-2222-222222222222', 'active',    NULL, false),
			('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2', '22222222-2222-2222-2222-222222222222', 'invited',   NULL, false);
	`)
	if err != nil {
		t.Fatal(err)
	}

	srv := licenseTestServer(t, Config{})
	srv.db = pool
	srv.seatCounter = nil

	used, byTenant, err := srv.licenseSeatUsage(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if used != 3 {
		t.Fatalf("global used = %d, want 3 (active non-deleted non-admin)", used)
	}
	if len(byTenant) != 3 {
		t.Fatalf("byTenant = %#v, want three tenants", byTenant)
	}
	want := []struct {
		name  string
		seats int
	}{{"Alpha", 2}, {"Beta", 1}, {"Gamma", 0}}
	for index, expected := range want {
		if byTenant[index]["name"] != expected.name || byTenant[index]["activeSeats"] != expected.seats {
			t.Fatalf("byTenant[%d] = %#v, want %s/%d", index, byTenant[index], expected.name, expected.seats)
		}
	}

	used, byTenant, err = srv.licenseSeatUsage(ctx, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if used != 2 || len(byTenant) != 0 {
		t.Fatalf("tenant-scoped usage = %d rows=%#v, want 2 and no rows", used, byTenant)
	}

	used, _, err = srv.licenseSeatUsage(ctx, "33333333-3333-3333-3333-333333333333")
	if err != nil || used != 0 {
		t.Fatalf("empty tenant usage = %d err=%v, want 0", used, err)
	}

	used, _, err = srv.licenseSeatUsage(ctx, "not-a-uuid")
	if err != nil || used != 0 {
		t.Fatalf("invalid tenant claim usage = %d err=%v, want 0", used, err)
	}
}
