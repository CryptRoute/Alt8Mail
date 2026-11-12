package control

import (
	"context"
	"strings"
	"time"
)

var filesystemStatLookup = readFilesystemStat

type storageOverviewPayload struct {
	Volumes     []storageVolumeView `json:"volumes"`
	Policy      storagePolicy       `json:"policy"`
	Totals      storageTotals       `json:"totals"`
	Doctor      storageDoctor       `json:"doctor"`
	Host        storageHostInfo     `json:"host"`
	Alerts      []storageAlertView  `json:"alerts"`
	GeneratedAt string              `json:"generatedAt"`
}

type storageHostInfo struct {
	VmailRoot   string `json:"vmailRoot"`
	StorageRoot string `json:"storageRoot"`
}

func (s *Server) storageOverview(ctx context.Context) (storageOverviewPayload, error) {
	volumes, err := s.storageVolumeRows(ctx)
	if err != nil {
		return storageOverviewPayload{}, err
	}
	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		return storageOverviewPayload{}, err
	}
	views := make([]storageVolumeView, 0, len(volumes))
	totals := storageTotals{}
	for _, volume := range volumes {
		view := storageVolumeView{storageVolume: volume, Topology: storageVolumeTopology(volume)}
		if volume.State != "retired" && volume.State != "wiped" {
			stat, statErr := filesystemStatLookup(volume.MountPath)
			if statErr == nil {
				view.Online = true
				view.TotalBytes = stat.TotalBytes
				view.UsedBytes = stat.UsedBytes
				view.FreeBytes = stat.FreeBytes
				view.UsedPercent = stat.UsedPercent
				view.InodeTotal = stat.InodeTotal
				view.InodeUsed = stat.InodeUsed
				view.InodePercent = stat.InodePercent
				view.CapacityTotalBytes = stat.TotalBytes
				view.CapacityUsedBytes = stat.UsedBytes
				view.CapacityFreeBytes = stat.FreeBytes
				view.CapacityPercent = stat.UsedPercent
				view.CapacityState = storagePercentLevel(policy, stat.UsedPercent)
				totals.TotalBytes += stat.TotalBytes
				totals.UsedBytes += stat.UsedBytes
				totals.FreeBytes += stat.FreeBytes
				view.Forecast = s.storageForecast(ctx, volume, policy)
			} else {
				view.CapacityState = "offline"
			}
		}
		view.History = s.storageHistory(ctx, volume.ID, 120)
		if len(view.History) == 0 && view.Online {
			view.History = []storageSamplePoint{{
				At: nowRFC3339(), UsedBytes: view.UsedBytes, UsedPercent: view.UsedPercent,
			}}
		}
		views = append(views, view)
	}
	if totals.TotalBytes > 0 {
		totals.UsedPercent = float64(totals.UsedBytes) / float64(totals.TotalBytes) * 100
	}
	return storageOverviewPayload{
		Volumes:     views,
		Policy:      policy,
		Totals:      totals,
		Doctor:      s.storageDoctor(ctx),
		Host:        storageHostInfo{VmailRoot: s.cfg.VmailRoot, StorageRoot: s.cfg.StorageRoot},
		Alerts:      s.storageActiveAlerts(ctx),
		GeneratedAt: nowRFC3339(),
	}, nil
}

func (s *Server) storageHistory(ctx context.Context, volumeID string, limit int) []storageSamplePoint {
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	rows, err := s.db.Query(ctx, `
		SELECT sampled_at, total_bytes, used_bytes
		FROM (
		  SELECT sampled_at, total_bytes, used_bytes
		  FROM control.storage_samples
		  WHERE volume_id = $1 AND sampled_at > now() - interval '24 hours'
		  ORDER BY sampled_at DESC
		  LIMIT $2
		) recent
		ORDER BY sampled_at ASC
	`, volumeID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []storageSamplePoint{}
	for rows.Next() {
		var at time.Time
		var total, used uint64
		if err := rows.Scan(&at, &total, &used); err != nil {
			continue
		}
		percent := 0.0
		if total > 0 {
			percent = float64(used) / float64(total) * 100
		}
		out = append(out, storageSamplePoint{
			At: at.UTC().Format(time.RFC3339), UsedBytes: used, UsedPercent: percent,
		})
	}
	return out
}

func (s *Server) runStorageSampler(ctx context.Context) {
	s.sampleStorageVolumes(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleStorageVolumes(ctx)
		}
	}
}

