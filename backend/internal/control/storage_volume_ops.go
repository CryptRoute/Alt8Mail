package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var storageLookPath = exec.LookPath
var storageHostCommand = runCommand

type storageLVMPlan struct {
	VG          string `json:"vg"`
	LV          string `json:"lv"`
	LVPath      string `json:"lvPath"`
	MountPath   string `json:"mountPath"`
	FSType      string `json:"fsType"`
	NewDevice   string `json:"newDevice,omitempty"`
	NewSerial   string `json:"newSerial,omitempty"`
	NewWWN      string `json:"newWwn,omitempty"`
	NewDiskSize uint64 `json:"newDiskSize,omitempty"`
}

type storageVolumePlan struct {
	Kind           string          `json:"kind"`
	Mode           string          `json:"mode"`
	Name           string          `json:"name,omitempty"`
	MountPath      string          `json:"mountPath,omitempty"`
	DevicePath     string          `json:"devicePath,omitempty"`
	PartitionPath  string          `json:"partitionPath,omitempty"`
	DeviceSerial   string          `json:"deviceSerial,omitempty"`
	DeviceWWN      string          `json:"deviceWwn,omitempty"`
	FSType         string          `json:"fsType,omitempty"`
	VolumeID       string          `json:"volumeId,omitempty"`
	VolumeName     string          `json:"volumeName,omitempty"`
	DiskSizeBytes  uint64          `json:"diskSizeBytes,omitempty"`
	OldSizeBytes   uint64          `json:"oldSizeBytes,omitempty"`
	RequireDestroy bool            `json:"requireDestroy"`
	ConfirmValue   string          `json:"confirmValue"`
	Commands       []string        `json:"commands"`
	Warnings       []string        `json:"warnings"`
	GrowDisk       string          `json:"growDisk,omitempty"`
	GrowPartition  string          `json:"growPartition,omitempty"`
	ResizeDevice   string          `json:"resizeDevice,omitempty"`
	ResizeMount    string          `json:"resizeMount,omitempty"`
	LVM            *storageLVMPlan `json:"lvm,omitempty"`
}

type storageHostCapabilities struct {
	Supported    bool            `json:"supported"`
	CanPartition bool            `json:"canPartition"`
	Reason       string          `json:"reason,omitempty"`
	StorageRoot  string          `json:"storageRoot"`
	FstabPath    string          `json:"fstabPath"`
	Tools        map[string]bool `json:"tools"`
	LVM          bool            `json:"lvm"`
	Provisioning bool            `json:"provisioningEnabled"`
	EnvEnabled   bool            `json:"envEnabled"`
}

type storageVolumePlanRequest struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	DevicePath   string `json:"devicePath"`
	DeviceSerial string `json:"deviceSerial"`
	DeviceWWN    string `json:"deviceWwn"`
	FSType       string `json:"fsType"`
	Mode         string `json:"mode"`
	VolumeID     string `json:"volumeId"`
}

type storageVolumePolicyRequest struct {
	ProvisioningEnabled *bool  `json:"provisioningEnabled"`
	AdvisoryPercent     *int   `json:"advisoryPercent"`
	WarningPercent      *int   `json:"warningPercent"`
	SeriousPercent      *int   `json:"seriousPercent"`
	CriticalPercent     *int   `json:"criticalPercent"`
	EmergencyPercent    *int   `json:"emergencyPercent"`
	ReservePercent      *int   `json:"reservePercent"`
	MinFreeBytes        *int64 `json:"minFreeBytes"`
	MigrationGraceHours *int   `json:"migrationGraceHours"`
	MaxConcurrentMoves  *int   `json:"maxConcurrentMoves"`
	AlertRearmHours     *int   `json:"alertRearmHours"`
	ForecastAlertDays   *int   `json:"forecastAlertDays"`
	GuardEnabled        *bool  `json:"guardEnabled"`
}

func (s *Server) registerStorageVolumeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/storage/host", s.require("storage.read", s.handleStorageHost))
	mux.HandleFunc("PATCH /control/api/storage/policy", s.require("storage.manage", s.handleStoragePolicy))
	mux.HandleFunc("POST /control/api/storage/volumes/plan", s.require("storage.manage", s.handleStorageVolumePlan))
	mux.HandleFunc("POST /control/api/storage/volumes/execute", s.require("storage.manage", s.handleStorageVolumeExecute))
	mux.HandleFunc("POST /control/api/storage/volumes/{id}/verify", s.require("storage.manage", s.handleStorageVolumeVerify))
}

func (s *Server) storageHostCapabilities(ctx context.Context) storageHostCapabilities {
	caps := storageHostCapabilities{
		StorageRoot: s.cfg.StorageRoot,
		FstabPath:   s.cfg.StorageFstab,
		Tools:       map[string]bool{},
		EnvEnabled:  s.cfg.StorageProvisioning,
	}
	if storageHostGOOS != "linux" {
		caps.Reason = "Host disk provisioning is available on the Linux control host only."
		return caps
	}
	for _, tool := range []string{
		"lsblk", "findmnt", "blkid", "systemd-escape", "rsync", "sgdisk", "parted",
		"mkfs.ext4", "mkfs.xfs", "growpart", "resize2fs", "xfs_growfs", "lvs",
		"pvcreate", "vgextend", "lvextend", "wipefs", "umount", "mount", "systemctl", "udevadm", "systemd-run",
	} {
		_, err := storageLookPath(tool)
		caps.Tools[tool] = err == nil
	}
	caps.Supported = caps.Tools["lsblk"] && caps.Tools["findmnt"]
	caps.CanPartition = caps.Tools["sgdisk"] || caps.Tools["parted"]
	caps.LVM = caps.Tools["lvs"] && caps.Tools["pvcreate"] && caps.Tools["vgextend"] && caps.Tools["lvextend"]
	if !caps.Supported {
		caps.Reason = "lsblk and findmnt are required on the Linux host."
	}
	return caps
}

