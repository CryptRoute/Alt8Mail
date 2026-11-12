package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStorageMailboxPickerAPIIntegration exercises the server-side paging used
// by the move wizard. It requires A8M_STORAGE_TEST_DSN with migrations applied.
func TestStorageMailboxPickerAPIIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage mailbox picker integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	prefix := "picker-" + suffix
	domain := prefix + ".example"
	var tenantID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO public.tenants (slug, name, status) VALUES ($1, 'Storage picker test', 'active')
		RETURNING id::text
	`, "storage-picker-"+suffix).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	var volumeID, legacyVolumeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name, mount_path, fs_type, role, managed, state, created_by)
		VALUES ($1, $2, 'ext4', 'mail', true, 'online', 'test') RETURNING id::text
	`, "picker-volume-"+suffix, "/tmp/picker-volume-"+suffix).Scan(&volumeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes (name, mount_path, fs_type, role, managed, state, created_by)
		VALUES ($1, $2, 'ext4', 'mail', false, 'online', 'test') RETURNING id::text
	`, "picker-legacy-"+suffix, "/tmp/picker-legacy-"+suffix).Scan(&legacyVolumeID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		// Mailboxes reference volumes with ON DELETE RESTRICT, so remove the
		// tenant (and its mailboxes) before the volume registry rows.
		_, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id = $1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id = ANY($1::uuid[])`, []string{volumeID, legacyVolumeID})
	}()

	for index := 1; index <= 5; index++ {
		var userID string
		quota, used := int64(10*1024*1024*1024+int64(index)), int64(index*1024*1024)
		if err := pool.QueryRow(ctx, `
			INSERT INTO public.users (tenant_id, email, name, role, status, quota_bytes, used_bytes)
			VALUES ($1, $2, $3, 'member', 'active', $4, $5) RETURNING id::text
		`, tenantID, fmt.Sprintf("%s-%d@%s", prefix, index, domain), fmt.Sprintf("Picker User %d", index), quota, used).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		local := fmt.Sprintf("%s-%d", prefix, index)
		home := fmt.Sprintf("/tmp/picker-volume-%s/%s/%s", suffix, domain, local)
		var volume any = volumeID
		if index == 5 {
			volume = nil
			home = ""
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.mailboxes
			  (tenant_id, user_id, email, domain, local_part, maildir_path, mail_home, storage_volume_id, active, provision_status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, 'ready')
		`, tenantID, userID, fmt.Sprintf("%s-%d@%s", prefix, index, domain), domain, local, home+"/Maildir", home, volume); err != nil {
			t.Fatal(err)
		}
	}

	server := &Server{db: pool}
	request := func(query string) (int, map[string]any) {
		recorder := httptest.NewRecorder()
		server.handleStorageMailboxes(recorder, httptest.NewRequest(http.MethodGet, "/control/api/storage/mailboxes"+query, nil), nil)
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v (%s)", query, err, recorder.Body.String())
		}
		return recorder.Code, payload
	}

	status, page := request(fmt.Sprintf("?q=%s&limit=2&page=1", prefix))
	if status != http.StatusOK {
		t.Fatalf("page status = %d", status)
	}
	if got := int(page["total"].(float64)); got != 5 {
		t.Fatalf("total = %d, want 5", got)
	}
	if got := int(page["pages"].(float64)); got != 3 {
		t.Fatalf("pages = %d, want 3", got)
	}
	mailboxes, ok := page["mailboxes"].([]any)
	if !ok || len(mailboxes) != 2 {
		t.Fatalf("mailboxes = %#v, want 2 rows", page["mailboxes"])
	}
	first := mailboxes[0].(map[string]any)
	if !strings.HasSuffix(first["email"].(string), "-1@"+domain) {
		t.Fatalf("first email = %v, want the first sorted mailbox", first["email"])
	}
	if first["userName"] != "Picker User 1" {
		t.Fatalf("userName = %v", first["userName"])
	}
	if int64(first["quotaBytes"].(float64)) != 10*1024*1024*1024+1 {
		t.Fatalf("quotaBytes = %v", first["quotaBytes"])
	}
	if int64(first["usedBytes"].(float64)) != 1024*1024 {
		t.Fatalf("usedBytes = %v", first["usedBytes"])
	}
	if first["volume"] != "picker-volume-"+suffix {
		t.Fatalf("volume = %v", first["volume"])
	}
	if first["provisionStatus"] != "ready" {
		t.Fatalf("provisionStatus = %v", first["provisionStatus"])
	}

	status, byName := request("?q=Picker%20User&limit=10&page=1")
	if status != http.StatusOK || int(byName["total"].(float64)) < 5 {
		t.Fatalf("name search status=%d total=%v", status, byName["total"])
	}

	status, legacy := request(fmt.Sprintf("?q=%s&limit=10&volume=legacy", prefix))
	if status != http.StatusOK || int(legacy["total"].(float64)) != 1 {
		t.Fatalf("legacy filter status=%d total=%v", status, legacy["total"])
	}
	legacyRows := legacy["mailboxes"].([]any)
	if got := legacyRows[0].(map[string]any)["volume"]; got != "" {
		t.Fatalf("legacy volume = %v, want empty", got)
	}

	status, ids := request(fmt.Sprintf("?q=%s&idsOnly=1&limit=3", prefix))
	if status != http.StatusOK {
		t.Fatalf("ids status = %d", status)
	}
	if got := int(ids["total"].(float64)); got != 5 {
		t.Fatalf("ids total = %d, want 5", got)
	}
	idRows := ids["ids"].([]any)
	if len(idRows) != 3 || ids["truncated"] != true {
		t.Fatalf("ids = %v truncated = %v, want 3 truncated rows", idRows, ids["truncated"])
	}
}
