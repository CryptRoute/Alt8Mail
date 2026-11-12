package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func storageIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func storageIntegrationSession() *sessionRecord {
	return &sessionRecord{User: adminUser{
		ID: "00000000-0000-4000-8000-000000000002", Email: "p3-lifecycle@example.test",
		Role: "superadmin", PermSet: permissionMap([]string{"*"}),
	}}
}

func createStorageIntegrationVolume(t *testing.T, pool *pgxpool.Pool, name, mountPath, state, fsUUID string, fstabManaged bool) (string, func()) {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_uuid, device_path, fs_type, role, managed, state,
		   size_bytes, fstab_managed, device_serial, device_wwn, mount_unit, created_by)
		VALUES ($1, $2, $3, '/dev/sdb1', 'ext4', 'mail', true, $4, 1000, $5,
		        'SN-LIFECYCLE', '0xlifecycle', 'srv-a8m-storage-test.mount', 'a8m')
		RETURNING id::text
	`, name, mountPath, fsUUID, state, fstabManaged).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM control.storage_volumes WHERE id=$1`, id)
	}
}

func runStorageIntegrationOperation(t *testing.T, server *Server, plan storageVolumePlan) storageOperation {
	t.Helper()
	ctx := context.Background()
	opID, _, _, err := server.createStorageVolumeOperation(ctx, storageIntegrationSession(), plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = server.db.Exec(context.Background(), `DELETE FROM control.storage_operations WHERE id=$1`, opID)
	})
	if _, err := server.db.Exec(ctx, `UPDATE control.storage_operations SET status='queued' WHERE id=$1`, opID); err != nil {
		t.Fatal(err)
	}
	op := mustLoadStorageOperation(t, server, opID)
	if err := server.RunStorageVolumeOperation(ctx, op); err != nil {
		t.Fatalf("runner failed: %v (log: %s)", err, operationLog(t, server, opID))
	}
	loaded, err := server.loadStorageOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "succeeded" {
		t.Fatalf("operation status = %s, error = %s, log = %s", loaded.Status, loaded.Error, loaded.Log)
	}
	return loaded
}

func operationLog(t *testing.T, server *Server, id string) string {
	t.Helper()
	var log string
	_ = server.db.QueryRow(context.Background(), `SELECT log FROM control.storage_operations WHERE id=$1`, id).Scan(&log)
	return log
}

func installStorageLifecycleHost(t *testing.T, discovery string, handler func(context.Context, time.Duration, string, ...string) (commandResult, error)) {
	t.Helper()
	oldLookPath := storageLookPath
	oldDiscovery := storageDiscoveryCommand
	oldHost := storageHostCommand
	oldGOOS := storageHostGOOS
	storageHostGOOS = "linux"
	storageLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	storageDiscoveryCommand = func(context.Context, time.Duration, string, ...string) (commandResult, error) {
		return commandResult{Stdout: discovery}, nil
	}
	storageHostCommand = handler
	t.Cleanup(func() {
		storageLookPath = oldLookPath
		storageDiscoveryCommand = oldDiscovery
		storageHostCommand = oldHost
		storageHostGOOS = oldGOOS
	})
}

func setStorageProvisioningForTest(t *testing.T, pool *pgxpool.Pool, enabled bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE control.storage_policy SET provisioning_enabled=$1 WHERE id=true`, enabled); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE control.storage_policy SET provisioning_enabled=false WHERE id=true`)
	})
}