func (s *Server) storageProvisioningEnabled(ctx context.Context) (bool, storagePolicy, error) {
	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		return false, storagePolicy{}, err
	}
	return policy.ProvisioningEnabled && s.cfg.StorageProvisioning, policy, nil
}

func (s *Server) handleStorageHost(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	caps := s.storageHostCapabilities(r.Context())
	policy, err := s.storagePolicyRow(r.Context())
	if err == nil {
		caps.Provisioning = policy.ProvisioningEnabled && s.cfg.StorageProvisioning
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"capabilities": caps,
		"policy":       policy,
	})
}

func (s *Server) handleStoragePolicy(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body storageVolumePolicyRequest
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	policy, err := s.storagePolicyRow(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	changed := false
	if body.ProvisioningEnabled != nil {
		if !sess.User.PermSet.Has("storage.destroy") {
			writeError(w, http.StatusForbidden, "PERMISSION", "Only a superadmin can change host disk provisioning")
			return
		}
		if *body.ProvisioningEnabled {
			caps := s.storageHostCapabilities(r.Context())
			if !caps.Supported {
				writeError(w, http.StatusConflict, "HOST", caps.Reason)
				return
			}
		}
		policy.ProvisioningEnabled = *body.ProvisioningEnabled
		changed = true
	}
	if body.AdvisoryPercent != nil {
		policy.AdvisoryPercent = *body.AdvisoryPercent
		changed = true
	}
	if body.WarningPercent != nil {
		policy.WarningPercent = *body.WarningPercent
		changed = true
	}
	if body.SeriousPercent != nil {
		policy.SeriousPercent = *body.SeriousPercent
		changed = true
	}
	if body.CriticalPercent != nil {
		policy.CriticalPercent = *body.CriticalPercent
		changed = true
	}
	if body.EmergencyPercent != nil {
		policy.EmergencyPercent = *body.EmergencyPercent
		changed = true
	}
	if body.ReservePercent != nil {
		policy.ReservePercent = *body.ReservePercent
		changed = true
	}
	if body.MinFreeBytes != nil {
		policy.MinFreeBytes = *body.MinFreeBytes
		changed = true
	}
	if body.MigrationGraceHours != nil {
		policy.MigrationGraceHours = *body.MigrationGraceHours
		changed = true
	}
	if body.MaxConcurrentMoves != nil {
		policy.MaxConcurrentMoves = *body.MaxConcurrentMoves
		changed = true
	}
	if body.AlertRearmHours != nil {
		policy.AlertRearmHours = *body.AlertRearmHours
		changed = true
	}
	if body.ForecastAlertDays != nil {
		policy.ForecastAlertDays = *body.ForecastAlertDays
		changed = true
	}
	if body.GuardEnabled != nil {
		policy.GuardEnabled = *body.GuardEnabled
		changed = true
	}
	if !changed {
		writeError(w, http.StatusBadRequest, "POLICY", "Provide at least one policy value")
		return
	}
	if err := validateStoragePolicy(policy); err != nil {
		writeError(w, http.StatusBadRequest, "POLICY", err.Error())
		return
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE control.storage_policy
		SET advisory_percent = $1, warning_percent = $2, serious_percent = $3,
		    critical_percent = $4, emergency_percent = $5, reserve_percent = $6,
		    min_free_bytes = $7, migration_grace_hours = $8, max_concurrent_moves = $9,
		    alert_rearm_hours = $10, forecast_alert_days = $11, guard_enabled = $12,
		    provisioning_enabled = $13, updated_at = now()
		WHERE id = true
	`, policy.AdvisoryPercent, policy.WarningPercent, policy.SeriousPercent,
		policy.CriticalPercent, policy.EmergencyPercent, policy.ReservePercent,
		policy.MinFreeBytes, policy.MigrationGraceHours, policy.MaxConcurrentMoves,
		policy.AlertRearmHours, policy.ForecastAlertDays, policy.GuardEnabled,
		policy.ProvisioningEnabled); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "storage.policy_changed", "global", "Updated storage capacity policy", map[string]any{
		"advisoryPercent": policy.AdvisoryPercent, "warningPercent": policy.WarningPercent,
		"seriousPercent": policy.SeriousPercent, "criticalPercent": policy.CriticalPercent,
		"emergencyPercent": policy.EmergencyPercent, "reservePercent": policy.ReservePercent,
		"minFreeBytes": policy.MinFreeBytes, "guardEnabled": policy.GuardEnabled,
		"provisioningEnabled": policy.ProvisioningEnabled,
	})
	policy, _ = s.storagePolicyRow(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": policy})
}

func validateStoragePolicy(policy storagePolicy) error {
	if policy.AdvisoryPercent < 1 || policy.AdvisoryPercent > 99 ||
		policy.WarningPercent < 1 || policy.WarningPercent > 100 ||
		policy.SeriousPercent < 1 || policy.SeriousPercent > 100 ||
		policy.CriticalPercent < 1 || policy.CriticalPercent > 100 ||
		policy.EmergencyPercent < 1 || policy.EmergencyPercent > 100 {
		return fmt.Errorf("capacity thresholds must be between 1 and 100")
	}
	if !(policy.AdvisoryPercent < policy.WarningPercent &&
		policy.WarningPercent < policy.SeriousPercent &&
		policy.SeriousPercent < policy.CriticalPercent &&
		policy.CriticalPercent < policy.EmergencyPercent) {
		return fmt.Errorf("capacity thresholds must increase from advisory to emergency")
	}
	if policy.ReservePercent < 0 || policy.ReservePercent > 50 {
		return fmt.Errorf("reservePercent must be between 0 and 50")
	}
	if policy.MinFreeBytes < 0 {
		return fmt.Errorf("minFreeBytes must not be negative")
	}
	if policy.MigrationGraceHours < 1 || policy.MigrationGraceHours > 336 {
		return fmt.Errorf("migrationGraceHours must be between 1 and 336")
	}
	if policy.MaxConcurrentMoves < 1 || policy.MaxConcurrentMoves > 16 {
		return fmt.Errorf("maxConcurrentMoves must be between 1 and 16")
	}
	if policy.AlertRearmHours < 1 || policy.AlertRearmHours > 720 {
		return fmt.Errorf("alertRearmHours must be between 1 and 720")
	}
	if policy.ForecastAlertDays < 1 || policy.ForecastAlertDays > 365 {
		return fmt.Errorf("forecastAlertDays must be between 1 and 365")
	}
	return nil
}

func (s *Server) loadStorageVolume(ctx context.Context, id string) (storageVolume, error) {
	return scanStorageVolume(s.db.QueryRow(ctx, `SELECT `+storageVolumeColumns+` FROM control.storage_volumes WHERE id = $1`, id))
}

func (s *Server) storageVolumeNameTaken(ctx context.Context, name, mountPath string) error {
	var count int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM control.storage_volumes WHERE name = $1 OR mount_path = $2
	`, name, mountPath).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("a volume with that name or mount path is already registered")
	}
	return nil
}

