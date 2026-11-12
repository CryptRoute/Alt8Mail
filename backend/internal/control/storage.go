package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type storageVolume struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	MountPath      string     `json:"mountPath"`
	FSUUID         string     `json:"fsUuid"`
	DevicePath     string     `json:"devicePath"`
	FSType         string     `json:"fsType"`
	Role           string     `json:"role"`
	Managed        bool       `json:"managed"`
	State          string     `json:"state"`
	SizeBytes      uint64     `json:"sizeBytes"`
	MountOptions   string     `json:"mountOptions"`
	FstabManaged   bool       `json:"fstabManaged"`
	DeviceSerial   string     `json:"deviceSerial"`
	DeviceWWN      string     `json:"deviceWwn"`
	MountUnit      string     `json:"mountUnit"`
	RebootVerified bool       `json:"rebootVerified"`
	VerifiedAt     *time.Time `json:"verifiedAt,omitempty"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastSeenAt     *time.Time `json:"lastSeenAt,omitempty"`

	CapacityTotalBytes uint64     `json:"capacityTotalBytes"`
	CapacityUsedBytes  uint64     `json:"capacityUsedBytes"`
	CapacityFreeBytes  uint64     `json:"capacityFreeBytes"`
	CapacityPercent    float64    `json:"capacityPercent"`
	CapacityState      string     `json:"capacityState"`
	CapacityCheckedAt  *time.Time `json:"capacityCheckedAt,omitempty"`
}

type storagePolicy struct {
	DefaultVolumeID     string    `json:"defaultVolumeId"`
	AdvisoryPercent     int       `json:"advisoryPercent"`
	WarningPercent      int       `json:"warningPercent"`
	SeriousPercent      int       `json:"seriousPercent"`
	CriticalPercent     int       `json:"criticalPercent"`
	EmergencyPercent    int       `json:"emergencyPercent"`
	ReservePercent      int       `json:"reservePercent"`
	MinFreeBytes        int64     `json:"minFreeBytes"`
	MigrationGraceHours int       `json:"migrationGraceHours"`
	MaxConcurrentMoves  int       `json:"maxConcurrentMoves"`
	AlertRearmHours     int       `json:"alertRearmHours"`
	ForecastAlertDays   int       `json:"forecastAlertDays"`
	GuardEnabled        bool      `json:"guardEnabled"`
	ProvisioningEnabled bool      `json:"provisioningEnabled"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type storageSamplePoint struct {
	At          string  `json:"at"`
	UsedBytes   uint64  `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

type storageVolumeView struct {
	storageVolume
	TotalBytes   uint64               `json:"totalBytes"`
	UsedBytes    uint64               `json:"usedBytes"`
	FreeBytes    uint64               `json:"freeBytes"`
	UsedPercent  float64              `json:"usedPercent"`
	InodeTotal   uint64               `json:"inodeTotal"`
	InodeUsed    uint64               `json:"inodeUsed"`
	InodePercent float64              `json:"inodePercent"`
	Online       bool                 `json:"online"`
	Topology     string               `json:"topology"`
	History      []storageSamplePoint `json:"history"`
	Forecast     *storageForecast     `json:"forecast,omitempty"`
}

type storageTotals struct {
	TotalBytes  uint64  `json:"totalBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

type storageIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Message  string `json:"message"`
}

type storageDoctor struct {
	UserDBReady bool           `json:"userdbReady"`
	ConfigPath  string         `json:"configPath"`
	CheckedAt   string         `json:"checkedAt"`
	Issues      []storageIssue `json:"issues"`
}

type mountInspection struct {
	FSUUID  string
	Device  string
	FSType  string
	Options string
	Stat    filesystemStat
	HasStat bool
}

var storageVolumeColumns = `id::text, name, mount_path, COALESCE(fs_uuid,''), device_path,
	fs_type, role, managed, state, size_bytes, mount_options, fstab_managed,
	device_serial, device_wwn, mount_unit, reboot_verified, verified_at,
	created_by, created_at, last_seen_at,
	capacity_total_bytes, capacity_used_bytes, capacity_free_bytes,
	capacity_percent, capacity_state, capacity_checked_at`

func (s *Server) registerStorageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/storage/overview", s.require("storage.read", s.handleStorageOverview))
	mux.HandleFunc("GET /control/api/storage/disks", s.require("storage.read", s.handleStorageDisks))
	mux.HandleFunc("GET /control/api/storage/placements", s.require("storage.read", s.handleStoragePlacements))
	mux.HandleFunc("POST /control/api/storage/assignments", s.require("storage.manage", s.handleStorageAssignment))
	mux.HandleFunc("POST /control/api/storage/doctor/dovecot", s.require("storage.manage", s.handleStorageDovecotUpdate))
	s.registerStorageUsageRoutes(mux)
}

