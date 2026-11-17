package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) RunStorageVolumeOperation(ctx context.Context, op storageOperation) error {
	var plan storageVolumePlan
	if err := json.Unmarshal(op.Plan, &plan); err != nil {
		s.failStorageVolumeOperation(ctx, op.ID, "Could not read the stored plan: "+err.Error())
		return err
	}
	enabled, _, err := s.storageProvisioningEnabled(ctx)
	if err != nil {
		s.failStorageVolumeOperation(ctx, op.ID, "Could not read the provisioning policy: "+err.Error())
		return err
	}
	if !enabled {
		err = fmt.Errorf("host disk provisioning is disabled")
		s.failStorageVolumeOperation(ctx, op.ID, err.Error())
		return err
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'running', step = 'Preparing host operation', progress = 5,
		    started_at = COALESCE(started_at, now()), updated_at = now(), error = ''
		WHERE id = $1 AND status IN ('queued', 'running')
	`, op.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Storage runner started: "+op.Kind)
	switch op.Kind {
	case "volume_add":
		err = s.runVolumeAdd(ctx, op, plan)
	case "volume_adopt":
		err = s.runVolumeAdopt(ctx, op, plan)
	case "volume_extend":
		err = s.runVolumeExtend(ctx, op, plan)
	case "volume_retire":
		err = s.runVolumeRetire(ctx, op, plan)
	case "volume_wipe":
		err = s.runVolumeWipe(ctx, op, plan)
	default:
		err = fmt.Errorf("unsupported host operation %q", op.Kind)
	}
	if err != nil {
		s.failStorageVolumeOperation(ctx, op.ID, err.Error())
		s.auditStorageOperation(ctx, op, "storage.volume_failed", "Host storage operation failed", map[string]any{"kind": op.Kind, "error": truncateText(err.Error(), 500)})
		return err
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'succeeded', step = 'Completed', progress = 100,
		    finished_at = now(), updated_at = now()
		WHERE id = $1
	`, op.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Host operation completed.")
	s.auditStorageOperation(ctx, op, "storage.volume_completed", "Completed a host storage operation", map[string]any{"kind": op.Kind})
	return nil
}

func (s *Server) failStorageVolumeOperation(ctx context.Context, id, message string) {
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'failed', step = 'Failed; review the runner log',
		    error = $2, finished_at = now(), updated_at = now()
		WHERE id = $1
	`, id, truncateText(message, 1000))
	s.appendStorageLog(ctx, id, "Failed: "+truncateText(message, 500))
}

func (s *Server) volumeOperationStep(ctx context.Context, id, step string, progress int) {
	if s.db == nil {
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET step = $2, progress = $3, updated_at = now()
		WHERE id = $1
	`, id, step, progress)
}

func (s *Server) runHostStep(ctx context.Context, opID, label string, timeout time.Duration, name string, args ...string) (commandResult, error) {
	s.appendStorageLog(ctx, opID, "$ "+name+" "+strings.Join(args, " "))
	result, err := storageHostCommand(ctx, timeout, name, args...)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr + " " + result.Stdout)
		if detail == "" {
			detail = err.Error()
		}
		return result, fmt.Errorf("%s: %s", label, truncateText(detail, 500))
	}
	return result, nil
}

// storageMountActive reports whether mountPath is itself a mountpoint.
// findmnt --target falls back to the containing filesystem, which would make a
// stale volume look mounted (and a verified UUID look like the root disk).
func (s *Server) storageMountActive(ctx context.Context, mountPath string) bool {
	result, err := storageHostCommand(ctx, 10*time.Second, "findmnt", "-n", "--mountpoint", mountPath)
	return err == nil && strings.TrimSpace(result.Stdout) != ""
}

func (s *Server) storageMountUnit(ctx context.Context, mountPath string) string {
	if result, err := storageHostCommand(ctx, 10*time.Second, "systemd-escape", "-p", "--suffix=mount", mountPath); err == nil {
		if unit := strings.TrimSpace(result.Stdout); unit != "" {
			return unit
		}
	}
	return storageMountUnitName(mountPath)
}