func (s *Server) sampleStorageVolumes(ctx context.Context) {
	if s.db == nil {
		return
	}
	if err := s.ensureStorageVolumes(ctx); err != nil {
		s.logger.Debug("storage volume discovery failed", "err", err)
		return
	}
	volumes, err := s.storageVolumeRows(ctx)
	if err != nil {
		s.logger.Debug("storage volume lookup failed", "err", err)
		return
	}
	policy, err := s.storagePolicyRow(ctx)
	if err != nil {
		s.logger.Debug("storage policy lookup failed", "err", err)
		return
	}
	for _, volume := range volumes {
		if volume.State == "retired" || volume.State == "wiped" {
			continue
		}
		stat, statErr := filesystemStatLookup(volume.MountPath)
		if statErr != nil {
			if volume.CapacityState != "offline" {
				_, _ = s.db.Exec(ctx, `
					UPDATE control.storage_volumes
					SET capacity_state = 'offline', capacity_checked_at = now(), updated_at = now()
					WHERE id = $1
				`, volume.ID)
				_, _ = s.db.Exec(ctx, `
					UPDATE control.storage_alerts SET status = 'resolved', last_seen_at = now()
					WHERE volume_id = $1 AND status IN ('active', 'acknowledged')
				`, volume.ID)
				s.notifyEvent(ctx, "storage.volume_offline", map[string]any{
					"volumeId": volume.ID, "volume": volume.Name, "mountPath": volume.MountPath,
					"message": "The registered storage volume " + volume.Name + " is no longer readable at " + volume.MountPath + ".",
				})
			}
			continue
		}
		level := storagePercentLevel(policy, stat.UsedPercent)
		if volume.CapacityState == "offline" {
			s.notifyEvent(ctx, "storage.volume_online", map[string]any{
				"volumeId": volume.ID, "volume": volume.Name, "mountPath": volume.MountPath,
				"message": "The registered storage volume " + volume.Name + " is readable again.",
			})
		}
		_, _ = s.db.Exec(ctx, `
			INSERT INTO control.storage_samples
			  (volume_id, sampled_at, total_bytes, used_bytes, free_bytes, inode_used, inode_total)
			VALUES ($1, now(), $2, $3, $4, $5, $6)
			ON CONFLICT (volume_id, sampled_at) DO NOTHING
		`, volume.ID, stat.TotalBytes, stat.UsedBytes, stat.FreeBytes, stat.InodeUsed, stat.InodeTotal)
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_volumes
			SET size_bytes = $2, last_seen_at = now(), updated_at = now(),
			    capacity_total_bytes = $2, capacity_used_bytes = $3,
			    capacity_free_bytes = $4, capacity_percent = $5,
			    capacity_state = $6, capacity_checked_at = now()
			WHERE id = $1
		`, volume.ID, stat.TotalBytes, stat.UsedBytes, stat.FreeBytes, stat.UsedPercent, level)
		s.reconcileStorageAlert(ctx, volume, policy, level, stat.UsedPercent, stat.FreeBytes, stat.TotalBytes)
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM control.storage_samples WHERE sampled_at < now() - interval '30 days'`)
}

func storageVolumeTopology(volume storageVolume) string {
	fsType := strings.ToLower(strings.TrimSpace(volume.FSType))
	device := strings.ToLower(strings.TrimSpace(volume.DevicePath))
	switch {
	case fsType == "nfs" || fsType == "nfs4" || strings.Contains(strings.ToLower(volume.MountOptions), "nfs"):
		return "nfs"
	case strings.HasPrefix(device, "/dev/md"):
		return "raid"
	case strings.Contains(device, "mpath"):
		return "multipath"
	case strings.HasPrefix(device, "/dev/mapper/"):
		return "mapped"
	default:
		return "local"
	}
}