func (s *Server) handleStorageOverview(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	if err := s.ensureStorageVolumes(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE", "Could not inspect storage volumes: "+err.Error())
		return
	}
	payload, err := s.storageOverview(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func scanStorageVolume(row pgx.Row) (storageVolume, error) {
	var volume storageVolume
	err := row.Scan(
		&volume.ID, &volume.Name, &volume.MountPath, &volume.FSUUID, &volume.DevicePath,
		&volume.FSType, &volume.Role, &volume.Managed, &volume.State, &volume.SizeBytes,
		&volume.MountOptions, &volume.FstabManaged, &volume.DeviceSerial, &volume.DeviceWWN,
		&volume.MountUnit, &volume.RebootVerified, &volume.VerifiedAt, &volume.CreatedBy,
		&volume.CreatedAt, &volume.LastSeenAt,
		&volume.CapacityTotalBytes, &volume.CapacityUsedBytes, &volume.CapacityFreeBytes,
		&volume.CapacityPercent, &volume.CapacityState, &volume.CapacityCheckedAt,
	)
	return volume, err
}

func (s *Server) storageVolumeRows(ctx context.Context) ([]storageVolume, error) {
	rows, err := s.db.Query(ctx, `SELECT `+storageVolumeColumns+`
		FROM control.storage_volumes
		ORDER BY (name = 'legacy') DESC, managed DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storageVolume{}
	for rows.Next() {
		volume, err := scanStorageVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, volume)
	}
	return out, rows.Err()
}

func (s *Server) storagePolicyRow(ctx context.Context) (storagePolicy, error) {
	var policy storagePolicy
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(default_volume_id::text, ''), advisory_percent, warning_percent,
		       serious_percent, critical_percent, emergency_percent, reserve_percent,
		       min_free_bytes, migration_grace_hours, max_concurrent_moves,
		       alert_rearm_hours, forecast_alert_days, guard_enabled, provisioning_enabled, updated_at
		FROM control.storage_policy WHERE id = true
	`).Scan(
		&policy.DefaultVolumeID, &policy.AdvisoryPercent, &policy.WarningPercent,
		&policy.SeriousPercent, &policy.CriticalPercent, &policy.EmergencyPercent,
		&policy.ReservePercent, &policy.MinFreeBytes, &policy.MigrationGraceHours,
		&policy.MaxConcurrentMoves, &policy.AlertRearmHours, &policy.ForecastAlertDays,
		&policy.GuardEnabled, &policy.ProvisioningEnabled, &policy.UpdatedAt,
	)
	return policy, err
}

func (s *Server) ensureStorageVolumes(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	if err := s.ensureLegacyStorageVolume(ctx); err != nil {
		return err
	}
	s.discoverStorageVolumes(ctx)
	return nil
}

func (s *Server) ensureLegacyStorageVolume(ctx context.Context) error {
	root := strings.TrimSpace(s.cfg.VmailRoot)
	if root == "" {
		return nil
	}
	root = filepath.Clean(root)
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM control.storage_volumes WHERE mount_path = $1`, root).Scan(&id)
	if err == nil {
		s.setDefaultVolumeIfEmpty(ctx, id)
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	inspection := s.inspectMount(ctx, root)
	var fsUUID any
	if inspection.FSUUID != "" {
		fsUUID = inspection.FSUUID
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.storage_volumes
		  (name, mount_path, fs_uuid, device_path, fs_type, role, managed, state,
		   size_bytes, mount_options, fstab_managed, created_by, last_seen_at)
		VALUES ('legacy', $1, $2, $3, $4, 'mail', false, 'online', $5, $6, false, 'legacy', now())
		ON CONFLICT (mount_path) DO NOTHING
	`, root, fsUUID, inspection.Device, inspection.FSType, inspection.Stat.TotalBytes, inspection.Options)
	if err != nil {
		return err
	}
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM control.storage_volumes WHERE mount_path = $1`, root).Scan(&id); err == nil {
		s.setDefaultVolumeIfEmpty(ctx, id)
	}
	return nil
}

func (s *Server) setDefaultVolumeIfEmpty(ctx context.Context, volumeID string) {
	if volumeID == "" {
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_policy
		SET default_volume_id = COALESCE(default_volume_id, $1), updated_at = now()
		WHERE id = true AND default_volume_id IS NULL
	`, volumeID)
}