func (s *Server) writeStorageFstab(ctx context.Context, content, previous string) error {
	path := filepath.Clean(strings.TrimSpace(s.cfg.StorageFstab))
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("fstab path is not absolute")
	}
	mode := os.FileMode(0o644)
	uid, gid := 0, 0
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if ownerUID, ownerGID, ok := fileOwner(path); ok {
			uid, gid = ownerUID, ownerGID
		}
	}
	backupDir := filepath.Join(s.cfg.BackupDir, "config")
	if strings.TrimSpace(s.cfg.BackupDir) == "" {
		backupDir = filepath.Dir(path)
	}
	if err := os.MkdirAll(backupDir, 0o700); err == nil && previous != "" {
		_ = os.WriteFile(filepath.Join(backupDir, "fstab."+time.Now().UTC().Format("20060102-150405.000000")+".bak"), []byte(previous), 0o600)
	}
	if err := writeFileAtomic(path, content, mode, uid, gid); err != nil {
		return err
	}
	restore := func() {
		if previous == "" {
			_ = os.Remove(path)
			return
		}
		_ = writeFileAtomic(path, previous, mode, uid, gid)
		_, _ = storageHostCommand(ctx, 20*time.Second, "systemctl", "daemon-reload")
	}
	if result, err := storageHostCommand(ctx, 20*time.Second, "findmnt", "--verify"); err != nil {
		restore()
		return fmt.Errorf("fstab validation failed: %s", truncateText(strings.TrimSpace(result.Stderr+" "+result.Stdout), 500))
	}
	if result, err := storageHostCommand(ctx, 30*time.Second, "systemctl", "daemon-reload"); err != nil {
		restore()
		return fmt.Errorf("systemd daemon-reload failed: %s", truncateText(strings.TrimSpace(result.Stderr), 300))
	}
	return nil
}