func findStorageDisk(devices []storageDisk, path, serial, wwn string) (storageDisk, bool) {
	path = strings.TrimSpace(path)
	serial = strings.TrimSpace(serial)
	wwn = strings.TrimSpace(wwn)
	var walk func([]storageDisk) (storageDisk, bool)
	walk = func(list []storageDisk) (storageDisk, bool) {
		for _, device := range list {
			if path != "" && device.Path == path {
				return device, true
			}
			if serial != "" && device.Serial != "" && strings.EqualFold(device.Serial, serial) {
				return device, true
			}
			if wwn != "" && device.WWN != "" && strings.EqualFold(device.WWN, wwn) {
				return device, true
			}
			if found, ok := walk(device.Children); ok {
				return found, true
			}
		}
		return storageDisk{}, false
	}
	return walk(devices)
}

// findStorageDevicePath resolves one exact device path and returns the path
// from the top-level disk down to that device. Wiping must never widen a
// partition path to its parent disk, so identity matching happens only after
// the exact path has been found.
func findStorageDevicePath(devices []storageDisk, path string) (storageDisk, []storageDisk, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return storageDisk{}, nil, false
	}
	var walk func([]storageDisk, []storageDisk) (storageDisk, []storageDisk, bool)
	walk = func(list []storageDisk, ancestors []storageDisk) (storageDisk, []storageDisk, bool) {
		for _, device := range list {
			chain := append(append([]storageDisk{}, ancestors...), device)
			if device.Path == path {
				return device, chain, true
			}
			if found, foundChain, ok := walk(device.Children, chain); ok {
				return found, foundChain, true
			}
		}
		return storageDisk{}, nil, false
	}
	return walk(devices, nil)
}

func storageDeviceIdentityMatches(chain []storageDisk, serial, wwn string) bool {
	serial = strings.TrimSpace(serial)
	wwn = strings.TrimSpace(wwn)
	if serial == "" && wwn == "" {
		return true
	}
	for _, device := range chain {
		if serial != "" && device.Serial != "" && strings.EqualFold(device.Serial, serial) {
			return true
		}
		if wwn != "" && device.WWN != "" && strings.EqualFold(device.WWN, wwn) {
			return true
		}
	}
	return false
}