func TestStorageVolumeAdoptAndExtendIntegration(t *testing.T) {
	pool := storageIntegrationPool(t)
	setStorageProvisioningForTest(t, pool, true)
	roots := t.TempDir()
	adoptPath := filepath.Join(roots, "adopt-volume")
	extendPath := filepath.Join(roots, "extend-volume")
	nfsPath := filepath.Join(roots, "nfs-volume")
	for _, path := range []string{adoptPath, extendPath, nfsPath} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	suffixInt := time.Now().UnixNano() % 1000000
	suffix := fmt.Sprintf("%d", suffixInt)
	adoptUUID := fmt.Sprintf("6f0c2a1e-aaaa-bbbb-cccc-%012d", suffixInt)
	extendCalls := 0
	installStorageLifecycleHost(t, `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":100000000000,"serial":"SN-LIFECYCLE","wwn":"0xlifecycle","children":[{"name":"sdb1","path":"/dev/sdb1","type":"part","size":100000000000}]}]}`,
		func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
			target := ""
			if len(args) > 0 {
				target = args[len(args)-1]
			}
			switch name {
			case "findmnt":
				if len(args) > 0 && args[0] == "--verify" {
					return commandResult{}, nil
				}
				if containsArg(args, "UUID") {
					if target == nfsPath {
						return commandResult{}, nil
					}
					return commandResult{Stdout: adoptUUID + "\n"}, nil
				}
				if target == nfsPath {
					return commandResult{Stdout: "server:/export nfs4 rw\n"}, nil
				}
				if target == adoptPath {
					return commandResult{Stdout: "/dev/sdc1 ext4 rw,relatime\n"}, nil
				}
				if target == extendPath {
					return commandResult{Stdout: "/dev/sdb1 ext4\n"}, nil
				}
				return commandResult{}, nil
			case "lsblk":
				return commandResult{Stdout: "part sdb\n"}, nil
			case "systemd-escape":
				return commandResult{Stdout: "srv-a8m-storage-test.mount\n"}, nil
			case "systemctl", "growpart", "resize2fs":
				return commandResult{}, nil
			default:
				return commandResult{}, nil
			}
		})
	oldStat := filesystemStatLookup
	filesystemStatLookup = func(path string) (filesystemStat, error) {
		if strings.Contains(path, "extend-volume") {
			extendCalls++
			total := uint64(1000)
			if extendCalls > 1 {
				total = 2000
			}
			return filesystemStat{TotalBytes: total, FreeBytes: total / 2, UsedBytes: total / 2}, nil
		}
		return filesystemStat{TotalBytes: 5000, FreeBytes: 2500, UsedBytes: 2500}, nil
	}
	t.Cleanup(func() { filesystemStatLookup = oldStat })

	server := &Server{db: pool, cfg: Config{
		StorageRoot: roots, StorageFstab: filepath.Join(roots, "fstab"),
		StorageProvisioning: true, BackupDir: t.TempDir(), VmailRoot: filepath.Join(roots, "vmail"),
	}}

	adoptName := "adopt-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM control.storage_volumes WHERE name=$1`, adoptName)
	})
	adoptPlan, err := server.buildStorageVolumeAdoptPlan(context.Background(), storageVolumePlanRequest{
		Name: adoptName, MountPath: adoptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	runStorageIntegrationOperation(t, server, adoptPlan)
	var adoptedState, adoptedBy, adoptedType string
	if err := pool.QueryRow(context.Background(), `
		SELECT state, created_by, fs_type FROM control.storage_volumes WHERE name=$1
	`, adoptName).Scan(&adoptedState, &adoptedBy, &adoptedType); err != nil {
		t.Fatal(err)
	}
	if adoptedState != "online" || adoptedBy != "adopted" || adoptedType != "ext4" {
		t.Fatalf("adopted volume = %s/%s/%s", adoptedState, adoptedBy, adoptedType)
	}

	nfsName := "nfs-" + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM control.storage_volumes WHERE name=$1`, nfsName)
	})
	nfsPlan, err := server.buildStorageVolumeAdoptPlan(context.Background(), storageVolumePlanRequest{
		Name: nfsName, MountPath: nfsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nfsPlan.FSType != "nfs4" || len(nfsPlan.Warnings) == 0 {
		t.Fatalf("unexpected NFS plan: %+v", nfsPlan)
	}
	runStorageIntegrationOperation(t, server, nfsPlan)
	var nfsType string
	if err := pool.QueryRow(context.Background(), `SELECT fs_type FROM control.storage_volumes WHERE name=$1`, nfsName).Scan(&nfsType); err != nil {
		t.Fatal(err)
	}
	if nfsType != "nfs4" {
		t.Fatalf("NFS volume fs_type = %s", nfsType)
	}

	extendID, extendCleanup := createStorageIntegrationVolume(t, pool, "extend-"+suffix, extendPath, "online", fmt.Sprintf("6f0c2a1e-dddd-eeee-ffff-%012d", suffixInt), false)
	defer extendCleanup()
	extendPlan, err := server.buildStorageVolumeExtendPlan(context.Background(), storageVolumePlanRequest{VolumeID: extendID})
	if err != nil {
		t.Fatal(err)
	}
	if extendPlan.GrowDisk != "/dev/sdb" || extendPlan.GrowPartition != "1" || extendPlan.ResizeDevice != "/dev/sdb1" {
		t.Fatalf("unexpected extend plan: %+v", extendPlan)
	}
	runStorageIntegrationOperation(t, server, extendPlan)
	var grownSize uint64
	if err := pool.QueryRow(context.Background(), `SELECT size_bytes FROM control.storage_volumes WHERE id=$1`, extendID).Scan(&grownSize); err != nil {
		t.Fatal(err)
	}
	if grownSize != 2000 {
		t.Fatalf("volume size after extend = %d, want 2000", grownSize)
	}
}

func TestStorageVolumeRetireAndWipeIntegration(t *testing.T) {
	pool := storageIntegrationPool(t)
	setStorageProvisioningForTest(t, pool, true)
	root := t.TempDir()
	mountPath := filepath.Join(root, "retire-volume")
	if err := os.MkdirAll(mountPath, 0o750); err != nil {
		t.Fatal(err)
	}
	fstabPath := filepath.Join(root, "fstab")
	suffixInt := time.Now().UnixNano() % 1000000
	fsUUID := fmt.Sprintf("6f0c2a1e-1111-2222-3333-%012d", suffixInt)
	name := fmt.Sprintf("retire-%d", suffixInt)
	block := storageFstabBlock(name, fsUUID, mountPath, "ext4")
	if err := os.WriteFile(fstabPath, []byte("# test fstab\n"+block+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wipeCalls := []string{}
	installStorageLifecycleHost(t, `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":100000000000,"serial":"SN-LIFECYCLE","wwn":"0xlifecycle","children":[{"name":"sdb1","path":"/dev/sdb1","type":"part","size":100000000000}]}]}`,
		func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
			switch name {
			case "findmnt":
				if len(args) > 0 && args[0] == "--verify" {
					return commandResult{}, nil
				}
				return commandResult{}, nil
			case "systemctl":
				return commandResult{}, nil
			case "wipefs":
				wipeCalls = append(wipeCalls, strings.Join(args, " "))
				return commandResult{}, nil
			case "udevadm":
				return commandResult{}, nil
			case "systemd-escape":
				return commandResult{Stdout: "srv-a8m-storage-test.mount\n"}, nil
			default:
				return commandResult{}, nil
			}
		})

	server := &Server{db: pool, cfg: Config{
		StorageRoot: root, StorageFstab: fstabPath,
		StorageProvisioning: true, BackupDir: t.TempDir(), VmailRoot: filepath.Join(root, "vmail"),
	}}
	volumeID, cleanup := createStorageIntegrationVolume(t, pool, name, mountPath, "online", fsUUID, true)
	defer cleanup()

	retirePlan, err := server.buildStorageVolumeRetirePlan(context.Background(), storageVolumePlanRequest{VolumeID: volumeID})
	if err != nil {
		t.Fatal(err)
	}
	runStorageIntegrationOperation(t, server, retirePlan)
	var state string
	if err := pool.QueryRow(context.Background(), `SELECT state FROM control.storage_volumes WHERE id=$1`, volumeID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "retired" {
		t.Fatalf("state after retire = %s", state)
	}
	rawFstab, _ := os.ReadFile(fstabPath)
	if strings.Contains(string(rawFstab), fsUUID) || strings.Contains(string(rawFstab), "name="+name) {
		t.Fatalf("retire left the managed fstab block behind: %s", rawFstab)
	}

	wipePlan, err := server.buildStorageVolumeWipePlan(context.Background(), storageVolumePlanRequest{VolumeID: volumeID})
	if err != nil {
		t.Fatal(err)
	}
	if !wipePlan.RequireDestroy || wipePlan.DevicePath != "/dev/sdb1" {
		t.Fatalf("unexpected wipe plan: %+v", wipePlan)
	}
	runStorageIntegrationOperation(t, server, wipePlan)
	if err := pool.QueryRow(context.Background(), `SELECT state FROM control.storage_volumes WHERE id=$1`, volumeID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "wiped" {
		t.Fatalf("state after wipe = %s", state)
	}
	if len(wipeCalls) != 1 || wipeCalls[0] != "-a /dev/sdb1" {
		t.Fatalf("wipefs calls = %v, want exactly -a /dev/sdb1", wipeCalls)
	}
}

func TestStorageVolumeWipeRefusesOnlineVolume(t *testing.T) {
	pool := storageIntegrationPool(t)
	setStorageProvisioningForTest(t, pool, true)
	root := t.TempDir()
	installStorageLifecycleHost(t, `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":100000000000,"serial":"SN-LIFECYCLE","children":[{"name":"sdb1","path":"/dev/sdb1","type":"part"}]}]}`,
		func(context.Context, time.Duration, string, ...string) (commandResult, error) {
			return commandResult{}, nil
		})
	server := &Server{db: pool, cfg: Config{
		StorageRoot: root, StorageFstab: filepath.Join(root, "fstab"),
		StorageProvisioning: true, BackupDir: t.TempDir(), VmailRoot: filepath.Join(root, "vmail"),
	}}
	name := fmt.Sprintf("online-%d", time.Now().UnixNano()%1000000)
	volumeID, cleanup := createStorageIntegrationVolume(t, pool, name, filepath.Join(root, name), "online", "6f0c2a1e-online", false)
	defer cleanup()
	if _, err := server.buildStorageVolumeWipePlan(context.Background(), storageVolumePlanRequest{VolumeID: volumeID}); err == nil {
		t.Fatal("wipe plan was built for an online volume")
	}
}

func TestStorageVolumeAddKeepsFstabWhenFormattingFails(t *testing.T) {
	pool := storageIntegrationPool(t)
	setStorageProvisioningForTest(t, pool, true)
	root := t.TempDir()
	fstabPath := filepath.Join(root, "fstab")
	const original = "# original fstab\nUUID=root / ext4 defaults 0 1\n"
	if err := os.WriteFile(fstabPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	installStorageLifecycleHost(t, `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":5000000000,"serial":"SN-MKFS-FAIL"}]}`,
		func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
			if name == "mkfs.ext4" {
				return commandResult{Stderr: "injected mkfs failure"}, errors.New("exit status 1")
			}
			return commandResult{}, nil
		})
	server := &Server{db: pool, cfg: Config{
		StorageRoot: root, StorageFstab: fstabPath,
		StorageProvisioning: true, BackupDir: t.TempDir(), VmailRoot: filepath.Join(root, "vmail"),
	}}
	plan := storageVolumePlan{
		Kind: "volume_add", Mode: "initialize", Name: fmt.Sprintf("mkfs-fail-%d", time.Now().UnixNano()%1000000),
		MountPath: filepath.Join(root, "mkfs-fail-volume"), DevicePath: "/dev/sdb",
		DeviceSerial: "SN-MKFS-FAIL", FSType: "ext4",
	}
	ctx := context.Background()
	opID, _, _, err := server.createStorageVolumeOperation(ctx, storageIntegrationSession(), plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM control.storage_operations WHERE id=$1`, opID)
	})
	if _, err := pool.Exec(ctx, `UPDATE control.storage_operations SET status='queued' WHERE id=$1`, opID); err != nil {
		t.Fatal(err)
	}
	if err := server.RunStorageVolumeOperation(ctx, mustLoadStorageOperation(t, server, opID)); err == nil {
		t.Fatal("runner accepted a failing mkfs")
	}
	content, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatalf("fstab disappeared after an early runner failure: %v", err)
	}
	if string(content) != original {
		t.Fatalf("fstab changed after an early runner failure: %s", content)
	}
}

func TestStorageFstabRollbackOnValidationFailure(t *testing.T) {
	root := t.TempDir()
	fstabPath := filepath.Join(root, "fstab")
	const original = "# original fstab\nUUID=root / ext4 defaults 0 1\n"
	if err := os.WriteFile(fstabPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	oldHost := storageHostCommand
	storageHostCommand = func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
		if name == "findmnt" && len(args) > 0 && args[0] == "--verify" {
			return commandResult{Stderr: "bad fstab candidate"}, errors.New("exit status 1")
		}
		return commandResult{}, nil
	}
	t.Cleanup(func() { storageHostCommand = oldHost })

	server := &Server{cfg: Config{StorageFstab: fstabPath, BackupDir: filepath.Join(root, "backups")}}
	if err := server.writeStorageFstab(context.Background(), "# candidate fstab\n", original); err == nil {
		t.Fatal("writeStorageFstab accepted an invalid candidate")
	}
	content, err := os.ReadFile(fstabPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("fstab was not restored: %s", content)
	}
	backups, err := os.ReadDir(filepath.Join(root, "backups", "config"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("no fstab backup was written: %v %v", backups, err)
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestStorageVolumeTopologyGrowthGuards(t *testing.T) {
	pool := storageIntegrationPool(t)
	setStorageProvisioningForTest(t, pool, true)
	root := t.TempDir()
	suffixInt := time.Now().UnixNano() % 1000000
	server := &Server{db: pool, cfg: Config{
		StorageRoot: root, StorageFstab: filepath.Join(root, "fstab"),
		StorageProvisioning: true, BackupDir: t.TempDir(), VmailRoot: filepath.Join(root, "vmail"),
	}}
	mountPath := filepath.Join(root, "topology-volume")
	if err := os.MkdirAll(mountPath, 0o750); err != nil {
		t.Fatal(err)
	}
	volumeID, cleanup := createStorageIntegrationVolume(t, pool, fmt.Sprintf("topology-%d", suffixInt), mountPath, "online", fmt.Sprintf("6f0c2a1e-eeee-ffff-aaaa-%012d", suffixInt), false)
	defer cleanup()
	installStorageLifecycleHost(t, `{"blockdevices":[{"name":"sdb","path":"/dev/sdb","type":"disk","size":100000000000,"serial":"SN-LIFECYCLE"}]}`,
		func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
			switch name {
			case "findmnt":
				return commandResult{Stdout: "/dev/mapper/vg-thin ext4\n"}, nil
			case "lsblk":
				return commandResult{Stdout: "lvm\n"}, nil
			case "lvs":
				return commandResult{Stdout: "thin,sparse vg thin\n"}, nil
			default:
				return commandResult{}, nil
			}
		})
	if _, err := server.buildStorageVolumeExtendPlan(context.Background(), storageVolumePlanRequest{VolumeID: volumeID}); err == nil || !strings.Contains(err.Error(), "thin LVM") {
		t.Fatalf("thin LVM extension was not refused: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE control.storage_volumes SET fs_type='nfs4', device_path='server:/export' WHERE id=$1`, volumeID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.buildStorageVolumeExtendPlan(context.Background(), storageVolumePlanRequest{VolumeID: volumeID}); err == nil || !strings.Contains(err.Error(), "NFS") {
		t.Fatalf("NFS extension was not refused: %v", err)
	}
}