func writeFileAtomic(path, content string, mode os.FileMode, uid, gid int) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".a8m-file-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return err
	}
	if err := chownPath(tempName, uid, gid); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func (s *Server) runVolumeAdd(ctx context.Context, op storageOperation, plan storageVolumePlan) error {
	enabled, _, err := s.storageProvisioningEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("host disk provisioning is disabled")
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return fmt.Errorf("%s", message)
	}
	disk, ok := findStorageDisk(devices, plan.DevicePath, plan.DeviceSerial, plan.DeviceWWN)
	if !ok || disk.Type != "disk" {
		return fmt.Errorf("the selected disk is no longer present")
	}
	if disk.SystemProtected {
		return fmt.Errorf("the selected disk is a system or virtual device")
	}
	if !plan.RequireDestroy && (disk.HasSignatures || disk.Mountpoint != "" || len(disk.Children) > 0) {
		return fmt.Errorf("the disk changed after planning; create a new plan")
	}
	if disk.Mountpoint != "" {
		return fmt.Errorf("the selected disk is mounted")
	}
	partition := storagePartitionDevice(disk.Path, 1)
	if !strings.HasPrefix(partition, disk.Path) {
		return fmt.Errorf("unsafe partition path")
	}
	mountPath, err := validateStorageMountPath(s.cfg.StorageRoot, plan.MountPath)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(mountPath); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("the mount path is not a directory")
		}
		entries, _ := os.ReadDir(mountPath)
		if len(entries) > 0 {
			return fmt.Errorf("the mount path is not empty")
		}
	}
	if err := os.MkdirAll(filepath.Dir(mountPath), 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(mountPath, 0o750); err != nil {
		return err
	}
	if uid, gid, ownerErr := mailOwnerLookup(); ownerErr == nil && uid >= 0 {
		_ = os.Chown(mountPath, 0, gid)
	}
	var unit string
	var previousFstab string
	mounted := false
	fstabWritten := false
	success := false
	defer func() {
		if success {
			return
		}
		if unit != "" {
			_, _ = storageHostCommand(context.Background(), 30*time.Second, "systemctl", "stop", unit)
		}
		if mounted {
			_, _ = storageHostCommand(context.Background(), 30*time.Second, "umount", mountPath)
		}
		// Only undo an fstab change this runner actually made. An earlier
		// failure (for example during mkfs) must never delete the host fstab.
		if fstabWritten {
			if previousFstab != "" {
				_ = writeFileAtomic(s.cfg.StorageFstab, previousFstab, 0o644, 0, 0)
				_, _ = storageHostCommand(context.Background(), 20*time.Second, "systemctl", "daemon-reload")
			} else if pathExists(s.cfg.StorageFstab) {
				_ = os.Remove(s.cfg.StorageFstab)
			}
		}
		_ = os.Remove(mountPath)
	}()
	s.volumeOperationStep(ctx, op.ID, "Partitioning "+disk.Path, 15)
	if _, err := s.runHostStep(ctx, op.ID, "wipe disk signatures", 5*time.Minute, "sgdisk", "--zap-all", disk.Path); err != nil {
		return err
	}
	if _, err := s.runHostStep(ctx, op.ID, "create GPT partition", 5*time.Minute,
		"sgdisk", "-n", "1:0:0", "-t", "1:8300", "-c", "1:a8m-"+plan.Name, disk.Path); err != nil {
		return err
	}
	if _, err := s.runHostStep(ctx, op.ID, "reread partition table", 2*time.Minute, "partprobe", disk.Path); err != nil {
		return err
	}
	if _, err := storageLookPath("udevadm"); err == nil {
		_, _ = s.runHostStep(ctx, op.ID, "settle udev", 2*time.Minute, "udevadm", "settle")
	}
	s.volumeOperationStep(ctx, op.ID, "Formatting "+partition, 35)
	if plan.FSType == "xfs" {
		if _, err := s.runHostStep(ctx, op.ID, "format XFS", 30*time.Minute, "mkfs.xfs", "-f", "-L", "a8m-"+plan.Name, partition); err != nil {
			return err
		}
	} else {
		if _, err := s.runHostStep(ctx, op.ID, "format ext4", 30*time.Minute, "mkfs.ext4", "-F", "-L", "a8m-"+plan.Name, "-m", "1", partition); err != nil {
			return err
		}
	}
	uuidResult, uuidErr := s.runHostStep(ctx, op.ID, "read filesystem UUID", 30*time.Second, "blkid", "-s", "UUID", "-o", "value", partition)
	uuid := strings.TrimSpace(uuidResult.Stdout)
	if uuidErr != nil || uuid == "" {
		lsblkResult, err := storageHostCommand(ctx, 30*time.Second, "lsblk", "-no", "UUID", partition)
		uuid = strings.TrimSpace(lsblkResult.Stdout)
		if err != nil || uuid == "" {
			return fmt.Errorf("could not read the new filesystem UUID")
		}
	}
	s.volumeOperationStep(ctx, op.ID, "Writing managed fstab entry", 55)
	if raw, readErr := os.ReadFile(s.cfg.StorageFstab); readErr == nil {
		previousFstab = string(raw)
	}
	block := storageFstabBlock(plan.Name, uuid, mountPath, plan.FSType)
	updated, err := upsertStorageFstabBlock(previousFstab, block, plan.Name, uuid)
	if err != nil {
		return err
	}
	if err := s.writeStorageFstab(ctx, updated, previousFstab); err != nil {
		return err
	}
	fstabWritten = true
	unit = s.storageMountUnit(ctx, mountPath)
	s.volumeOperationStep(ctx, op.ID, "Mounting through systemd", 70)
	if _, err := s.runHostStep(ctx, op.ID, "start mount unit", 2*time.Minute, "systemctl", "start", unit); err != nil {
		return err
	}
	mounted = true
	if result, err := storageHostCommand(ctx, 30*time.Second, "findmnt", "-n", "-o", "UUID", "--mountpoint", mountPath); err != nil || strings.TrimSpace(result.Stdout) != uuid {
		return fmt.Errorf("the mounted filesystem UUID did not match the new filesystem")
	}
	testPath := filepath.Join(mountPath, ".a8m-write-test")
	if err := os.WriteFile(testPath, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("mount write test failed: %w", err)
	}
	_ = os.Remove(testPath)
	s.volumeOperationStep(ctx, op.ID, "Registering volume", 90)
	if _, err := s.db.Exec(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_uuid, device_path, fs_type, role, managed, state,
		   size_bytes, mount_options, fstab_managed, device_serial, device_wwn,
		   mount_unit, reboot_verified, verified_at, created_by, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, 'mail', true, 'online', $6,
		        'defaults,nofail,noatime,nodev,nosuid', true, $7, $8, $9, false, now(), 'a8m', now())
	`, plan.Name, mountPath, uuid, partition, plan.FSType, disk.SizeBytes,
		disk.Serial, disk.WWN, unit); err != nil {
		return err
	}
	success = true
	s.appendStorageLog(ctx, op.ID, "Registered volume "+plan.Name+" at "+mountPath+".")
	return nil
}

func (s *Server) runVolumeAdopt(ctx context.Context, op storageOperation, plan storageVolumePlan) error {
	result, err := storageHostCommand(ctx, 10*time.Second, "findmnt", "-n", "-o", "SOURCE,FSTYPE,OPTIONS", "--mountpoint", plan.MountPath)
	if err != nil {
		return fmt.Errorf("the path is not a mounted filesystem")
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) < 2 {
		return fmt.Errorf("could not determine the mounted filesystem")
	}
	source, fsType := fields[0], strings.ToLower(fields[1])
	if fsType != "ext4" && fsType != "xfs" && fsType != "nfs" && fsType != "nfs4" {
		return fmt.Errorf("only ext4, xfs and NFS mounts can be adopted")
	}
	uuid := ""
	if uuidResult, err := storageHostCommand(ctx, 10*time.Second, "findmnt", "-n", "-o", "UUID", "--mountpoint", plan.MountPath); err == nil {
		uuid = strings.TrimSpace(uuidResult.Stdout)
	}
	stat, err := filesystemStatLookup(plan.MountPath)
	if err != nil {
		return err
	}
	unit := s.storageMountUnit(ctx, plan.MountPath)
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_uuid, device_path, fs_type, role, managed, state,
		   size_bytes, mount_options, fstab_managed, mount_unit, reboot_verified,
		   verified_at, created_by, last_seen_at)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, 'mail', true, 'online', $6,
		        $7, false, $8, false, now(), 'adopted', now())
	`, plan.Name, plan.MountPath, uuid, source, fsType, stat.TotalBytes,
		strings.Join(fields[2:], " "), unit)
	if err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Adopted mounted volume "+plan.Name+" at "+plan.MountPath+".")
	return nil
}

