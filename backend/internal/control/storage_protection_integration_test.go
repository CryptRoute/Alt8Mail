package control

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStorageProtectionIntegration(t *testing.T) {
	dsn := strings.TrimSpace(testStorageDSN())
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage protection integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, _ = pool.Exec(ctx, `
		DELETE FROM public.mailboxes
		WHERE storage_volume_id IN (
		  SELECT id FROM control.storage_volumes
		  WHERE name LIKE 'protect-source-%' OR name LIKE 'protect-target-%'
		)
	`)
	_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE name LIKE 'protect-source-%' OR name LIKE 'protect-target-%'`)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	domain := "protect" + suffix + ".example"
	localPart := "user" + suffix
	email := "protect" + suffix + "@" + domain
	mailboxEmail := localPart + "@" + domain
	root := t.TempDir()

	var tenantID, userID, sourceVolumeID, targetVolumeID, legacyVolumeID, mailboxID, legacyMailboxID string
	legacyEmail := "legacy" + suffix + "@" + "protect" + suffix + ".example"
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.tenants (slug, name, status) VALUES ($1, 'Storage protection test', 'active')
		RETURNING id::text
	`, "storage-protect-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.mailboxes WHERE email=$1`, email)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.user_aliases WHERE email=$1`, email)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.users WHERE email=$1`, email)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.mail_domains WHERE tenant_id=$1`, tenantID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.tenants WHERE id=$1`, tenantID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.mailboxes WHERE email=$1`, legacyEmail)
		_, _ = pool.Exec(cleanupCtx, `UPDATE control.storage_policy SET default_volume_id=NULL WHERE id=true`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM public.users WHERE email=$1`, email)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM control.storage_volumes WHERE id=ANY($1::uuid[])`, []string{sourceVolumeID, targetVolumeID, legacyVolumeID})
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.users (tenant_id, email, name, role, status)
		VALUES ($1, $2, 'Protect User', 'member', 'active') RETURNING id::text
	`, tenantID, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mail_domains (tenant_id, domain, status) VALUES ($1, $2, 'manual')
	`, tenantID, domain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_type, role, managed, state, created_by,
		   capacity_total_bytes, capacity_used_bytes, capacity_free_bytes,
		   capacity_percent, capacity_state, capacity_checked_at)
		VALUES ($1, $2, 'ext4', 'mail', true, 'online', 'test', 1000, 920, 80, 92, 'serious', now())
		RETURNING id::text
	`, "protect-source-"+suffix, filepath.Join(root, "source")).Scan(&sourceVolumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_type, role, managed, state, created_by,
		   capacity_total_bytes, capacity_used_bytes, capacity_free_bytes,
		   capacity_percent, capacity_state, capacity_checked_at)
		VALUES ($1, $2, 'ext4', 'mail', true, 'online', 'test', 1000, 100, 900, 10, 'ok', now())
		RETURNING id::text
	`, "protect-target-"+suffix, filepath.Join(root, "target")).Scan(&targetVolumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_type, role, managed, state, created_by,
		   capacity_total_bytes, capacity_used_bytes, capacity_free_bytes,
		   capacity_percent, capacity_state, capacity_checked_at)
		VALUES ($1, $2, 'ext4', 'mail', false, 'online', 'test', 1000, 100, 900, 10, 'ok', now())
		RETURNING id::text
	`, "protect-legacy-"+suffix, filepath.Join(root, "legacy")).Scan(&legacyVolumeID); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "source", domain, localPart)
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.mailboxes
		  (tenant_id, user_id, email, domain, local_part, maildir_path, mail_home, storage_volume_id, active, provision_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, 'ready')
		RETURNING id::text
	`, tenantID, userID, mailboxEmail, domain, localPart, filepath.Join(home, "Maildir"), home, sourceVolumeID).Scan(&mailboxID); err != nil {
		t.Fatal(err)
	}
	legacyHome := filepath.Join(root, "legacy", domain, "legacy")
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.mailboxes
		  (tenant_id, user_id, email, domain, local_part, maildir_path, mail_home, storage_volume_id, active, provision_status)
		VALUES ($1, $2, $3, $4, 'legacy', $5, NULL, NULL, true, 'ready')
		RETURNING id::text
	`, tenantID, userID, legacyEmail, domain, filepath.Join(legacyHome, "Maildir")).Scan(&legacyMailboxID); err != nil {
		t.Fatal(err)
	}
	baseDay := time.Now().UTC().Truncate(24 * time.Hour).Add(-10 * 24 * time.Hour)
	for i := 0; i <= 10; i++ {
		used := 500 + i*10
		if _, err := pool.Exec(ctx, `
			INSERT INTO control.storage_samples (volume_id, sampled_at, total_bytes, used_bytes, free_bytes)
			VALUES ($1, $2, 1000, $3, $4)
		`, sourceVolumeID, baseDay.Add(time.Duration(i)*24*time.Hour), used, 1000-used); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE control.storage_policy
		SET guard_enabled=true, min_free_bytes=0, reserve_percent=5
		WHERE id=true
	`); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: pool, cfg: Config{VmailRoot: filepath.Join(root, "legacy"), StorageRoot: root}}
	volume := storageVolume{ID: sourceVolumeID, Name: "protect-source", CapacityTotalBytes: 1000}
	policy, err := server.storagePolicyRow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	forecast := server.storageForecast(ctx, volume, policy)
	if !forecast.EnoughData || forecast.DailyGrowthBytes != 10 {
		t.Fatalf("forecast = %+v", forecast)
	}
	var criticalDays *float64
	for _, threshold := range forecast.Thresholds {
		if threshold.Level == "critical" {
			criticalDays = threshold.Days
		}
	}
	if criticalDays == nil || *criticalDays < 34 || *criticalDays > 36 {
		t.Fatalf("critical forecast days = %v", criticalDays)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO control.storage_usage (volume_id, mailbox_id, email, domain, bytes, messages, scanned_at)
		VALUES ($1, $2, $3, $4, 600, 12, now())
	`, sourceVolumeID, mailboxID, mailboxEmail, domain); err != nil {
		t.Fatal(err)
	}
	payload, err := server.storageUsagePayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Domains) == 0 || payload.Recommendation == nil {
		t.Fatalf("usage payload = %+v", payload)
	}
	if payload.Recommendation.TargetVolumeID == sourceVolumeID || payload.Recommendation.Domain != domain {
		t.Fatalf("recommendation = %+v", payload.Recommendation)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE control.storage_volumes SET capacity_state='critical', capacity_percent=96,
		  capacity_checked_at=now() WHERE id=$1
	`, sourceVolumeID); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := server.storageGuardForEmail(ctx, mailboxEmail); !blocked {
		t.Fatal("critical volume did not block the mailbox")
	}
	if _, err := pool.Exec(ctx, `UPDATE control.storage_policy SET default_volume_id=$1 WHERE id=true`, sourceVolumeID); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := server.storageGuardForEmail(ctx, legacyEmail); blocked {
		t.Fatal("a legacy mailbox was guarded by the new global default volume")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE control.storage_volumes SET capacity_checked_at=now() - interval '10 minutes' WHERE id=$1
	`, sourceVolumeID); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := server.storageGuardForEmail(ctx, mailboxEmail); blocked {
		t.Fatal("stale capacity state blocked mail")
	}

	volume.CapacityState = "unknown"
	volume.Name = "protect-source-" + suffix
	server.reconcileStorageAlert(ctx, volume, policy, "warning", 86, 140, 1000)
	var active int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM control.storage_alerts WHERE volume_id=$1 AND level='warning' AND status='active'
	`, sourceVolumeID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("warning alert missing: active=%d err=%v", active, err)
	}
	server.reconcileStorageAlert(ctx, volume, policy, "critical", 96, 40, 1000)
	var warningStatus, criticalStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM control.storage_alerts WHERE volume_id=$1 AND level='warning'`, sourceVolumeID).Scan(&warningStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM control.storage_alerts WHERE volume_id=$1 AND level='critical'`, sourceVolumeID).Scan(&criticalStatus); err != nil {
		t.Fatal(err)
	}
	if warningStatus != "resolved" || criticalStatus != "active" {
		t.Fatalf("alert transitions = warning %s, critical %s", warningStatus, criticalStatus)
	}
	if _, err := pool.Exec(ctx, `UPDATE control.storage_policy SET forecast_alert_days=365 WHERE id=true`); err != nil {
		t.Fatal(err)
	}
	policy, err = server.storagePolicyRow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectedVolume := storageVolume{ID: sourceVolumeID, Name: volume.Name, CapacityPercent: 92, CapacityFreeBytes: 80}
	server.reconcileProjectedFullAlert(ctx, projectedVolume, policy, 151, 10)
	var projectedStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM control.storage_alerts WHERE volume_id=$1 AND level='projected_full'
	`, sourceVolumeID).Scan(&projectedStatus); err != nil {
		t.Fatal(err)
	}
	if projectedStatus != "active" {
		t.Fatalf("projected-full alert status = %s", projectedStatus)
	}
	server.reconcileStorageAlert(ctx, projectedVolume, policy, "ok", 50, 500, 1000)
	if err := pool.QueryRow(ctx, `
		SELECT status FROM control.storage_alerts WHERE volume_id=$1 AND level='projected_full'
	`, sourceVolumeID).Scan(&projectedStatus); err != nil {
		t.Fatal(err)
	}
	if projectedStatus != "active" {
		t.Fatalf("capacity recovery incorrectly resolved projected-full alert: %s", projectedStatus)
	}
}

func testStorageDSN() string {
	return strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
}

func TestStoragePolicyUpdateHandler(t *testing.T) {
	dsn := strings.TrimSpace(testStorageDSN())
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage policy integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var original int
	if err := pool.QueryRow(ctx, `SELECT forecast_alert_days FROM control.storage_policy WHERE id=true`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE control.storage_policy SET forecast_alert_days=$1 WHERE id=true`, original)
	})
	want := original + 1
	if want > 365 {
		want = original - 1
	}
	server := &Server{db: pool}
	request := httptest.NewRequest(http.MethodPatch, "/control/api/storage/policy",
		strings.NewReader(fmt.Sprintf(`{"forecastAlertDays":%d}`, want)))
	recorder := httptest.NewRecorder()
	session := &sessionRecord{User: adminUser{ID: "00000000-0000-4000-8000-000000000002", Email: "policy@example.test", Role: "superadmin", PermSet: permissionMap([]string{"*"})}}
	server.handleStoragePolicy(recorder, request, session)
	if recorder.Code != http.StatusOK {
		t.Fatalf("policy update status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT forecast_alert_days FROM control.storage_policy WHERE id=true`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != want {
		t.Fatalf("forecast_alert_days = %d, want %d", stored, want)
	}
}
