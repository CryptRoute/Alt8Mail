package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStorageMoveIntegration requires A8M_STORAGE_TEST_DSN with all migrations
// applied. It exercises the full move, rollback and finalize path against a
// real PostgreSQL operation table and two temporary volume roots.
func TestStorageMoveIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage move integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	oldLookup := mailOwnerLookup
	if runtime.GOOS == "windows" {
		mailOwnerLookup = func() (int, int, error) { return -1, -1, nil }
	} else {
		uid, gid := os.Getuid(), os.Getgid()
		mailOwnerLookup = func() (int, int, error) { return uid, gid, nil }
	}
	defer func() { mailOwnerLookup = oldLookup }()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "move.example", "alice", "Maildir", "cur"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "move.example", "alice", "Maildir", "new"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "move.example", "alice", "Maildir", "cur", "one"), []byte("message one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "move.example", "alice", "sieve.sieve"), []byte("keep;"), 0o600); err != nil {
		t.Fatal(err)
	}

	var tenantID, userID, mailboxID, legacyVolumeID, targetVolumeID, operationID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.tenants (slug, name, status) VALUES ($1, 'Storage move test', 'active')
		RETURNING id::text
	`, "storage-move-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id = $1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id = ANY($1::uuid[])`, []string{legacyVolumeID, targetVolumeID})
	}()
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.users (tenant_id, email, name, role, status)
		VALUES ($1, $2, 'Alice Move', 'member', 'active') RETURNING id::text
	`, tenantID, "alice@move.example").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mail_domains (tenant_id, domain, status) VALUES ($1, 'move.example', 'manual')
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name, mount_path, fs_type, role, managed, state, created_by)
		VALUES ($1, $2, 'ext4', 'mail', false, 'online', 'test') RETURNING id::text
	`, "legacy-"+suffix, sourceRoot).Scan(&legacyVolumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name, mount_path, fs_type, role, managed, state, created_by)
		VALUES ($1, $2, 'ext4', 'mail', false, 'online', 'test') RETURNING id::text
	`, "target-"+suffix, targetRoot).Scan(&targetVolumeID); err != nil {
		t.Fatal(err)
	}
	sourceHome := filepath.Join(sourceRoot, "move.example", "alice")
	targetHome := filepath.Join(targetRoot, "move.example", "alice")
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.mailboxes
		  (tenant_id, user_id, email, domain, local_part, maildir_path, mail_home, storage_volume_id, active, provision_status)
		VALUES ($1, $2, 'alice@move.example', 'move.example', 'alice', $3, $4, $5, true, 'ready')
		RETURNING id::text
	`, tenantID, userID, filepath.Join(sourceHome, "Maildir"), sourceHome, legacyVolumeID).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	plan := storageMovePlan{
		Scope: "mailbox", TargetVolumeID: targetVolumeID, TargetVolumeName: "target-" + suffix,
		TargetMountPath: targetRoot, Verify: "standard", GraceHours: 24,
	}
	rawPlan, _ := json.Marshal(plan)
	err = pool.QueryRow(ctx, `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, step, total_bytes, actor_email)
		VALUES ('move_mailboxes', 'queued', $1::jsonb, 'test', 'Queued', 12, 'tester@example.test')
		RETURNING id::text
	`, string(rawPlan)).Scan(&operationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO control.storage_move_items
		  (operation_id, mailbox_id, email, source_home, target_home, source_mail_home,
		   source_volume_id, source_volume_root, source_volume_name, target_volume_id,
		   target_volume_root, target_volume_name, target_is_legacy, bytes)
		VALUES ($1, $2, 'alice@move.example', $3, $4, $5, $6, $7, 'legacy', $8, $9, $10, false, 12)
	`, operationID, mailboxID, sourceHome, targetHome, sourceHome, legacyVolumeID, sourceRoot,
		targetVolumeID, targetRoot, "target-"+suffix); err != nil {
		t.Fatal(err)
	}

	server := &Server{db: pool, cfg: Config{VmailRoot: sourceRoot, StorageRoot: filepath.Dir(targetRoot), DovecotDir: t.TempDir()}}
	if err := server.RunStorageOperation(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	op, err := server.loadStorageOperation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "grace" {
		t.Fatalf("operation status = %s, want grace (error: %s)", op.Status, op.Error)
	}
	var mailHome, volumeID string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(mail_home,''), COALESCE(storage_volume_id::text,'') FROM public.mailboxes WHERE user_id=$1`, userID).Scan(&mailHome, &volumeID); err != nil {
		t.Fatal(err)
	}
	if mailHome != targetHome || volumeID != targetVolumeID {
		t.Fatalf("mailbox moved to %s volume %s, want %s volume %s", mailHome, volumeID, targetHome, targetVolumeID)
	}
	if content, err := os.ReadFile(filepath.Join(targetHome, "Maildir", "cur", "one")); err != nil || string(content) != "message one" {
		t.Fatalf("target message missing: %q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(sourceHome, "Maildir", "cur", "one")); err != nil {
		t.Fatalf("source was removed during the grace window: %v", err)
	}

	if err := server.RollbackStorageOperation(ctx, operationID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(mail_home,''), COALESCE(storage_volume_id::text,'') FROM public.mailboxes WHERE user_id=$1`, userID).Scan(&mailHome, &volumeID); err != nil {
		t.Fatal(err)
	}
	if mailHome != sourceHome || volumeID != legacyVolumeID {
		t.Fatalf("rollback left mailbox at %s volume %s", mailHome, volumeID)
	}

	// A domain-scope move changes the domain default while the move runs and
	// restores the previous default when it is rolled back.
	if _, err := pool.Exec(ctx, `UPDATE public.mail_domains SET storage_volume_id=$2 WHERE domain=$1`, "move.example", legacyVolumeID); err != nil {
		t.Fatal(err)
	}
	domainPlan := storageMovePlan{
		Scope: "domain", Domain: "move.example", TargetVolumeID: targetVolumeID,
		TargetVolumeName: "target-" + suffix, TargetMountPath: targetRoot,
		PreviousDomainVolumeID: legacyVolumeID, Verify: "standard", GraceHours: 24,
	}
	domainPlanJSON, _ := json.Marshal(domainPlan)
	var domainOperation string
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, step, total_bytes, actor_email)
		VALUES ('move_mailboxes', 'queued', $1::jsonb, 'test-domain', 'Queued', 12, 'tester@example.test')
		RETURNING id::text
	`, string(domainPlanJSON)).Scan(&domainOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO control.storage_move_items
		  (operation_id, mailbox_id, email, source_home, target_home, source_mail_home,
		   source_volume_id, source_volume_root, source_volume_name, target_volume_id,
		   target_volume_root, target_volume_name, target_is_legacy, bytes)
		VALUES ($1, $2, 'alice@move.example', $3, $4, $5, $6, $7, 'legacy', $8, $9, $10, false, 12)
	`, domainOperation, mailboxID, sourceHome, targetHome, sourceHome, legacyVolumeID, sourceRoot,
		targetVolumeID, targetRoot, "target-"+suffix); err != nil {
		t.Fatal(err)
	}
	if err := server.RunStorageOperation(ctx, domainOperation); err != nil {
		t.Fatal(err)
	}
	var effectiveDefault string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(storage_volume_id::text,'') FROM public.mail_domains WHERE domain='move.example'`).Scan(&effectiveDefault); err != nil {
		t.Fatal(err)
	}
	if effectiveDefault != targetVolumeID {
		t.Fatalf("domain default after move = %s, want %s", effectiveDefault, targetVolumeID)
	}
	if err := server.RollbackStorageOperation(ctx, domainOperation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(storage_volume_id::text,'') FROM public.mail_domains WHERE domain='move.example'`).Scan(&effectiveDefault); err != nil {
		t.Fatal(err)
	}
	if effectiveDefault != legacyVolumeID {
		t.Fatalf("domain default after rollback = %s, want %s", effectiveDefault, legacyVolumeID)
	}

	// Run a second move and finalize it explicitly.
	var secondOperation string
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, step, total_bytes, actor_email)
		VALUES ('move_mailboxes', 'queued', $1::jsonb, 'test2', 'Queued', 12, 'tester@example.test')
		RETURNING id::text
	`, string(rawPlan)).Scan(&secondOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO control.storage_move_items
		  (operation_id, mailbox_id, email, source_home, target_home, source_mail_home,
		   source_volume_id, source_volume_root, source_volume_name, target_volume_id,
		   target_volume_root, target_volume_name, target_is_legacy, bytes)
		VALUES ($1, $2, 'alice@move.example', $3, $4, $5, $6, $7, 'legacy', $8, $9, $10, false, 12)
	`, secondOperation, mailboxID, sourceHome, targetHome, sourceHome, legacyVolumeID, sourceRoot,
		targetVolumeID, targetRoot, "target-"+suffix); err != nil {
		t.Fatal(err)
	}
	if err := server.RunStorageOperation(ctx, secondOperation); err != nil {
		t.Fatal(err)
	}
	if err := server.FinalizeStorageOperation(ctx, secondOperation, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourceHome); !os.IsNotExist(err) {
		t.Fatalf("finalize left the source home in place: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM control.storage_operations WHERE id=$1`, secondOperation).Scan(&op.Status); err != nil {
		t.Fatal(err)
	}
	if op.Status != "succeeded" {
		t.Fatalf("final operation status = %s", op.Status)
	}
}