func (s *Server) storageVolumeAssignedCounts(ctx context.Context, volumeID string) (mailboxes, domains, defaults int64, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM public.mailboxes WHERE storage_volume_id = $1),
		       (SELECT count(*) FROM public.mail_domains WHERE storage_volume_id = $1),
		       (SELECT count(*) FROM control.storage_policy WHERE default_volume_id = $1)
	`, volumeID).Scan(&mailboxes, &domains, &defaults)
	return mailboxes, domains, defaults, err
}

func storagePartitionDevice(diskPath string, number int) string {
	if strings.Contains(diskPath, "nvme") || strings.Contains(diskPath, "mmcblk") || strings.Contains(diskPath, "loop") {
		return fmt.Sprintf("%sp%d", diskPath, number)
	}
	return fmt.Sprintf("%s%d", diskPath, number)
}

func storageFstabHasMountPath(content, mountPath string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] != "" && fields[0][0] != '#' && fields[1] == mountPath {
			return true
		}
	}
	return false
}

func (s *Server) buildStorageVolumeAddPlan(ctx context.Context, req storageVolumePlanRequest) (storageVolumePlan, error) {
	name, err := validateStorageVolumeName(req.Name)
	if err != nil {
		return storageVolumePlan{}, err
	}
	mountPath, err := validateStorageMountPath(s.cfg.StorageRoot, req.MountPath)
	if err != nil {
		return storageVolumePlan{}, err
	}
	if err := s.storageVolumeNameTaken(ctx, name, mountPath); err != nil {
		return storageVolumePlan{}, err
	}
	fsType := strings.ToLower(strings.TrimSpace(req.FSType))
	if fsType == "" {
		fsType = "ext4"
	}
	if fsType != "ext4" && fsType != "xfs" {
		return storageVolumePlan{}, fmt.Errorf("filesystem must be ext4 or xfs")
	}
	caps := s.storageHostCapabilities(ctx)
	if !caps.CanPartition || !caps.Tools["mkfs."+fsType] || !caps.Tools["systemctl"] || !caps.Tools["systemd-run"] {
		return storageVolumePlan{}, fmt.Errorf("the host is missing sgdisk/parted, mkfs.%s, systemctl or systemd-run", fsType)
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return storageVolumePlan{}, fmt.Errorf("%s", message)
	}
	disk, ok := findStorageDisk(devices, req.DevicePath, req.DeviceSerial, req.DeviceWWN)
	if !ok || disk.Type != "disk" {
		return storageVolumePlan{}, fmt.Errorf("the selected disk is no longer present")
	}
	if disk.SystemProtected {
		return storageVolumePlan{}, fmt.Errorf("the selected disk is a system or virtual device and cannot be used")
	}
	requireDestroy := disk.HasSignatures || disk.Mountpoint != "" || len(disk.Children) > 0
	if disk.Mountpoint != "" {
		return storageVolumePlan{}, fmt.Errorf("the selected disk is mounted; adopt the mounted filesystem or unmount it first")
	}
	if disk.SizeBytes < 1<<30 {
		return storageVolumePlan{}, fmt.Errorf("the selected disk is smaller than 1 GiB")
	}
	if info, statErr := os.Stat(mountPath); statErr == nil {
		if !info.IsDir() {
			return storageVolumePlan{}, fmt.Errorf("the mount path is not a directory")
		}
		entries, readErr := os.ReadDir(mountPath)
		if readErr != nil {
			return storageVolumePlan{}, readErr
		}
		if len(entries) > 0 {
			return storageVolumePlan{}, fmt.Errorf("the mount path is not empty; choose an empty directory")
		}
	}
	fstabRaw, _ := os.ReadFile(s.cfg.StorageFstab)
	if storageFstabHasMountPath(string(fstabRaw), mountPath) {
		return storageVolumePlan{}, fmt.Errorf("the mount path already appears in %s; remove the unmanaged entry first", s.cfg.StorageFstab)
	}
	partition := storagePartitionDevice(disk.Path, 1)
	commands := []string{
		"sgdisk --zap-all " + disk.Path,
		fmt.Sprintf("sgdisk -n 1:0:0 -t 1:8300 -c 1:a8m-%s %s", name, disk.Path),
		"partprobe " + disk.Path,
		"udevadm settle",
	}
	if fsType == "xfs" {
		commands = append(commands, fmt.Sprintf("mkfs.xfs -f -L a8m-%s %s", name, partition))
	} else {
		commands = append(commands, fmt.Sprintf("mkfs.ext4 -F -L a8m-%s -m 1 %s", name, partition))
	}
	commands = append(commands, "systemctl daemon-reload", "systemctl start <escaped mount unit>")
	warnings := []string{}
	if requireDestroy {
		warnings = append(warnings, "The selected disk already contains partitions or filesystem signatures. Initializing it permanently erases that data.")
	}
	confirm := firstNonEmpty(disk.Serial, disk.WWN, disk.Path)
	return storageVolumePlan{
		Kind: "volume_add", Mode: "initialize", Name: name, MountPath: mountPath,
		DevicePath: disk.Path, PartitionPath: partition, DeviceSerial: disk.Serial,
		DeviceWWN: disk.WWN, FSType: fsType, DiskSizeBytes: disk.SizeBytes,
		RequireDestroy: requireDestroy, ConfirmValue: confirm,
		Commands: commands, Warnings: warnings,
	}, nil
}

func (s *Server) buildStorageVolumeAdoptPlan(ctx context.Context, req storageVolumePlanRequest) (storageVolumePlan, error) {
	name, err := validateStorageVolumeName(req.Name)
	if err != nil {
		return storageVolumePlan{}, err
	}
	mountPath := filepath.Clean(strings.TrimSpace(req.MountPath))
	if mountPath == "" || !filepath.IsAbs(mountPath) {
		return storageVolumePlan{}, fmt.Errorf("mount path must be absolute")
	}
	if err := s.storageVolumeNameTaken(ctx, name, mountPath); err != nil {
		return storageVolumePlan{}, err
	}
	result, err := storageHostCommand(ctx, 10*time.Second, "findmnt", "-n", "-o", "SOURCE,FSTYPE,OPTIONS", "--mountpoint", mountPath)
	if err != nil {
		return storageVolumePlan{}, fmt.Errorf("the path is not a mounted filesystem")
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) < 3 {
		return storageVolumePlan{}, fmt.Errorf("could not determine the mounted filesystem")
	}
	source, fsType := fields[0], strings.ToLower(fields[1])
	if fsType != "ext4" && fsType != "xfs" && fsType != "nfs" && fsType != "nfs4" {
		return storageVolumePlan{}, fmt.Errorf("only ext4, xfs and NFS mounts can be adopted")
	}
	stat, err := filesystemStatLookup(mountPath)
	if err != nil {
		return storageVolumePlan{}, fmt.Errorf("the mount is not readable: %w", err)
	}
	warnings := []string{"Adoption registers the existing mount and does not change its filesystem or fstab entry."}
	if fsType == "nfs" || fsType == "nfs4" {
		warnings = append(warnings, "This is an NFS mount. Alt8Mail does not manage the export, mount options, locking or server-side capacity; growth and wipe actions are not available.")
	}
	return storageVolumePlan{
		Kind: "volume_adopt", Mode: "adopt", Name: name, MountPath: mountPath,
		DevicePath: source, FSType: fsType, DiskSizeBytes: stat.TotalBytes,
		ConfirmValue: name, Commands: []string{}, Warnings: warnings,
	}, nil
}

func (s *Server) buildStorageVolumeExtendPlan(ctx context.Context, req storageVolumePlanRequest) (storageVolumePlan, error) {
	volume, err := s.loadStorageVolume(ctx, strings.TrimSpace(req.VolumeID))
	if err != nil {
		return storageVolumePlan{}, err
	}
	if volume.State != "online" {
		return storageVolumePlan{}, fmt.Errorf("the volume is not online")
	}
	if volume.FSType == "nfs" || volume.FSType == "nfs4" {
		return storageVolumePlan{}, fmt.Errorf("NFS capacity is managed by the export; grow the export on the server, then verify the mount")
	}
	if caps := s.storageHostCapabilities(ctx); !caps.Tools["systemd-run"] || !caps.Tools["systemctl"] {
		return storageVolumePlan{}, fmt.Errorf("systemd-run and systemctl are required to grow a mounted filesystem")
	}
	result, err := storageHostCommand(ctx, 10*time.Second, "findmnt", "-n", "-o", "SOURCE,FSTYPE", "--mountpoint", volume.MountPath)
	if err != nil {
		return storageVolumePlan{}, fmt.Errorf("could not find the mounted filesystem: %w", err)
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) < 2 {
		return storageVolumePlan{}, fmt.Errorf("could not determine the backing device")
	}
	source, fsType := fields[0], strings.ToLower(fields[1])
	if fsType != "ext4" && fsType != "xfs" {
		return storageVolumePlan{}, fmt.Errorf("online growth supports ext4 and xfs")
	}
	plan := storageVolumePlan{
		Kind: "volume_extend", Mode: "grow", VolumeID: volume.ID,
		VolumeName: volume.Name, MountPath: volume.MountPath, DevicePath: source,
		FSType: fsType, OldSizeBytes: volume.SizeBytes, ConfirmValue: volume.Name,
		Commands: []string{}, Warnings: []string{},
	}
	if strings.TrimSpace(req.DevicePath) != "" || strings.TrimSpace(req.DeviceSerial) != "" || strings.TrimSpace(req.DeviceWWN) != "" {
		devices, supported, message := s.listStorageDisks(ctx)
		if !supported {
			return storageVolumePlan{}, fmt.Errorf("%s", message)
		}
		disk, ok := findStorageDisk(devices, req.DevicePath, req.DeviceSerial, req.DeviceWWN)
		if !ok || disk.Type != "disk" {
			return storageVolumePlan{}, fmt.Errorf("the new disk is no longer present")
		}
		if disk.SystemProtected || disk.Mountpoint != "" || disk.HasSignatures || len(disk.Children) > 0 {
			return storageVolumePlan{}, fmt.Errorf("the new disk must be blank and unmounted")
		}
		lvs, lvsErr := storageHostCommand(ctx, 10*time.Second, "lvs", "--noheadings", "-o", "vg_name,lv_name", source)
		if lvsErr != nil {
			return storageVolumePlan{}, fmt.Errorf("the mounted filesystem is not on thick LVM: %w", lvsErr)
		}
		parts := strings.Fields(lvs.Stdout)
		if len(parts) < 2 {
			return storageVolumePlan{}, fmt.Errorf("could not determine the LVM volume group and logical volume")
		}
		plan.Mode = "lvm_extend"
		plan.LVM = &storageLVMPlan{
			VG: parts[0], LV: parts[1], LVPath: source, MountPath: volume.MountPath,
			FSType: fsType, NewDevice: disk.Path, NewSerial: disk.Serial,
			NewWWN: disk.WWN, NewDiskSize: disk.SizeBytes,
		}
		plan.DevicePath = disk.Path
		plan.DeviceSerial = disk.Serial
		plan.DeviceWWN = disk.WWN
		plan.DiskSizeBytes = disk.SizeBytes
		plan.ConfirmValue = firstNonEmpty(disk.Serial, disk.WWN, disk.Path)
		plan.Commands = []string{
			"pvcreate " + disk.Path,
			fmt.Sprintf("vgextend %s %s", parts[0], disk.Path),
			fmt.Sprintf("lvextend -l +100%%FREE %s", source),
		}
		if fsType == "xfs" {
			plan.Commands = append(plan.Commands, "xfs_growfs "+volume.MountPath)
		} else {
			plan.Commands = append(plan.Commands, "resize2fs "+source)
		}
		plan.Warnings = append(plan.Warnings, "The new disk becomes a physical volume in "+parts[0]+" and its full capacity is added to the logical volume.")
		return plan, nil
	}
	lsblkResult, lsblkErr := storageHostCommand(ctx, 10*time.Second, "lsblk", "-no", "TYPE,PKNAME", source)
	if lsblkErr == nil {
		parts := strings.Fields(lsblkResult.Stdout)
		if len(parts) > 0 && parts[0] == "lvm" {
			plan.Mode = "lvm_grow"
			lvs, lvsErr := storageHostCommand(ctx, 10*time.Second, "lvs", "--noheadings", "-o", "lv_layout,vg_name,lv_name", source)
			if lvsErr == nil {
				lvParts := strings.Fields(lvs.Stdout)
				if len(lvParts) >= 3 {
					layout := strings.ToLower(lvParts[0])
					if strings.Contains(layout, "thin") {
						return storageVolumePlan{}, fmt.Errorf("thin LVM growth requires thin-pool capacity management and is not automated")
					}
					if strings.Contains(layout, "raid") {
						return storageVolumePlan{}, fmt.Errorf("LVM RAID growth requires a separate RAID design and lab matrix")
					}
					plan.LVM = &storageLVMPlan{VG: lvParts[1], LV: lvParts[2], LVPath: source, MountPath: volume.MountPath, FSType: fsType}
				}
			}
			plan.Commands = []string{fmt.Sprintf("lvextend -l +100%%FREE %s", source)}
			if fsType == "xfs" {
				plan.ResizeMount = volume.MountPath
				plan.Commands = append(plan.Commands, "xfs_growfs "+volume.MountPath)
			} else {
				plan.ResizeDevice = source
				plan.Commands = append(plan.Commands, "resize2fs "+source)
			}
			return plan, nil
		}
	}
	parent := ""
	if lsblkErr == nil {
		fields := strings.Fields(lsblkResult.Stdout)
		if len(fields) >= 2 {
			parent = fields[1]
		}
	}
	if parent != "" && !strings.HasPrefix(parent, "/dev/") {
		parent = "/dev/" + parent
	}
	if parent != "" {
		partNum := strings.TrimPrefix(filepath.Base(source), filepath.Base(parent))
		partNum = strings.TrimPrefix(partNum, "p")
		if partNum == "" {
			return storageVolumePlan{}, fmt.Errorf("could not determine the partition number")
		}
		plan.GrowDisk = parent
		plan.GrowPartition = partNum
		plan.Commands = append(plan.Commands, fmt.Sprintf("growpart %s %s", parent, partNum))
	}
	if fsType == "xfs" {
		plan.ResizeMount = volume.MountPath
		plan.Commands = append(plan.Commands, "xfs_growfs "+volume.MountPath)
	} else {
		plan.ResizeDevice = source
		plan.Commands = append(plan.Commands, "resize2fs "+source)
	}
	if len(plan.Commands) == 1 && parent == "" {
		plan.Warnings = append(plan.Warnings, "No partition parent was detected; the filesystem growth command runs directly on the device.")
	}
	return plan, nil
}

func (s *Server) buildStorageVolumeRetirePlan(ctx context.Context, req storageVolumePlanRequest) (storageVolumePlan, error) {
	volume, err := s.loadStorageVolume(ctx, strings.TrimSpace(req.VolumeID))
	if err != nil {
		return storageVolumePlan{}, err
	}
	if volume.Name == "legacy" || sameStoragePath(volume.MountPath, s.cfg.VmailRoot) {
		return storageVolumePlan{}, fmt.Errorf("the legacy mail root cannot be retired")
	}
	if caps := s.storageHostCapabilities(ctx); !caps.Tools["systemd-run"] || !caps.Tools["systemctl"] {
		return storageVolumePlan{}, fmt.Errorf("systemd-run and systemctl are required to retire a volume")
	}
	mailboxes, domains, defaults, err := s.storageVolumeAssignedCounts(ctx, volume.ID)
	if err != nil {
		return storageVolumePlan{}, err
	}
	if mailboxes > 0 || domains > 0 || defaults > 0 {
		return storageVolumePlan{}, fmt.Errorf("the volume still has %d mailbox(es), %d domain default(s) and %d global default assignment(s); evacuate it first", mailboxes, domains, defaults)
	}
	warnings := []string{"The filesystem is not formatted or deleted. Its data stays on the device after the volume is unmounted."}
	if !volume.FstabManaged {
		warnings = append(warnings, "This adopted volume has no Alt8Mail-managed fstab block. Its existing mount entry is left untouched and may mount the volume again after a reboot.")
	}
	plan := storageVolumePlan{
		Kind: "volume_retire", Mode: "retire", VolumeID: volume.ID, VolumeName: volume.Name,
		MountPath: volume.MountPath, DevicePath: volume.DevicePath, DeviceSerial: volume.DeviceSerial,
		DeviceWWN: volume.DeviceWWN, FSType: volume.FSType, ConfirmValue: volume.Name,
		Commands: []string{"systemctl stop <escaped mount unit>", "remove the Alt8Mail fstab block", "systemctl daemon-reload"},
		Warnings: warnings,
	}
	return plan, nil
}

func (s *Server) buildStorageVolumeWipePlan(ctx context.Context, req storageVolumePlanRequest) (storageVolumePlan, error) {
	volume, err := s.loadStorageVolume(ctx, strings.TrimSpace(req.VolumeID))
	if err != nil {
		return storageVolumePlan{}, err
	}
	if volume.Name == "legacy" || sameStoragePath(volume.MountPath, s.cfg.VmailRoot) {
		return storageVolumePlan{}, fmt.Errorf("the legacy mail root cannot be wiped")
	}
	if volume.State != "retired" {
		return storageVolumePlan{}, fmt.Errorf("retire and unmount the volume before wiping it")
	}
	caps := s.storageHostCapabilities(ctx)
	if !caps.Tools["wipefs"] || !caps.Tools["findmnt"] {
		return storageVolumePlan{}, fmt.Errorf("wipefs and findmnt are required to wipe a volume")
	}
	mailboxes, domains, defaults, err := s.storageVolumeAssignedCounts(ctx, volume.ID)
	if err != nil {
		return storageVolumePlan{}, err
	}
	if mailboxes > 0 || domains > 0 || defaults > 0 {
		return storageVolumePlan{}, fmt.Errorf("the volume still has %d mailbox(es), %d domain default(s) and %d global default assignment(s); evacuate it before wiping", mailboxes, domains, defaults)
	}
	devicePath := strings.TrimSpace(volume.DevicePath)
	if devicePath == "" || !strings.HasPrefix(devicePath, "/dev/") {
		return storageVolumePlan{}, fmt.Errorf("the volume has no valid recorded device path")
	}
	if s.storageMountActive(ctx, volume.MountPath) {
		return storageVolumePlan{}, fmt.Errorf("the volume is still mounted; retire it before wiping")
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return storageVolumePlan{}, fmt.Errorf("%s", message)
	}
	device, chain, ok := findStorageDevicePath(devices, devicePath)
	if !ok {
		return storageVolumePlan{}, fmt.Errorf("the volume device %s is no longer present", devicePath)
	}
	for _, entry := range chain {
		if entry.SystemProtected {
			return storageVolumePlan{}, fmt.Errorf("%s is a system or virtual device and cannot be wiped", devicePath)
		}
	}
	if !storageDeviceIdentityMatches(chain, volume.DeviceSerial, volume.DeviceWWN) {
		return storageVolumePlan{}, fmt.Errorf("the device identity at %s no longer matches the registered volume", devicePath)
	}
	if device.Path != devicePath {
		return storageVolumePlan{}, fmt.Errorf("the volume device path changed; re-discover the disk before wiping")
	}
	return storageVolumePlan{
		Kind: "volume_wipe", Mode: "wipe", VolumeID: volume.ID, VolumeName: volume.Name,
		MountPath: volume.MountPath, DevicePath: devicePath, DeviceSerial: volume.DeviceSerial,
		DeviceWWN: volume.DeviceWWN, FSType: volume.FSType, DiskSizeBytes: volume.SizeBytes,
		RequireDestroy: true, ConfirmValue: volume.Name,
		Commands: []string{"wipefs -a " + devicePath},
		Warnings: []string{
			"Wiping removes the filesystem and other signatures at " + devicePath + ". File contents are not securely erased and may remain recoverable.",
			"This is a separate destructive action from retirement. The volume must already be retired, unmounted and free of assignments.",
		},
	}, nil
}

func (s *Server) createStorageVolumeOperation(ctx context.Context, sess *sessionRecord, plan storageVolumePlan) (string, string, time.Time, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", "", time.Time{}, err
	}
	hash := canonicalPlanHash(raw)
	expires := time.Now().UTC().Add(10 * time.Minute)
	opKind := plan.Kind
	if opKind == "" {
		opKind = "volume_add"
	}
	var id string
	err = s.db.QueryRow(ctx, `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, plan_expires_at, step, total_bytes, actor_id, actor_email)
		VALUES ($1, 'planned', $2::jsonb, $3, $4, 'Ready to execute', $5,
		        NULLIF($6, '')::uuid, $7)
		RETURNING id::text
	`, opKind, string(raw), hash, expires, plan.DiskSizeBytes, sess.User.ID, sess.User.Email).Scan(&id)
	if err != nil {
		return "", "", time.Time{}, err
	}
	token := s.signCSRF("storage-volume:" + id + ":" + hash)
	return id, token, expires, nil
}

func (s *Server) handleStorageVolumePlan(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var req storageVolumePlanRequest
	if err := decodeJSON(w, r, &req, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	enabled, _, err := s.storageProvisioningEnabled(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusConflict, "PROVISIONING", "Host disk provisioning is disabled. A superadmin must enable it in Disk Management.")
		return
	}
	caps := s.storageHostCapabilities(r.Context())
	if !caps.Supported {
		writeError(w, http.StatusConflict, "HOST", caps.Reason)
		return
	}
	var plan storageVolumePlan
	switch strings.ToLower(strings.TrimSpace(req.Kind)) {
	case "add", "":
		plan, err = s.buildStorageVolumeAddPlan(r.Context(), req)
	case "adopt":
		plan, err = s.buildStorageVolumeAdoptPlan(r.Context(), req)
	case "extend":
		plan, err = s.buildStorageVolumeExtendPlan(r.Context(), req)
	case "retire":
		plan, err = s.buildStorageVolumeRetirePlan(r.Context(), req)
	case "wipe":
		plan, err = s.buildStorageVolumeWipePlan(r.Context(), req)
	default:
		writeError(w, http.StatusBadRequest, "KIND", "Use add, adopt, extend, retire or wipe")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "PLAN", err.Error())
		return
	}
	if plan.RequireDestroy && !sess.User.PermSet.Has("storage.destroy") {
		writeError(w, http.StatusForbidden, "PERMISSION", "This plan would erase existing disk data and requires the storage.destroy permission")
		return
	}
	operationID, token, expires, err := s.createStorageVolumeOperation(r.Context(), sess, plan)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "storage.volume_planned", operationID, "Planned a host storage operation", map[string]any{
		"kind": plan.Kind, "mode": plan.Mode, "name": plan.Name,
		"mountPath": plan.MountPath, "device": plan.DevicePath, "requireDestroy": plan.RequireDestroy,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "operationId": operationID, "token": token,
		"expiresAt": expires.Format(time.RFC3339), "plan": plan,
	})
}

func (s *Server) handleStorageVolumeExecute(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		OperationID  string `json:"operationId"`
		Token        string `json:"token"`
		Confirm      bool   `json:"confirm"`
		ConfirmValue string `json:"confirmValue"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Confirm the host storage plan before executing it")
		return
	}
	op, err := s.loadStorageOperation(r.Context(), strings.TrimSpace(body.OperationID))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !strings.HasPrefix(op.Kind, "volume_") {
		writeError(w, http.StatusConflict, "OPERATION", "That is not a host volume operation")
		return
	}
	if op.Status != "planned" {
		writeError(w, http.StatusConflict, "OPERATION", "That operation is no longer waiting to execute")
		return
	}
	if op.PlanExpiresAt != nil && time.Now().After(*op.PlanExpiresAt) {
		writeError(w, http.StatusConflict, "EXPIRED", "This plan has expired. Create a new plan.")
		return
	}
	if !constantTimeEqual(op.PlanHash, canonicalPlanHash(op.Plan)) ||
		!constantTimeEqual(body.Token, s.signCSRF("storage-volume:"+op.ID+":"+op.PlanHash)) {
		writeError(w, http.StatusForbidden, "CONFIRM", "The plan token is invalid. Create a new plan.")
		return
	}
	var plan storageVolumePlan
	if err := json.Unmarshal(op.Plan, &plan); err != nil {
		writeError(w, http.StatusConflict, "OPERATION", "The stored plan is not readable")
		return
	}
	if strings.TrimSpace(body.ConfirmValue) != strings.TrimSpace(plan.ConfirmValue) {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type the exact confirmation value from the plan")
		return
	}
	if plan.RequireDestroy && !sess.User.PermSet.Has("storage.destroy") {
		writeError(w, http.StatusForbidden, "PERMISSION", "This operation requires the storage.destroy permission")
		return
	}
	enabled, _, err := s.storageProvisioningEnabled(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !enabled {
		writeError(w, http.StatusConflict, "PROVISIONING", "Host disk provisioning is disabled")
		return
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE control.storage_operations
		SET status = 'queued', step = 'Queued', error = '', updated_at = now()
		WHERE id = $1 AND status = 'planned'
	`, op.ID); err != nil {
		writeFromError(w, err)
		return
	}
	if err := s.enqueueStorageOperation(op.ID); err != nil {
		_, _ = s.db.Exec(context.Background(), `
			UPDATE control.storage_operations SET status = 'failed', error = $2,
			       step = 'Could not start the storage runner', finished_at = now(), updated_at = now()
			WHERE id = $1
		`, op.ID, truncateText(err.Error(), 500))
		writeError(w, http.StatusBadGateway, "RUNNER", "Could not start the storage runner: "+err.Error())
		return
	}
	s.actorAudit(r, sess, "storage.volume_started", op.ID, "Started a host storage operation", map[string]any{"kind": op.Kind})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "operationId": op.ID})
}

func (s *Server) handleStorageVolumeVerify(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	volume, err := s.loadStorageVolume(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if !s.storageMountActive(r.Context(), volume.MountPath) {
		writeError(w, http.StatusConflict, "MOUNT", "The volume is not mounted at "+volume.MountPath)
		return
	}
	stat, err := filesystemStatLookup(volume.MountPath)
	if err != nil {
		writeError(w, http.StatusConflict, "MOUNT", "The volume is not readable: "+err.Error())
		return
	}
	unit := volume.MountUnit
	if unit == "" {
		unit = storageMountUnitName(volume.MountPath)
	}
	verified := true
	if volume.FSUUID != "" {
		result, err := storageHostCommand(r.Context(), 10*time.Second, "findmnt", "-n", "-o", "UUID", "--mountpoint", volume.MountPath)
		if err != nil || strings.TrimSpace(result.Stdout) != volume.FSUUID {
			verified = false
		}
	}
	testPath := filepath.Join(volume.MountPath, ".a8m-write-test")
	if err := os.WriteFile(testPath, []byte("ok"), 0o600); err != nil {
		verified = false
	} else {
		_ = os.Remove(testPath)
	}
	state := volume.State
	if verified && (state == "offline" || state == "degraded") {
		state = "online"
	}
	if !verified && state == "online" {
		state = "degraded"
	}
	if _, err := s.db.Exec(r.Context(), `
		UPDATE control.storage_volumes
		SET size_bytes = $2, last_seen_at = now(), verified_at = now(),
		    reboot_verified = $5, mount_unit = $3, state = $4, updated_at = now()
		WHERE id = $1
	`, volume.ID, stat.TotalBytes, unit, state, verified); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "storage.volume_verified", volume.Name, "Verified a storage volume mount", map[string]any{"verified": verified, "mountPath": volume.MountPath})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": verified, "volumeId": volume.ID, "mountPath": volume.MountPath,
		"verified": verified, "totalBytes": stat.TotalBytes, "usedBytes": stat.UsedBytes,
		"freeBytes": stat.FreeBytes, "state": state,
	})
}
