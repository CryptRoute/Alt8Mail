package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) RunStorageSwapOperation(ctx context.Context, op storageOperation) error {
	var plan storageSwapPlan
	if err := json.Unmarshal(op.Plan, &plan); err != nil {
		s.failStorageVolumeOperation(ctx, op.ID, "Could not read the stored swap plan: "+err.Error())
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
		SET status = 'running', step = 'Preparing swap', progress = 5,
		    started_at = COALESCE(started_at, now()), updated_at = now(), error = ''
		WHERE id = $1 AND status IN ('queued', 'running')
	`, op.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Storage runner started: "+op.Kind)
	switch plan.Mode {
	case "file":
		err = s.runSwapFileCreate(ctx, op, plan)
	case "device":
		err = s.runSwapDeviceCreate(ctx, op, plan)
	default:
		err = fmt.Errorf("unsupported swap mode %q", plan.Mode)
	}
	if err != nil {
		s.failStorageVolumeOperation(ctx, op.ID, err.Error())
		s.auditStorageOperation(ctx, op, "storage.swap_failed", "Swap creation failed", map[string]any{"target": plan.Target, "error": truncateText(err.Error(), 500)})
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
	s.appendStorageLog(ctx, op.ID, "Swap is active and recorded in fstab.")
	s.auditStorageOperation(ctx, op, "storage.swap_completed", "Created swap", map[string]any{
		"mode": plan.Mode, "target": plan.Target, "sizeBytes": plan.SizeBytes,
	})
	return nil
}

func (s *Server) runSwapFileCreate(ctx context.Context, op storageOperation, plan storageSwapPlan) error {
	volume, err := s.loadStorageVolume(ctx, plan.VolumeID)
	if err != nil {
		return fmt.Errorf("the source volume is no longer registered")
	}
	if volume.State == "retired" || volume.State == "wiped" || !s.storageMountActive(ctx, volume.MountPath) {
		return fmt.Errorf("the source volume is not online and mounted")
	}
	fsType := strings.ToLower(strings.TrimSpace(volume.FSType))
	if fsType != "ext4" && fsType != "xfs" {
		return fmt.Errorf("swap files require ext4 or XFS")
	}
	filePath := filepath.Clean(plan.FilePath)
	if !pathWithin(volume.MountPath, filePath) || filepath.Base(filePath) != ".a8m-swapfile" {
		return fmt.Errorf("the swap file path is outside the selected volume")
	}
	if info, statErr := os.Lstat(filePath); statErr == nil {
		_ = info
		return fmt.Errorf("a swap file already exists at %s", filePath)
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		return err
	}
	stat, err := filesystemStatLookup(volume.MountPath)
	if err != nil {
		return fmt.Errorf("the volume is no longer readable: %w", err)
	}
	reserve := storageSwapFileReserve(policy, stat.TotalBytes)
	if plan.SizeBytes < storageSwapMinBytes || plan.SizeBytes > storageSwapMaxBytes {
		return fmt.Errorf("the planned swap size is outside the allowed range")
	}
	if stat.FreeBytes <= reserve || plan.SizeBytes > stat.FreeBytes-reserve {
		return fmt.Errorf("the volume no longer has enough free space after the capacity reserve")
	}
	if s.swapPathActive(filePath) {
		return fmt.Errorf("that swap file is already active")
	}
	previousFstab := ""
	if raw, readErr := os.ReadFile(s.cfg.StorageFstab); readErr == nil {
		previousFstab = string(raw)
	}
	createdFile := false
	fstabWritten := false
	active := false
	success := false
	defer func() {
		if success {
			return
		}
		if active {
			_, _ = storageHostCommand(context.Background(), 2*time.Minute, "swapoff", filePath)
		}
		if fstabWritten {
			s.restoreStorageSwapFstab(context.Background(), previousFstab)
		}
		if createdFile {
			_ = os.Remove(filePath)
		}
	}()

	s.volumeOperationStep(ctx, op.ID, "Allocating swap file", 20)
	if _, err := s.runHostStep(ctx, op.ID, "allocate swap file", 10*time.Minute,
		"fallocate", "-l", strconv.FormatUint(plan.SizeBytes, 10), filePath); err != nil {
		return err
	}
	createdFile = true
	if _, err := s.runHostStep(ctx, op.ID, "restrict swap file permissions", 30*time.Second, "chmod", "600", filePath); err != nil {
		return err
	}
	s.volumeOperationStep(ctx, op.ID, "Formatting swap", 35)
	if _, err := s.runHostStep(ctx, op.ID, "create swap signature", 10*time.Minute, "mkswap", "-L", plan.Label, filePath); err != nil {
		return err
	}
	s.volumeOperationStep(ctx, op.ID, "Writing managed fstab entry", 55)
	block := storageSwapFstabBlock(plan.Name, filePath, filePath)
	updated, err := upsertStorageSwapFstabBlock(previousFstab, block, plan.Name, filePath)
	if err != nil {
		return err
	}
	if err := s.writeStorageFstab(ctx, updated, previousFstab); err != nil {
		return err
	}
	fstabWritten = true
	s.volumeOperationStep(ctx, op.ID, "Activating swap", 75)
	if _, err := s.runHostStep(ctx, op.ID, "activate swap", 2*time.Minute, "swapon", filePath); err != nil {
		return err
	}
	active = true
	if !s.swapPathActive(filePath) {
		return fmt.Errorf("swap activation did not appear in /proc/swaps")
	}
	success = true
	s.appendStorageLog(ctx, op.ID, "Swap file "+filePath+" is active and persistent.")
	return nil
}

func (s *Server) runSwapDeviceCreate(ctx context.Context, op storageOperation, plan storageSwapPlan) error {
	devicePath := strings.TrimSpace(plan.DevicePath)
	if devicePath == "" || !strings.HasPrefix(devicePath, "/dev/") {
		return fmt.Errorf("the planned device path is invalid")
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return fmt.Errorf("%s", message)
	}
	device, ok := findStorageDisk(devices, devicePath, plan.DeviceSerial, plan.DeviceWWN)
	if !ok || device.Type != "disk" || device.Path != devicePath {
		return fmt.Errorf("the selected disk is no longer present or has changed")
	}
	if device.SystemProtected || device.Mountpoint != "" || len(device.Children) > 0 {
		return fmt.Errorf("the selected disk is protected, mounted or partitioned")
	}
	if !storageDeviceIdentityMatches([]storageDisk{device}, plan.DeviceSerial, plan.DeviceWWN) {
		return fmt.Errorf("the disk identity no longer matches the plan")
	}
	if s.swapPathActive(device.Path) {
		return fmt.Errorf("the selected disk is already active swap")
	}
	previousFstab := ""
	if raw, readErr := os.ReadFile(s.cfg.StorageFstab); readErr == nil {
		previousFstab = string(raw)
	}
	fstabWritten := false
	active := false
	success := false
	defer func() {
		if success {
			return
		}
		if active {
			_, _ = storageHostCommand(context.Background(), 2*time.Minute, "swapoff", device.Path)
		}
		if fstabWritten {
			s.restoreStorageSwapFstab(context.Background(), previousFstab)
		}
		// A failed attempt must not leave a partial swap signature behind.
		_, _ = storageHostCommand(context.Background(), 2*time.Minute, "wipefs", "-a", device.Path)
	}()

	s.volumeOperationStep(ctx, op.ID, "Erasing disk signatures", 20)
	if _, err := s.runHostStep(ctx, op.ID, "erase disk signatures", 5*time.Minute, "wipefs", "-a", device.Path); err != nil {
		return err
	}
	s.volumeOperationStep(ctx, op.ID, "Creating swap signature", 35)
	if _, err := s.runHostStep(ctx, op.ID, "create swap signature", 10*time.Minute, "mkswap", "-L", plan.Label, device.Path); err != nil {
		return err
	}
	uuidResult, err := s.runHostStep(ctx, op.ID, "read swap UUID", 30*time.Second, "blkid", "-s", "UUID", "-o", "value", device.Path)
	if err != nil {
		return err
	}
	swapUUID := strings.TrimSpace(uuidResult.Stdout)
	if swapUUID == "" {
		return fmt.Errorf("could not read the new swap UUID")
	}
	s.volumeOperationStep(ctx, op.ID, "Writing managed fstab entry", 55)
	block := storageSwapFstabBlock(plan.Name, "UUID="+swapUUID, device.Path)
	updated, err := upsertStorageSwapFstabBlock(previousFstab, block, plan.Name, device.Path)
	if err != nil {
		return err
	}
	if err := s.writeStorageFstab(ctx, updated, previousFstab); err != nil {
		return err
	}
	fstabWritten = true
	s.volumeOperationStep(ctx, op.ID, "Activating swap", 75)
	if _, err := s.runHostStep(ctx, op.ID, "activate swap", 2*time.Minute, "swapon", device.Path); err != nil {
		return err
	}
	active = true
	if !s.swapPathActive(device.Path) {
		return fmt.Errorf("swap activation did not appear in /proc/swaps")
	}
	success = true
	s.appendStorageLog(ctx, op.ID, "Swap device "+device.Path+" is active and persistent.")
	return nil
}

func (s *Server) swapPathActive(path string) bool {
	entries, err := readStorageSwapEntries()
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if sameStoragePath(entry.Path, path) {
			return true
		}
	}
	return false
}

func (s *Server) restoreStorageSwapFstab(ctx context.Context, previous string) {
	path := filepath.Clean(strings.TrimSpace(s.cfg.StorageFstab))
	if path == "" {
		return
	}
	mode := os.FileMode(0o644)
	uid, gid := 0, 0
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if ownerUID, ownerGID, ok := fileOwner(path); ok {
			uid, gid = ownerUID, ownerGID
		}
	}
	if previous != "" {
		_ = writeFileAtomic(path, previous, mode, uid, gid)
	} else if pathExists(path) {
		_ = os.Remove(path)
	}
	_, _ = storageHostCommand(ctx, 20*time.Second, "systemctl", "daemon-reload")
}
