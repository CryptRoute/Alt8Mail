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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStorageVolumeAddPlanAndRunnerIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage volume integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffixInt := time.Now().UnixNano() % 1000000
	name := fmt.Sprintf("mail-p3-%d", suffixInt)
	testUUID := fmt.Sprintf("6f0c2a1e-aaaa-bbbb-cccc-%012d", suffixInt)
	oldLookPath := storageLookPath
	oldDiscovery := storageDiscoveryCommand
	oldHost := storageHostCommand
	oldOwner := mailOwnerLookup
	oldGOOS := storageHostGOOS
	storageHostGOOS = "linux"
	storageLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	storageDiscoveryCommand = func(context.Context, time.Duration, string, ...string) (commandResult, error) {
		return commandResult{Stdout: `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":5000000000,"fstype":null,"label":null,"uuid":null,"mountpoint":null,"rota":false,"model":"Test Disk","serial":"SN-P3-TEST","wwn":"0x1234","tran":"sata"}]}`}, nil
	}
	storageHostCommand = func(ctx context.Context, timeout time.Duration, name string, args ...string) (commandResult, error) {
		switch name {
		case "blkid":
			return commandResult{Stdout: testUUID + "\n"}, nil
		case "findmnt":
			if len(args) > 0 && args[0] == "--verify" {
				return commandResult{}, nil
			}
			if len(args) >= 2 && args[0] == "-n" && args[1] == "-o" {
				return commandResult{Stdout: testUUID + "\n"}, nil
			}
			return commandResult{Stdout: "UUID=" + testUUID + " / ext4 rw\n"}, nil
		case "systemd-escape":
			return commandResult{Stdout: "srv-a8m\\x2dstorage-mail-03.mount\n"}, nil
		case "systemctl", "sgdisk", "partprobe", "udevadm", "mkfs.ext4":
			return commandResult{}, nil
		default:
			return commandResult{}, nil
		}
	}
	mailOwnerLookup = func() (int, int, error) { return -1, -1, nil }
	defer func() {
		storageLookPath = oldLookPath
		storageDiscoveryCommand = oldDiscovery
		storageHostCommand = oldHost
		mailOwnerLookup = oldOwner
		storageHostGOOS = oldGOOS
	}()

	root := t.TempDir()
	fstab := filepath.Join(root, "fstab")
	if err := os.WriteFile(fstab, []byte("# test fstab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `UPDATE control.storage_policy SET provisioning_enabled=true WHERE id=true`)
	defer func() {
		_, _ = pool.Exec(ctx, `UPDATE control.storage_policy SET provisioning_enabled=false WHERE id=true`)
	}()

	server := &Server{db: pool, cfg: Config{
		StorageRoot:         root,
		StorageFstab:        fstab,
		StorageProvisioning: true,
		BackupDir:           t.TempDir(),
	}}
	mountPath := filepath.Join(root, name)
	body, _ := json.Marshal(map[string]any{
		"kind": "add", "name": name, "mountPath": mountPath,
		"devicePath": "/dev/sdb", "deviceSerial": "SN-P3-TEST", "fsType": "ext4", "mode": "initialize",
	})
	request := httptest.NewRequest(http.MethodPost, "/control/api/storage/volumes/plan", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	session := &sessionRecord{User: adminUser{
		ID: "00000000-0000-4000-8000-000000000002", Email: "p3@example.test",
		Role: "superadmin", PermSet: permissionMap([]string{"*"}),
	}}
	server.handleStorageVolumePlan(recorder, request, session)
	if recorder.Code != http.StatusOK {
		t.Fatalf("plan status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OperationID string            `json:"operationId"`
		Token       string            `json:"token"`
		Plan        storageVolumePlan `json:"plan"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OperationID == "" || response.Plan.Name != name || response.Plan.PartitionPath != "/dev/sdb1" {
		t.Fatalf("unexpected plan: %+v", response)
	}
	if !strings.Contains(strings.Join(response.Plan.Commands, " "), "mkfs.ext4 -F") {
		t.Fatalf("add plan does not force-format a reused filesystem: %v", response.Plan.Commands)
	}
	if _, err := pool.Exec(ctx, `UPDATE control.storage_operations SET status='queued' WHERE id=$1`, response.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := server.RunStorageVolumeOperation(ctx, mustLoadStorageOperation(t, server, response.OperationID)); err != nil {
		t.Fatal(err)
	}
	op, err := server.loadStorageOperation(ctx, response.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "succeeded" {
		t.Fatalf("operation status = %s, error = %s", op.Status, op.Error)
	}
	var volumeID, storedPath, storedUUID string
	if err := pool.QueryRow(ctx, `SELECT id::text, mount_path, fs_uuid FROM control.storage_volumes WHERE name=$1`, name).Scan(&volumeID, &storedPath, &storedUUID); err != nil {
		t.Fatal(err)
	}
	if storedPath != mountPath || storedUUID == "" {
		t.Fatalf("registered volume = %s / %s", storedPath, storedUUID)
	}
	rawFstab, _ := os.ReadFile(fstab)
	if !strings.Contains(string(rawFstab), name) || !strings.Contains(string(rawFstab), storedUUID) {
		t.Fatalf("fstab does not contain the managed entry: %s", rawFstab)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM control.storage_volumes WHERE id=$1`, volumeID)
	_, _ = pool.Exec(ctx, `DELETE FROM control.storage_operations WHERE id=$1`, response.OperationID)
}

func mustLoadStorageOperation(t *testing.T, server *Server, id string) storageOperation {
	t.Helper()
	op, err := server.loadStorageOperation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}