func TestStorageRestoreMapsArchiveToPlacedHome(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage restore integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	oldLookup := mailOwnerLookup
	mailOwnerLookup = func() (int, int, error) { return -1, -1, nil }
	defer func() { mailOwnerLookup = oldLookup }()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	vmailRoot := t.TempDir()
	volumeRoot := t.TempDir()
	stage := t.TempDir()
	domain := "restore-" + suffix + ".example.test"
	email := "alice@" + domain
	var tenantID, userID, volumeID string
	if err := pool.QueryRow(ctx, `INSERT INTO public.tenants (slug,name,status) VALUES ($1,'Restore storage','active') RETURNING id::text`, "restore-storage-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id=$1`, volumeID)
	}()
	if err := pool.QueryRow(ctx, `INSERT INTO public.users (tenant_id,email,name,role,status) VALUES ($1,$2,'Alice Restore','member','active') RETURNING id::text`, tenantID, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.mail_domains (tenant_id,domain,status) VALUES ($1,$2,'manual')`, tenantID, domain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name,mount_path,fs_type,role,managed,state,created_by)
		VALUES ($1,$2,'ext4','mail',false,'online','test') RETURNING id::text
	`, "restore-"+suffix, volumeRoot).Scan(&volumeID); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(volumeRoot, domain, "alice")
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mailboxes (tenant_id,user_id,email,domain,local_part,maildir_path,mail_home,storage_volume_id,active,provision_status)
		VALUES ($1,$2,$3,$4,'alice',$5,$6,$7,true,'ready')
	`, tenantID, userID, email, domain, filepath.Join(home, "Maildir"), home, volumeID); err != nil {
		t.Fatal(err)
	}
	stagedMaildir := filepath.Join(stage, "mail", "vmail", domain, "alice", "Maildir", "cur")
	if err := os.MkdirAll(stagedMaildir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedMaildir, "restored"), []byte("restored message"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: pool, cfg: Config{VmailRoot: vmailRoot, StorageRoot: filepath.Dir(volumeRoot)}}
	count, err := server.restoreMaildirTrees(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("restore copied no files")
	}
	content, err := os.ReadFile(filepath.Join(home, "Maildir", "cur", "restored"))
	if err != nil || string(content) != "restored message" {
		t.Fatalf("restored file missing or wrong: %q err=%v", content, err)
	}
}

func TestStorageMovePlanHandlerIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage plan integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	oldLookup := mailOwnerLookup
	mailOwnerLookup = func() (int, int, error) { return -1, -1, nil }
	defer func() { mailOwnerLookup = oldLookup }()
	oldStat := filesystemStatLookup
	filesystemStatLookup = func(string) (filesystemStat, error) {
		return filesystemStat{TotalBytes: 1 << 40, FreeBytes: 1 << 40, UsedPercent: 1}, nil
	}
	defer func() { filesystemStatLookup = oldStat }()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	domain := "plan-" + suffix + ".example.test"
	email := "alice@" + domain
	sourceHome := filepath.Join(sourceRoot, domain, "alice")
	if err := os.MkdirAll(filepath.Join(sourceHome, "Maildir", "cur"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHome, "Maildir", "cur", "one"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	dovecotDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dovecotDir, "dovecot-sql.conf.ext"), []byte("user_query = SELECT COALESCE(NULLIF(m.mail_home, ''), '/var/vmail/' || m.domain || '/' || m.local_part) AS home, 5000 AS uid, 5000 AS gid FROM mailboxes m;\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var tenantID, userID, mailboxID, sourceVolumeID, targetVolumeID string
	if err := pool.QueryRow(ctx, `INSERT INTO public.tenants (slug,name,status) VALUES ($1,'Plan test','active') RETURNING id::text`, "plan-test-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id=ANY($1::uuid[])`, []string{sourceVolumeID, targetVolumeID})
	}()
	if err := pool.QueryRow(ctx, `INSERT INTO public.users (tenant_id,email,name,role,status) VALUES ($1,$2,'Alice Plan','member','active') RETURNING id::text`, tenantID, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.mail_domains (tenant_id,domain,status) VALUES ($1,$2,'manual')`, tenantID, domain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO control.storage_volumes (name,mount_path,fs_type,role,managed,state,created_by) VALUES ($1,$2,'ext4','mail',false,'online','test') RETURNING id::text`, "plan-source-"+suffix, sourceRoot).Scan(&sourceVolumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO control.storage_volumes (name,mount_path,fs_type,role,managed,state,created_by) VALUES ($1,$2,'ext4','mail',false,'online','test') RETURNING id::text`, "plan-target-"+suffix, targetRoot).Scan(&targetVolumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.mailboxes (tenant_id,user_id,email,domain,local_part,maildir_path,mail_home,storage_volume_id,active,provision_status)
		VALUES ($1,$2,$3,$4,'alice',$5,$6,$7,true,'ready') RETURNING id::text
	`, tenantID, userID, email, domain, filepath.Join(sourceHome, "Maildir"), sourceHome, sourceVolumeID).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: pool, cfg: Config{
		VmailRoot: sourceRoot, StorageRoot: targetRoot, DovecotDir: dovecotDir,
	}}
	body, _ := json.Marshal(map[string]any{
		"scope": "mailbox", "mailboxIds": []string{mailboxID},
		"targetVolumeId": targetVolumeID, "verify": "standard", "graceHours": 1,
	})
	request := httptest.NewRequest(http.MethodPost, "/control/api/storage/moves/plan", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	session := &sessionRecord{User: adminUser{ID: "00000000-0000-4000-8000-000000000001", Email: "planner@example.test", Role: "superadmin"}}
	server.handleStorageMovePlan(recorder, request, session)
	if recorder.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OperationID string          `json:"operationId"`
		Token       string          `json:"token"`
		Plan        storageMovePlan `json:"plan"`
		Summary     map[string]any  `json:"summary"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OperationID == "" || response.Token == "" || len(response.Plan.Items) != 1 {
		t.Fatalf("unexpected plan response: %+v", response)
	}
	if response.Plan.Items[0].TargetHome != filepath.Join(targetRoot, domain, "alice") {
		t.Fatalf("unexpected target home %q", response.Plan.Items[0].TargetHome)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM control.storage_operations WHERE id=$1`, response.OperationID)
}
