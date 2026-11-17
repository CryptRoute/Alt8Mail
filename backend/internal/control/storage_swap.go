package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	storageSwapMinBytes = 64 << 20
	storageSwapMaxBytes = 1 << 40
)

var (
	storageSwapProcPath       = "/proc/swaps"
	storageSwapMeminfoPath    = "/proc/meminfo"
	storageSwapSwappinessPath = "/proc/sys/vm/swappiness"
)

type storageSwapEntry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	SizeBytes  uint64 `json:"sizeBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
	Priority   int    `json:"priority"`
	Managed    bool   `json:"managed"`
	Persistent bool   `json:"persistent"`
}

type storageSwapCandidate struct {
	VolumeID     string `json:"volumeId"`
	Name         string `json:"name"`
	MountPath    string `json:"mountPath"`
	FSType       string `json:"fsType"`
	Topology     string `json:"topology"`
	TotalBytes   uint64 `json:"totalBytes"`
	FreeBytes    uint64 `json:"freeBytes"`
	MaxSwapBytes uint64 `json:"maxSwapBytes"`
	Online       bool   `json:"online"`
	Eligible     bool   `json:"eligible"`
	Reason       string `json:"reason,omitempty"`
}

type storageSwapDevice struct {
	Path          string `json:"path"`
	SizeBytes     uint64 `json:"sizeBytes"`
	Model         string `json:"model"`
	Serial        string `json:"serial"`
	WWN           string `json:"wwn"`
	HasSignatures bool   `json:"hasSignatures"`
	Active        bool   `json:"active"`
	Eligible      bool   `json:"eligible"`
	Reason        string `json:"reason,omitempty"`
}

type storageSwapInfo struct {
	Supported            bool                   `json:"supported"`
	Reason               string                 `json:"reason,omitempty"`
	TotalBytes           uint64                 `json:"totalBytes"`
	UsedBytes            uint64                 `json:"usedBytes"`
	FreeBytes            uint64                 `json:"freeBytes"`
	MemoryTotalBytes     uint64                 `json:"memoryTotalBytes"`
	MemoryFreeBytes      uint64                 `json:"memoryFreeBytes"`
	RecommendedSizeBytes uint64                 `json:"recommendedSizeBytes"`
	Swappiness           int                    `json:"swappiness"`
	Entries              []storageSwapEntry     `json:"entries"`
	Candidates           []storageSwapCandidate `json:"candidates"`
	Devices              []storageSwapDevice    `json:"devices"`
	Tools                map[string]bool        `json:"tools"`
	GeneratedAt          string                 `json:"generatedAt"`
}

type storageSwapPlan struct {
	Kind           string   `json:"kind"`
	Mode           string   `json:"mode"`
	Name           string   `json:"name"`
	VolumeID       string   `json:"volumeId,omitempty"`
	VolumeName     string   `json:"volumeName,omitempty"`
	MountPath      string   `json:"mountPath,omitempty"`
	FilePath       string   `json:"filePath,omitempty"`
	FSType         string   `json:"fsType,omitempty"`
	DevicePath     string   `json:"devicePath,omitempty"`
	DeviceSerial   string   `json:"deviceSerial,omitempty"`
	DeviceWWN      string   `json:"deviceWwn,omitempty"`
	SizeBytes      uint64   `json:"sizeBytes"`
	Label          string   `json:"label"`
	Target         string   `json:"target"`
	RequireDestroy bool     `json:"requireDestroy"`
	ConfirmValue   string   `json:"confirmValue"`
	Commands       []string `json:"commands"`
	Warnings       []string `json:"warnings"`
}

type storageSwapPlanRequest struct {
	Mode         string `json:"mode"`
	VolumeID     string `json:"volumeId"`
	SizeBytes    uint64 `json:"sizeBytes"`
	DevicePath   string `json:"devicePath"`
	DeviceSerial string `json:"deviceSerial"`
	DeviceWWN    string `json:"deviceWwn"`
}

func (s *Server) registerStorageSwapRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/storage/swap", s.require("storage.read", s.handleStorageSwap))
	mux.HandleFunc("POST /control/api/storage/swap/plan", s.require("storage.manage", s.handleStorageSwapPlan))
	mux.HandleFunc("POST /control/api/storage/swap/execute", s.require("storage.manage", s.handleStorageSwapExecute))
}

func (s *Server) handleStorageSwap(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	info, err := s.loadStorageSwapInfo(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) loadStorageSwapInfo(ctx context.Context) (storageSwapInfo, error) {
	info := storageSwapInfo{
		Supported:   storageHostGOOS == "linux",
		Tools:       map[string]bool{},
		Entries:     []storageSwapEntry{},
		Candidates:  []storageSwapCandidate{},
		Devices:     []storageSwapDevice{},
		GeneratedAt: nowRFC3339(),
	}
	if !info.Supported {
		info.Reason = "Swap management is available on the Linux control host only."
		return info, nil
	}
	if s.db != nil {
		_ = s.ensureStorageVolumes(ctx)
	}
	for _, tool := range []string{"mkswap", "swapon", "swapoff", "fallocate", "dd", "chmod", "blkid", "wipefs", "systemctl", "findmnt"} {
		_, err := storageLookPath(tool)
		info.Tools[tool] = err == nil
	}
	if !info.Tools["mkswap"] || !info.Tools["swapon"] || !info.Tools["swapoff"] {
		info.Reason = "mkswap, swapon and swapoff are required to create swap."
	}

	entries, err := readStorageSwapEntries()
	if err == nil {
		info.Entries = entries
	}
	memTotal, memFree, swapTotal, swapFree := readStorageSwapMemory()
	info.MemoryTotalBytes = memTotal
	info.MemoryFreeBytes = memFree
	if swapTotal == 0 {
		for _, entry := range info.Entries {
			swapTotal += entry.SizeBytes
			swapFree += entry.SizeBytes - minUint64(entry.SizeBytes, entry.UsedBytes)
		}
	}
	info.TotalBytes = swapTotal
	info.FreeBytes = minUint64(swapTotal, swapFree)
	if info.TotalBytes >= info.FreeBytes {
		info.UsedBytes = info.TotalBytes - info.FreeBytes
	}
	fstabRaw, _ := os.ReadFile(s.cfg.StorageFstab)
	managed := storageSwapManagedDevices(string(fstabRaw))
	for index := range info.Entries {
		entry := &info.Entries[index]
		entry.Managed = managed[entry.Path]
		entry.Persistent = entry.Managed || storageSwapFstabHasSource(string(fstabRaw), entry.Path)
	}
	info.RecommendedSizeBytes = recommendedSwapSize(memTotal)
	info.Swappiness = readStorageSwappiness()

	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		return info, err
	}
	volumes, err := s.storageVolumeRows(ctx)
	if err != nil {
		return info, err
	}
	for _, volume := range volumes {
		if volume.State == "retired" || volume.State == "wiped" {
			continue
		}
		candidate := storageSwapCandidate{
			VolumeID: volume.ID, Name: volume.Name, MountPath: volume.MountPath,
			FSType: volume.FSType, Topology: storageVolumeTopology(volume),
		}
		stat, statErr := filesystemStatLookup(volume.MountPath)
		if statErr != nil {
			candidate.Reason = "The volume is not readable."
			info.Candidates = append(info.Candidates, candidate)
			continue
		}
		candidate.TotalBytes = stat.TotalBytes
		candidate.FreeBytes = stat.FreeBytes
		reserve := storageSwapFileReserve(policy, stat.TotalBytes)
		if stat.FreeBytes > reserve {
			candidate.MaxSwapBytes = stat.FreeBytes - reserve
		}
		fsType := strings.ToLower(strings.TrimSpace(volume.FSType))
		network := candidate.Topology == "nfs" || fsType == "nfs" || fsType == "nfs4"
		if err := s.storageSwapFileVolumeReady(ctx, volume); err != nil {
			candidate.Reason = err.Error()
		} else {
			candidate.Online = true
			switch {
			case network:
				candidate.Reason = "NFS cannot host a swap file."
			case fsType != "ext4" && fsType != "xfs":
				candidate.Reason = "Swap files require ext4 or XFS."
			case candidate.MaxSwapBytes < storageSwapMinBytes:
				candidate.Reason = "Not enough free space after the capacity reserve."
			default:
				candidate.Eligible = true
			}
		}
		info.Candidates = append(info.Candidates, candidate)
	}

	devices, supported, message := s.listStorageDisks(ctx)
	if supported {
		activeSwap := map[string]bool{}
		for _, entry := range info.Entries {
			activeSwap[entry.Path] = true
		}
		for _, disk := range devices {
			if disk.Type != "disk" || disk.SystemProtected {
				continue
			}
			device := storageSwapDevice{
				Path: disk.Path, SizeBytes: disk.SizeBytes, Model: disk.Model,
				Serial: disk.Serial, WWN: disk.WWN, HasSignatures: disk.HasSignatures,
				Active: activeSwap[disk.Path],
			}
			switch {
			case device.Active:
				device.Reason = "This device is already active swap."
			case disk.Mountpoint != "":
				device.Reason = "The disk is mounted."
			case len(disk.Children) > 0:
				device.Reason = "The disk contains partitions; choose a blank disk."
			case disk.SizeBytes < storageSwapMinBytes:
				device.Reason = "The disk is smaller than the minimum swap size."
			default:
				device.Eligible = true
				if disk.HasSignatures {
					device.Reason = "Existing signatures will be erased."
				}
			}
			info.Devices = append(info.Devices, device)
		}
	} else if strings.TrimSpace(message) != "" {
		info.Reason = message
	}
	return info, nil
}

func readStorageSwapEntries() ([]storageSwapEntry, error) {
	raw, err := os.ReadFile(storageSwapProcPath)
	if err != nil {
		return nil, err
	}
	entries := []storageSwapEntry{}
	for index, line := range strings.Split(string(raw), "\n") {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		sizeKiB, sizeErr := strconv.ParseUint(fields[2], 10, 64)
		usedKiB, usedErr := strconv.ParseUint(fields[3], 10, 64)
		priority, priorityErr := strconv.Atoi(fields[4])
		if sizeErr != nil || usedErr != nil || priorityErr != nil {
			continue
		}
		entries = append(entries, storageSwapEntry{
			Path: fields[0], Type: fields[1], SizeBytes: sizeKiB * 1024,
			UsedBytes: usedKiB * 1024, Priority: priority,
		})
	}
	return entries, nil
}

func readStorageSwapMemory() (total, free, swapTotal, swapFree uint64) {
	raw, err := os.ReadFile(storageSwapMeminfoPath)
	if err != nil {
		return 0, 0, 0, 0
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = value * 1024
	}
	total = values["MemTotal"]
	free = values["MemAvailable"]
	if free == 0 {
		free = values["MemFree"]
	}
	return total, free, values["SwapTotal"], values["SwapFree"]
}

func readStorageSwappiness() int {
	raw, err := os.ReadFile(storageSwapSwappinessPath)
	if err != nil {
		return 60
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 60
	}
	return value
}

func recommendedSwapSize(memoryTotal uint64) uint64 {
	if memoryTotal == 0 {
		return 2 << 30
	}
	size := memoryTotal / 4
	if size < 1<<30 {
		size = 1 << 30
	}
	if size > 8<<30 {
		size = 8 << 30
	}
	const step = 256 << 20
	return ((size + step - 1) / step) * step
}

func storageSwapFileReserve(policy storagePolicy, total uint64) uint64 {
	reserve := uint64(256 << 20)
	if minimum := uint64(policy.MinFreeBytes); minimum > reserve {
		reserve = minimum
	}
	if fraction := total / 20; fraction > reserve {
		reserve = fraction
	}
	return reserve
}

func minUint64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func storageSwapTargetName(prefix, target string) string {
	base := strings.ToLower(strings.TrimSpace(target))
	var builder strings.Builder
	lastHyphen := false
	for _, character := range base {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			lastHyphen = false
		} else if !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	base = strings.Trim(builder.String(), "-")
	name := prefix
	if base != "" {
		name += "-" + base
	}
	if len(name) > 48 {
		name = strings.TrimRight(name[:48], "-")
	}
	if name == "" {
		name = "swap"
	}
	return name
}

func storageSwapLabel(name string) string {
	label := "a8m-" + name
	if len(label) > 16 {
		label = strings.TrimRight(label[:16], "-")
	}
	return label
}

func storageSwapToolError(tools map[string]bool, names ...string) error {
	for _, name := range names {
		if !tools[name] {
			return fmt.Errorf("%s is required on the host", name)
		}
	}
	return nil
}

func (s *Server) buildStorageSwapPlan(ctx context.Context, req storageSwapPlanRequest) (storageSwapPlan, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "file"
	}
	caps := s.storageHostCapabilities(ctx)
	if !caps.Supported {
		return storageSwapPlan{}, fmt.Errorf("%s", caps.Reason)
	}
	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		return storageSwapPlan{}, err
	}
	switch mode {
	case "file":
		return s.buildStorageSwapFilePlan(ctx, req, policy)
	case "device":
		return s.buildStorageSwapDevicePlan(ctx, req)
	default:
		return storageSwapPlan{}, fmt.Errorf("swap mode must be file or device")
	}
}

// storageSwapFileVolumeReady accepts the legacy /var/vmail root even though it
// is usually a directory on the root filesystem rather than a separate mount.
// Managed volumes must still be real mounts so a missing mount cannot silently
// place the swap file on the parent filesystem.
func (s *Server) storageSwapFileVolumeReady(ctx context.Context, volume storageVolume) error {
	if volume.State != "" && volume.State != "online" {
		return fmt.Errorf("the source volume is not online")
	}
	mountPath := strings.TrimSpace(volume.MountPath)
	if mountPath == "" {
		return fmt.Errorf("the source volume has no mount path")
	}
	info, err := os.Stat(mountPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("the source volume directory is not readable")
	}
	if _, err := filesystemStatLookup(mountPath); err != nil {
		return fmt.Errorf("the source volume is not readable: %w", err)
	}
	legacy := volume.Name == "legacy" || sameStoragePath(mountPath, s.cfg.VmailRoot)
	if !legacy && !s.storageMountActive(ctx, mountPath) {
		return fmt.Errorf("the source volume is not mounted")
	}
	return nil
}

func (s *Server) buildStorageSwapFilePlan(ctx context.Context, req storageSwapPlanRequest, policy storagePolicy) (storageSwapPlan, error) {
	if err := storageSwapToolError(map[string]bool{
		"mkswap": hasStorageTool("mkswap"), "swapon": hasStorageTool("swapon"),
		"swapoff": hasStorageTool("swapoff"), "fallocate": hasStorageTool("fallocate"),
		"chmod": hasStorageTool("chmod"), "systemctl": hasStorageTool("systemctl"),
	}, "mkswap", "swapon", "swapoff", "fallocate", "chmod", "systemctl"); err != nil {
		return storageSwapPlan{}, err
	}
	volume, err := s.loadStorageVolume(ctx, strings.TrimSpace(req.VolumeID))
	if err != nil {
		return storageSwapPlan{}, fmt.Errorf("choose an online mail volume")
	}
	if err := s.storageSwapFileVolumeReady(ctx, volume); err != nil {
		return storageSwapPlan{}, err
	}
	fsType := strings.ToLower(strings.TrimSpace(volume.FSType))
	if fsType != "ext4" && fsType != "xfs" {
		return storageSwapPlan{}, fmt.Errorf("swap files require an ext4 or XFS volume")
	}
	stat, err := filesystemStatLookup(volume.MountPath)
	if err != nil {
		return storageSwapPlan{}, fmt.Errorf("the chosen volume is not readable: %w", err)
	}
	if req.SizeBytes < storageSwapMinBytes {
		return storageSwapPlan{}, fmt.Errorf("swap size must be at least 64 MiB")
	}
	if req.SizeBytes > storageSwapMaxBytes {
		return storageSwapPlan{}, fmt.Errorf("swap size must not exceed 1 TiB")
	}
	reserve := storageSwapFileReserve(policy, stat.TotalBytes)
	if stat.FreeBytes <= reserve || req.SizeBytes > stat.FreeBytes-reserve {
		available := uint64(0)
		if stat.FreeBytes > reserve {
			available = stat.FreeBytes - reserve
		}
		return storageSwapPlan{}, fmt.Errorf("not enough free space after the capacity reserve; maximum swap is %s", humanBytes(available))
	}
	filePath := filepath.Join(volume.MountPath, ".a8m-swapfile")
	if _, err := os.Lstat(filePath); err == nil {
		return storageSwapPlan{}, fmt.Errorf("a swap file already exists at %s", filePath)
	} else if !os.IsNotExist(err) {
		return storageSwapPlan{}, fmt.Errorf("could not inspect %s: %w", filePath, err)
	}
	if entryActive, err := readStorageSwapEntries(); err == nil {
		for _, entry := range entryActive {
			if sameStoragePath(entry.Path, filePath) {
				return storageSwapPlan{}, fmt.Errorf("that swap file is already active")
			}
		}
	}
	name := storageSwapTargetName("swapfile", volume.Name)
	label := storageSwapLabel(name)
	plan := storageSwapPlan{
		Kind: "swap_create", Mode: "file", Name: name,
		VolumeID: volume.ID, VolumeName: volume.Name, MountPath: volume.MountPath,
		FilePath: filePath, FSType: fsType, SizeBytes: req.SizeBytes,
		Label: label, Target: filePath,
		ConfirmValue: "CREATE SWAP",
		Commands: []string{
			fmt.Sprintf("fallocate -l %d %s", req.SizeBytes, filePath),
			"chmod 600 " + filePath,
			fmt.Sprintf("mkswap -L %s %s", label, filePath),
			fmt.Sprintf("fstab: %s none swap sw 0 0", filePath),
			"swapon " + filePath,
		},
		Warnings: []string{
			"The swap file is allocated from free space on " + volume.Name + ". Capacity alerts and the storage guard will include it.",
			"Swap files are supported on ext4 and XFS. NFS volumes are not eligible.",
		},
	}
	return plan, nil
}

func (s *Server) buildStorageSwapDevicePlan(ctx context.Context, req storageSwapPlanRequest) (storageSwapPlan, error) {
	if err := storageSwapToolError(map[string]bool{
		"mkswap": hasStorageTool("mkswap"), "swapon": hasStorageTool("swapon"),
		"swapoff": hasStorageTool("swapoff"), "wipefs": hasStorageTool("wipefs"),
		"blkid": hasStorageTool("blkid"), "systemctl": hasStorageTool("systemctl"),
	}, "mkswap", "swapon", "swapoff", "wipefs", "blkid", "systemctl"); err != nil {
		return storageSwapPlan{}, err
	}
	devicePath := strings.TrimSpace(req.DevicePath)
	if devicePath == "" {
		return storageSwapPlan{}, fmt.Errorf("choose a disk for swap")
	}
	devices, supported, message := s.listStorageDisks(ctx)
	if !supported {
		return storageSwapPlan{}, fmt.Errorf("%s", message)
	}
	device, ok := findStorageDisk(devices, devicePath, req.DeviceSerial, req.DeviceWWN)
	if !ok || device.Type != "disk" {
		return storageSwapPlan{}, fmt.Errorf("the selected disk is no longer present")
	}
	if device.Path != devicePath {
		return storageSwapPlan{}, fmt.Errorf("the selected disk path changed; refresh the disk inventory")
	}
	if device.SystemProtected || device.Mountpoint != "" || len(device.Children) > 0 {
		return storageSwapPlan{}, fmt.Errorf("the selected disk is protected, mounted or already partitioned")
	}
	if device.SizeBytes < storageSwapMinBytes {
		return storageSwapPlan{}, fmt.Errorf("the selected disk is smaller than the minimum swap size")
	}
	if entries, err := readStorageSwapEntries(); err == nil {
		for _, entry := range entries {
			if sameStoragePath(entry.Path, device.Path) {
				return storageSwapPlan{}, fmt.Errorf("that disk is already active swap")
			}
		}
	}
	name := storageSwapTargetName("swap", filepath.Base(device.Path))
	label := storageSwapLabel(name)
	plan := storageSwapPlan{
		Kind: "swap_create", Mode: "device", Name: name,
		DevicePath: device.Path, DeviceSerial: device.Serial, DeviceWWN: device.WWN,
		SizeBytes: device.SizeBytes, Label: label, Target: device.Path,
		RequireDestroy: true, ConfirmValue: "ERASE " + device.Path,
		Commands: []string{
			"wipefs -a " + device.Path,
			fmt.Sprintf("mkswap -L %s %s", label, device.Path),
			"fstab: UUID=<swap-uuid> none swap sw 0 0",
			"swapon " + device.Path,
		},
		Warnings: []string{
			"The whole disk " + device.Path + " will be erased and dedicated to swap.",
			"All existing data and filesystem signatures on that disk will be destroyed.",
			"The disk serial or WWN and its blank state are rechecked before the first write.",
		},
	}
	if device.HasSignatures {
		plan.Warnings = append(plan.Warnings, "The disk currently contains filesystem signatures; this plan removes them.")
	}
	return plan, nil
}

func hasStorageTool(name string) bool {
	_, err := storageLookPath(name)
	return err == nil
}

func (s *Server) createStorageSwapOperation(ctx context.Context, sess *sessionRecord, plan storageSwapPlan) (string, string, time.Time, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", "", time.Time{}, err
	}
	hash := canonicalPlanHash(raw)
	expires := time.Now().UTC().Add(10 * time.Minute)
	var id string
	err = s.db.QueryRow(ctx, `
		INSERT INTO control.storage_operations
		  (kind, status, plan, plan_hash, plan_expires_at, step, total_bytes, actor_id, actor_email)
		VALUES ('swap_create', 'planned', $1::jsonb, $2, $3, 'Ready to execute', $4,
		        NULLIF($5, '')::uuid, $6)
		RETURNING id::text
	`, string(raw), hash, expires, plan.SizeBytes, sess.User.ID, sess.User.Email).Scan(&id)
	if err != nil {
		return "", "", time.Time{}, err
	}
	token := s.signCSRF("storage-swap:" + id + ":" + hash)
	return id, token, expires, nil
}

func (s *Server) handleStorageSwapPlan(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var req storageSwapPlanRequest
	if err := decodeJSON(w, r, &req, 32<<10); err != nil {
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
	plan, err := s.buildStorageSwapPlan(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusConflict, "PLAN", err.Error())
		return
	}
	if plan.RequireDestroy && !sess.User.PermSet.Has("storage.destroy") {
		writeError(w, http.StatusForbidden, "PERMISSION", "Creating swap on a dedicated disk requires the storage.destroy permission")
		return
	}
	operationID, token, expires, err := s.createStorageSwapOperation(r.Context(), sess, plan)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "storage.swap_planned", operationID, "Planned a swap creation", map[string]any{
		"mode": plan.Mode, "target": plan.Target, "sizeBytes": plan.SizeBytes,
		"requireDestroy": plan.RequireDestroy,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "operationId": operationID, "token": token,
		"expiresAt": expires.Format(time.RFC3339), "plan": plan,
	})
}

func (s *Server) handleStorageSwapExecute(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		OperationID  string `json:"operationId"`
		Token        string `json:"token"`
		Confirm      bool   `json:"confirm"`
		ConfirmValue string `json:"confirmValue"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !body.Confirm {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Confirm the swap plan before executing it")
		return
	}
	op, err := s.loadStorageOperation(r.Context(), strings.TrimSpace(body.OperationID))
	if err != nil {
		writeFromError(w, err)
		return
	}
	if op.Kind != "swap_create" {
		writeError(w, http.StatusConflict, "OPERATION", "That is not a swap operation")
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
		!constantTimeEqual(body.Token, s.signCSRF("storage-swap:"+op.ID+":"+op.PlanHash)) {
		writeError(w, http.StatusForbidden, "CONFIRM", "The plan token is invalid. Create a new plan.")
		return
	}
	var plan storageSwapPlan
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
	s.actorAudit(r, sess, "storage.swap_started", op.ID, "Started a swap creation", map[string]any{"mode": plan.Mode, "target": plan.Target})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "operationId": op.ID})
}
