package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSCIMRoleRecomputeIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the SCIM role recompute test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := fmt.Sprintf("00000000-0000-6101-8000-%012d", time.Now().UnixNano()%1000000000000)
	ownerID := fmt.Sprintf("00000000-0000-6102-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	memberID := fmt.Sprintf("00000000-0000-6103-8000-%012d", (time.Now().UnixNano()+2)%1000000000000)
	manualID := fmt.Sprintf("00000000-0000-6104-8000-%012d", (time.Now().UnixNano()+3)%1000000000000)
	groupID := fmt.Sprintf("00000000-0000-6105-8000-%012d", (time.Now().UnixNano()+4)%1000000000000)
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenantID) }()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO tenants (id, slug, name, status) VALUES ($1,$2,'SCIM Roles','active')`, tenantID, "scim-roles-"+suffix)
	mustExec(`INSERT INTO users (id, tenant_id, email, name, role, role_source) VALUES ($1,$2,$3,'Owner','owner','manual')`,
		ownerID, tenantID, "owner-"+suffix+"@roles.example.test")
	mustExec(`INSERT INTO users (id, tenant_id, email, name, role, role_source) VALUES ($1,$2,$3,'Member','member','manual')`,
		memberID, tenantID, "member-"+suffix+"@roles.example.test")
	mustExec(`INSERT INTO users (id, tenant_id, email, name, role, role_source) VALUES ($1,$2,$3,'Manual','readonly','manual')`,
		manualID, tenantID, "manual-"+suffix+"@roles.example.test")
	mustExec(`INSERT INTO groups (id, tenant_id, name, email, group_type, status, scim_role) VALUES ($1,$2,'Engineering',$3,'security','active','admin')`,
		groupID, tenantID, "group-"+suffix+"@roles.example.test")

	server := &Server{store: &store.Store{DB: pool}}
	recompute := func() []scimRoleChange {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		changes, err := server.scimRecomputeRolesTx(ctx, tx, tenantID, []string{ownerID, memberID, manualID})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return changes
	}
	roleOf := func(userID string) (string, string) {
		t.Helper()
		var role, source string
		if err := pool.QueryRow(ctx, `SELECT role, role_source FROM users WHERE id=$1`, userID).Scan(&role, &source); err != nil {
			t.Fatal(err)
		}
		return role, source
	}

	// A mapped group promotes its member and records the change. Owners are
	// never touched even if the group mapping applies to them.
	mustExec(`INSERT INTO group_members (group_id, user_id) VALUES ($1,$2)`, groupID, memberID)
	changes := recompute()
	if len(changes) != 1 || changes[0].UserID != memberID || changes[0].OldRole != "member" || changes[0].NewRole != "admin" {
		t.Fatalf("promotion changes = %#v", changes)
	}
	if role, source := roleOf(memberID); role != "admin" || source != "scim" {
		t.Fatalf("member role = %q source = %q", role, source)
	}
	if role, source := roleOf(ownerID); role != "owner" || source != "manual" {
		t.Fatalf("owner role = %q source = %q", role, source)
	}

	// Removing the mapping resets a SCIM-managed role to member/manual but
	// leaves an explicitly manual role alone.
	mustExec(`DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, memberID)
	changes = recompute()
	if len(changes) != 1 || changes[0].NewRole != "" {
		t.Fatalf("reset changes = %#v", changes)
	}
	if role, source := roleOf(memberID); role != "member" || source != "manual" {
		t.Fatalf("reset role = %q source = %q", role, source)
	}
	if role, source := roleOf(manualID); role != "readonly" || source != "manual" {
		t.Fatalf("manual role = %q source = %q", role, source)
	}

	// An explicit mapping still wins over a manually assigned role.
	mustExec(`UPDATE groups SET scim_role='readonly' WHERE id=$1`, groupID)
	mustExec(`INSERT INTO group_members (group_id, user_id) VALUES ($1,$2)`, groupID, manualID)
	recompute()
	if role, source := roleOf(manualID); role != "readonly" || source != "scim" {
		t.Fatalf("mapped manual role = %q source = %q", role, source)
	}

	// The group must be an active security group for its mapping to apply.
	mustExec(`UPDATE groups SET status='archived' WHERE id=$1`, groupID)
	recompute()
	if role, source := roleOf(manualID); role != "member" || source != "manual" {
		t.Fatalf("archived mapping role = %q source = %q", role, source)
	}
}