func (s *Server) discoverStorageVolumes(ctx context.Context) {
	root := strings.TrimSpace(s.cfg.StorageRoot)
	if root == "" {
		return
	}
	root = filepath.Clean(root)
	entries, err := readMountEntries()
	if err != nil {
		return
	}
	for _, entry := range entries {
		mountPath := filepath.Clean(entry.MountPoint)
		if mountPath == "/" || mountPath == filepath.Clean(s.cfg.VmailRoot) {
			continue
		}
		if !pathWithin(root, mountPath) || isPseudoFilesystem(entry.FSType) {
			continue
		}
		var exists int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM control.storage_volumes WHERE mount_path = $1`, mountPath).Scan(&exists); err != nil || exists > 0 {
			continue
		}
		inspection := s.inspectMount(ctx, mountPath)
		if !inspection.HasStat {
			continue
		}
		name := s.uniqueStorageVolumeName(ctx, storageNameFromPath(mountPath))
		var fsUUID any
		if inspection.FSUUID != "" {
			fsUUID = inspection.FSUUID
		}
		_, _ = s.db.Exec(ctx, `
			INSERT INTO control.storage_volumes
			  (name, mount_path, fs_uuid, device_path, fs_type, role, managed, state,
			   size_bytes, mount_options, fstab_managed, created_by, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, 'mail', false, 'online', $6, $7, false, 'discovered', now())
			ON CONFLICT (mount_path) DO NOTHING
		`, name, mountPath, fsUUID, inspection.Device, inspection.FSType, inspection.Stat.TotalBytes, inspection.Options)
	}
}

func (s *Server) uniqueStorageVolumeName(ctx context.Context, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "volume"
	}
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		var exists int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM control.storage_volumes WHERE name = $1`, name).Scan(&exists); err == nil && exists == 0 {
			return name
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().Unix())
}

func (s *Server) inspectMount(ctx context.Context, path string) mountInspection {
	out := mountInspection{}
	if stat, err := filesystemStatLookup(path); err == nil {
		out.Stat = stat
		out.HasStat = true
	}
	if result, err := runCommand(ctx, 5*time.Second, "findmnt", "-n", "-o", "SOURCE,FSTYPE,OPTIONS", "--target", path); err == nil {
		fields := strings.Fields(result.Stdout)
		if len(fields) >= 3 {
			out.Device = fields[0]
			out.FSType = fields[1]
			out.Options = fields[2]
		}
	}
	if result, err := runCommand(ctx, 5*time.Second, "findmnt", "-n", "-o", "UUID", "--target", path); err == nil {
		out.FSUUID = strings.TrimSpace(result.Stdout)
	}
	return out
}

func (s *Server) storageDoctor(ctx context.Context) storageDoctor {
	doctor := storageDoctor{
		CheckedAt:  nowRFC3339(),
		ConfigPath: filepath.Join(s.cfg.DovecotDir, "dovecot-sql.conf.ext"),
		Issues:     []storageIssue{},
	}
	raw, err := os.ReadFile(doctor.ConfigPath)
	if err != nil {
		doctor.Issues = append(doctor.Issues, storageIssue{
			Code: "dovecot_config_unreadable", Severity: "warning",
			Title:   "Dovecot userdb configuration is not readable",
			Message: "The storage doctor could not read the userdb SQL configuration. Placement on new volumes stays disabled until it can verify the managed query.",
		})
	} else if !strings.Contains(string(raw), "mail_home") {
		doctor.Issues = append(doctor.Issues, storageIssue{
			Code: "dovecot_userdb_legacy", Severity: "warning",
			Title:   "Dovecot still uses the legacy mailbox home",
			Message: "The installed userdb query does not read mail_home. Existing mail is unaffected, but new placements on another volume cannot work until the managed query is applied.",
		})
	} else {
		doctor.UserDBReady = true
	}
	if info, err := os.Stat(s.cfg.VmailRoot); err != nil || !info.IsDir() {
		doctor.Issues = append(doctor.Issues, storageIssue{
			Code: "legacy_root_missing", Severity: "critical",
			Title:   "The legacy mail root is missing",
			Message: fmt.Sprintf("%s is not a readable directory. Existing mailboxes may be offline.", s.cfg.VmailRoot),
		})
	}
	if s.cfg.StorageRoot != "" {
		if info, err := os.Stat(s.cfg.StorageRoot); err != nil || !info.IsDir() {
			doctor.Issues = append(doctor.Issues, storageIssue{
				Code: "storage_root_missing", Severity: "info",
				Title:   "The managed storage root is not present",
				Message: fmt.Sprintf("%s will be created when the first managed disk is added. Existing volumes can still be registered.", s.cfg.StorageRoot),
			})
		}
	}
	return doctor
}

func isPseudoFilesystem(fsType string) bool {
	switch strings.ToLower(strings.TrimSpace(fsType)) {
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup", "cgroup2",
		"overlay", "mqueue", "securityfs", "debugfs", "tracefs", "pstore",
		"bpf", "autofs", "hugetlbfs", "fusectl", "configfs", "ramfs",
		"squashfs", "nsfs", "binfmt_misc", "rpc_pipefs", "efivarfs":
		return true
	}
	return false
}

var storageNamePattern = regexp.MustCompile(`[^a-z0-9]+`)

func storageNameFromPath(path string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(path)))
	base = strings.Trim(storageNamePattern.ReplaceAllString(base, "-"), "-")
	if len(base) > 48 {
		base = base[:48]
	}
	if base == "" {
		return "volume"
	}
	return base
}
