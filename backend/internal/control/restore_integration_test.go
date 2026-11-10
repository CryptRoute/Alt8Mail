package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMailboxRestorePreviewIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the mailbox restore integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := fmt.Sprintf("00000000-0000-4002-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4003-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "restore-" + suffix + "@restore.example.test"
	slug := "restoretest-" + suffix
	domain := "restore-" + suffix + ".example.test"
	local := "alice"
	backupRoot := filepath.Join(t.TempDir(), "backups")
	vmailRoot := filepath.Join(t.TempDir(), "vmail")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	maildir := filepath.Join(vmailRoot, domain, local, "Maildir")
	if err := os.MkdirAll(filepath.Join(maildir, ".Sent", "cur"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(maildir, ".Sent", "cur", "sent1"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := writeRestoreFixture(t, backupRoot, time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), domain, local)

	var backupID string
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM control.backups WHERE id::text=$1`, backupID)
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id::text=$1`, tenantID)
	}
	defer cleanup()
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name, status) VALUES ($1,$2,'Restore Test','active')`, tenantID, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.users (id, tenant_id, email, name, role, status) VALUES ($1,$2,$3,'Restore Test','member','active')`, userID, tenantID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active) VALUES ($1,$2,$3,$4,$5,$6,true)`, tenantID, userID, email, domain, local, maildir); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO control.backups (name,path,contents,status) VALUES ('restore-fixture',$1,ARRAY['database','maildirs','uploads'],'completed') RETURNING id::text`, archive).Scan(&backupID); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: Config{BackupDir: backupRoot, VmailRoot: vmailRoot}, db: pool}
	preview, target, name, err := s.previewMailboxRestore(ctx, backupID, mailboxRestoreRequest{Email: email, Folder: "sent"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "restore-fixture" || target.Folder != "sent" || preview.Messages != 2 || preview.Conflicts != 1 {
		t.Fatalf("preview = %#v target=%#v name=%q", preview, target, name)
	}
	if _, _, _, err := s.previewMailboxRestore(ctx, backupID, mailboxRestoreRequest{Email: email, Folder: "sent", TenantID: "00000000-0000-4004-8000-000000000001"}); err == nil {
		t.Fatal("cross-tenant preview was accepted")
	}

	jobID, err := s.createRestoreJob(ctx, backupID, target, "preview", preview, "")
	if err != nil {
		t.Fatal(err)
	}
	s.updateRestoreJob(jobID, "completed", preview, "preview")
	job, err := s.loadRestoreJob(ctx, jobID)
	if err != nil || job.Status != "completed" || job.Email != email || job.Folder != "sent" {
		t.Fatalf("job = %#v err=%v", job, err)
	}
	if _, err := os.Stat(filepath.Join(backupRoot, "outside.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("unexpected fixture path")
	}
}