func (s *Server) runVolumeExtend(ctx context.Context, op storageOperation, plan storageVolumePlan) error {
	volume, err := s.loadStorageVolume(ctx, plan.VolumeID)
	if err != nil {
		return err
	}
	if volume.State != "online" {
		return fmt.Errorf("the volume is not online")
	}
	if !s.storageMountActive(ctx, volume.MountPath) {
		return fmt.Errorf("the volume is not mounted")
	}
	before, err := filesystemStatLookup(volume.MountPath)
	if err != nil {
		return err
	}
	if plan.Mode == "lvm_extend" && plan.LVM != nil {
		devices, supported, message := s.listStorageDisks(ctx)
		if !supported {
			return fmt.Errorf("%s", message)
		}
		disk, ok := findStorageDisk(devices, plan.LVM.NewDevice, plan.LVM.NewSerial, plan.LVM.NewWWN)
		if !ok || disk.HasSignatures || disk.Mountpoint != "" || len(disk.Children) > 0 || disk.SystemProtected {
			return fmt.Errorf("the new disk changed after planning")
		}
		s.volumeOperationStep(ctx, op.ID, "Adding physical volume", 20)
		if _, err := s.runHostStep(ctx, op.ID, "pvcreate", 10*time.Minute, "pvcreate", plan.LVM.NewDevice); err != nil {
			return err
		}
		if _, err := s.runHostStep(ctx, op.ID, "extend volume group", 5*time.Minute, "vgextend", plan.LVM.VG, plan.LVM.NewDevice); err != nil {
			return err
		}
		if _, err := s.runHostStep(ctx, op.ID, "extend logical volume", 10*time.Minute, "lvextend", "-l", "+100%FREE", plan.LVM.LVPath); err != nil {
			return err
		}
	} else if plan.Mode == "lvm_grow" && plan.LVM != nil {
		s.volumeOperationStep(ctx, op.ID, "Extending logical volume", 30)
		if _, err := s.runHostStep(ctx, op.ID, "lvextend", 10*time.Minute, "lvextend", "-l", "+100%FREE", plan.LVM.LVPath); err != nil {
			return err
		}
	} else if plan.GrowDisk != "" {
		s.volumeOperationStep(ctx, op.ID, "Growing partition", 25)
		if _, err := s.runHostStep(ctx, op.ID, "growpart", 5*time.Minute, "growpart", plan.GrowDisk, plan.GrowPartition); err != nil {
			return err
		}
	}
	s.volumeOperationStep(ctx, op.ID, "Growing filesystem", 60)
	if plan.ResizeMount != "" {
		if _, err := s.runHostStep(ctx, op.ID, "xfs_growfs", 10*time.Minute, "xfs_growfs", plan.ResizeMount); err != nil {
			return err
		}
	} else if plan.ResizeDevice != "" {
		if _, err := s.runHostStep(ctx, op.ID, "resize2fs", 10*time.Minute, "resize2fs", plan.ResizeDevice); err != nil {
			return err
		}
	} else if plan.LVM != nil {
		if plan.FSType == "xfs" {
			if _, err := s.runHostStep(ctx, op.ID, "xfs_growfs", 10*time.Minute, "xfs_growfs", volume.MountPath); err != nil {
				return err
			}
		} else {
			if _, err := s.runHostStep(ctx, op.ID, "resize2fs", 10*time.Minute, "resize2fs", plan.LVM.LVPath); err != nil {
				return err
			}
		}
	}
	after, err := filesystemStatLookup(volume.MountPath)
	if err != nil {
		return err
	}
	if before.TotalBytes > 0 && after.TotalBytes <= before.TotalBytes {
		return fmt.Errorf("the filesystem did not grow; review the underlying disk or volume group free space")
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_volumes
		SET size_bytes = $2, verified_at = now(), last_seen_at = now(), updated_at = now()
		WHERE id = $1
	`, volume.ID, after.TotalBytes); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, fmt.Sprintf("Volume %s grew from %s to %s.", volume.Name, humanBytes(before.TotalBytes), humanBytes(after.TotalBytes)))
	return nil
}

func (s *Server) runVolumeRetire(ctx context.Context, op storageOperation, plan storageVolumePlan) error {
	volume, err := s.loadStorageVolume(ctx, plan.VolumeID)
	if err != nil {
		return err
	}
	if volume.Name == "legacy" || sameStoragePath(volume.MountPath, s.cfg.VmailRoot) {
		return fmt.Errorf("the legacy mail root cannot be retired")
	}
	mailboxes, domains, defaults, err := s.storageVolumeAssignedCounts(ctx, volume.ID)
	if err != nil {
		return err
	}
	if assignments := mailboxes + domains + defaults; assignments > 0 {
		return fmt.Errorf("the volume still has %d assignment(s); evacuate it first", assignments)
	}
	unit := volume.MountUnit
	if unit == "" {
		unit = s.storageMountUnit(ctx, volume.MountPath)
	}
	s.volumeOperationStep(ctx, op.ID, "Stopping mount", 30)
	if _, err := storageHostCommand(ctx, 2*time.Minute, "systemctl", "stop", unit); err != nil {
		if _, umountErr := storageHostCommand(ctx, 2*time.Minute, "umount", volume.MountPath); umountErr != nil {
			return fmt.Errorf("could not stop the mount unit or unmount the volume")
		}
	}
	if s.storageMountActive(ctx, volume.MountPath) {
		return fmt.Errorf("the volume is still mounted")
	}
	if volume.FstabManaged {
		raw, err := os.ReadFile(s.cfg.StorageFstab)
		if err != nil {
			return err
		}
		updated, err := removeStorageFstabBlocks(string(raw), volume.Name, volume.FSUUID)
		if err != nil {
			return err
		}
		if err := s.writeStorageFstab(ctx, updated, string(raw)); err != nil {
			return err
		}
	} else {
		s.appendStorageLog(ctx, op.ID, "Adopted volume has no managed fstab entry; the existing fstab line was left untouched.")
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_volumes
		SET state = 'retired', fstab_managed = false, updated_at = now()
		WHERE id = $1
	`, volume.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Volume "+volume.Name+" retired; no data was formatted or deleted.")
	return nil
}

func (s *Server) runVolumeWipe(ctx context.Context, op storageOperation, plan storageVolumePlan) error {
	volume, err := s.loadStorageVolume(ctx, plan.VolumeID)
	if err != nil {
		return err
	}
	if volume.Name == "legacy" || sameStoragePath(volume.MountPath, s.cfg.VmailRoot) {
		return fmt.Errorf("the legacy mail root cannot be wiped")
	}
	if volume.State != "retired" {
		return fmt.Errorf("the volume must be retired before it can be wiped")
	}
	mailboxes, domains, defaults, err := s.storageVolumeAssignedCounts(ctx, volume.ID)
	if err != nil {
		return err
	}
	if assignments := mailboxes + domains + defaults; assignments > 0 {
		return fmt.Errorf("the volume still has %d assignment(s); evacuate it before wiping", assignments)
	}
	if s.storageMountActive(ctx, volume.MountPath) {
		return fmt.Errorf("the volume is still mounted")
	}
	devicePath := strings.TrimSpace(plan.DevicePath)
	if devicePath == "" || !strings.HasPrefix(devicePath, "/dev/") || devicePath != strings.TrimSpace(volume.DevicePath) {
		return fmt.Errorf("the planned device no longer matches the registered volume")
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return fmt.Errorf("%s", message)
	}
	device, chain, ok := findStorageDevicePath(devices, devicePath)
	if !ok {
		return fmt.Errorf("the volume device %s is no longer present", devicePath)
	}
	for _, entry := range chain {
		if entry.SystemProtected {
			return fmt.Errorf("%s is a system or virtual device and cannot be wiped", devicePath)
		}
	}
	if !storageDeviceIdentityMatches(chain, plan.DeviceSerial, plan.DeviceWWN) {
		return fmt.Errorf("the device identity at %s no longer matches the plan", devicePath)
	}
	if device.Path != devicePath {
		return fmt.Errorf("the volume device path changed after planning")
	}
	s.volumeOperationStep(ctx, op.ID, "Wiping filesystem signatures", 40)
	if _, err := s.runHostStep(ctx, op.ID, "wipefs", 5*time.Minute, "wipefs", "-a", devicePath); err != nil {
		return err
	}
	if _, err := storageLookPath("udevadm"); err == nil {
		_, _ = s.runHostStep(ctx, op.ID, "settle udev", 2*time.Minute, "udevadm", "settle")
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_volumes
		SET state = 'wiped', fs_uuid = '', verified_at = now(), last_seen_at = now(), updated_at = now()
		WHERE id = $1
	`, volume.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Wiped filesystem signatures on "+devicePath+". File contents were not securely erased.")
	return nil
}
