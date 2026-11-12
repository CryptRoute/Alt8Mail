package control

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type storageForecastPoint struct {
	At        string  `json:"at"`
	UsedBytes uint64  `json:"usedBytes"`
	Projected bool    `json:"projected,omitempty"`
	Percent   float64 `json:"percent"`
}

type storageThresholdForecast struct {
	Level   string   `json:"level"`
	Percent int      `json:"percent"`
	Bytes   uint64   `json:"bytes"`
	Days    *float64 `json:"days"`
}

type storageForecast struct {
	SampleDays         int                        `json:"sampleDays"`
	EnoughData         bool                       `json:"enoughData"`
	DailyGrowthBytes   int64                      `json:"dailyGrowthBytes"`
	DailyGrowthPercent float64                    `json:"dailyGrowthPercent"`
	CurrentUsedBytes   uint64                     `json:"currentUsedBytes"`
	Projected30Days    uint64                     `json:"projected30DaysBytes"`
	DaysToFull         *float64                   `json:"daysToFull"`
	Thresholds         []storageThresholdForecast `json:"thresholds"`
	Points             []storageForecastPoint     `json:"points"`
}

type storageAlertView struct {
	ID           string     `json:"id"`
	VolumeID     string     `json:"volumeId"`
	VolumeName   string     `json:"volumeName"`
	Level        string     `json:"level"`
	Status       string     `json:"status"`
	Percent      float64    `json:"percent"`
	FreeBytes    uint64     `json:"freeBytes"`
	FirstSeenAt  time.Time  `json:"firstSeenAt"`
	LastSeenAt   time.Time  `json:"lastSeenAt"`
	LastNotified *time.Time `json:"lastNotifiedAt,omitempty"`
	NotifyCount  int        `json:"notifyCount"`
	Acknowledged *time.Time `json:"acknowledgedAt,omitempty"`
	Message      string     `json:"message"`
}

func storagePercentLevel(policy storagePolicy, percent float64) string {
	if percent >= float64(policy.EmergencyPercent) {
		return "emergency"
	}
	if percent >= float64(policy.CriticalPercent) {
		return "critical"
	}
	if percent >= float64(policy.SeriousPercent) {
		return "serious"
	}
	if percent >= float64(policy.WarningPercent) {
		return "warning"
	}
	if percent >= float64(policy.AdvisoryPercent) {
		return "advisory"
	}
	return "ok"
}

func storageReserveBytes(policy storagePolicy, total uint64) uint64 {
	reserve := uint64(0)
	if policy.MinFreeBytes > 0 {
		reserve = uint64(policy.MinFreeBytes)
	}
	if policy.ReservePercent > 0 && total > 0 {
		if fromPercent := total * uint64(policy.ReservePercent) / 100; fromPercent > reserve {
			reserve = fromPercent
		}
	}
	return reserve
}

func storageThresholdBytes(total uint64, percent int) uint64 {
	if total == 0 || percent <= 0 {
		return 0
	}
	return total * uint64(percent) / 100
}

// storageForecast uses the last 14 days of daily high-water samples. It is a
// planning aid: a negative or flat slope produces no projected-full date.
func (s *Server) storageForecast(ctx context.Context, volume storageVolume, policy storagePolicy) *storageForecast {
	forecast := &storageForecast{Thresholds: []storageThresholdForecast{}, Points: []storageForecastPoint{}}
	rows, err := s.db.Query(ctx, `
		SELECT date_trunc('day', sampled_at) AS day,
		       (array_agg(used_bytes ORDER BY sampled_at DESC))[1] AS used
		FROM control.storage_samples
		WHERE volume_id = $1
		  AND sampled_at >= date_trunc('day', now()) - interval '13 days'
		GROUP BY 1
		ORDER BY 1
	`, volume.ID)
	if err != nil {
		return forecast
	}
	defer rows.Close()
	total := volume.CapacityTotalBytes
	if total == 0 {
		total = volume.SizeBytes
	}
	points := []struct {
		at   time.Time
		used uint64
	}{}
	for rows.Next() {
		var at time.Time
		var used uint64
		if rows.Scan(&at, &used) != nil {
			continue
		}
		points = append(points, struct {
			at   time.Time
			used uint64
		}{at.UTC(), used})
	}
	if len(points) == 0 {
		return forecast
	}
	forecast.SampleDays = len(points)
	first := points[0].at
	lastUsed := points[len(points)-1].used
	forecast.CurrentUsedBytes = lastUsed
	for _, point := range points {
		percent := 0.0
		if total > 0 {
			percent = float64(point.used) / float64(total) * 100
		}
		forecast.Points = append(forecast.Points, storageForecastPoint{
			At: point.at.Format(time.RFC3339), UsedBytes: point.used, Percent: percent,
		})
	}
	if len(points) >= 2 {
		var sumX, sumY, sumXY, sumXX float64
		n := float64(len(points))
		for _, point := range points {
			x := point.at.Sub(first).Hours() / 24
			y := float64(point.used)
			sumX += x
			sumY += y
			sumXY += x * y
			sumXX += x * x
		}
		denominator := n*sumXX - sumX*sumX
		if denominator != 0 {
			slope := (n*sumXY - sumX*sumY) / denominator
			forecast.DailyGrowthBytes = int64(math.Round(slope))
			if total > 0 {
				forecast.DailyGrowthPercent = slope / float64(total) * 100
			}
			if slope > 0 {
				forecast.EnoughData = true
				projected := float64(lastUsed) + slope*30
				if projected < 0 {
					projected = 0
				}
				forecast.Projected30Days = uint64(math.Round(projected))
				projectionAt := points[len(points)-1].at.Add(30 * 24 * time.Hour)
				percent := 0.0
				if total > 0 {
					percent = projected / float64(total) * 100
				}
				forecast.Points = append(forecast.Points, storageForecastPoint{
					At: projectionAt.Format(time.RFC3339), UsedBytes: forecast.Projected30Days,
					Projected: true, Percent: percent,
				})
				if lastUsed >= total {
					days := 0.0
					forecast.DaysToFull = &days
				} else {
					days := math.Round((float64(total)-float64(lastUsed))/slope*10) / 10
					forecast.DaysToFull = &days
				}
			}
		}
	}
	for _, threshold := range []struct {
		level   string
		percent int
	}{
		{"advisory", policy.AdvisoryPercent},
		{"warning", policy.WarningPercent},
		{"serious", policy.SeriousPercent},
		{"critical", policy.CriticalPercent},
		{"emergency", policy.EmergencyPercent},
	} {
		bytes := storageThresholdBytes(total, threshold.percent)
		item := storageThresholdForecast{Level: threshold.level, Percent: threshold.percent, Bytes: bytes}
		if forecast.EnoughData && forecast.DailyGrowthBytes > 0 {
			switch {
			case lastUsed >= bytes:
				days := 0.0
				item.Days = &days
			case bytes > lastUsed:
				days := math.Round((float64(bytes)-float64(lastUsed))/float64(forecast.DailyGrowthBytes)*10) / 10
				item.Days = &days
			}
		}
		forecast.Thresholds = append(forecast.Thresholds, item)
	}
	return forecast
}

func (s *Server) storageActiveAlerts(ctx context.Context) []storageAlertView {
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.volume_id::text, v.name, a.level, a.status,
		       COALESCE(v.capacity_percent,0), COALESCE(v.capacity_free_bytes,0),
		       a.first_seen_at, a.last_seen_at, a.last_notified_at, a.notify_count,
		       a.acknowledged_at, a.detail
		FROM control.storage_alerts a
		JOIN control.storage_volumes v ON v.id = a.volume_id
		WHERE a.status IN ('active', 'acknowledged')
		ORDER BY CASE a.level WHEN 'emergency' THEN 1 WHEN 'critical' THEN 2
		         WHEN 'serious' THEN 3 WHEN 'warning' THEN 4 ELSE 5 END,
		         a.last_seen_at DESC
	`)
	if err != nil {
		return []storageAlertView{}
	}
	defer rows.Close()
	out := []storageAlertView{}
	for rows.Next() {
		var item storageAlertView
		var detail []byte
		if rows.Scan(&item.ID, &item.VolumeID, &item.VolumeName, &item.Level, &item.Status,
			&item.Percent, &item.FreeBytes, &item.FirstSeenAt, &item.LastSeenAt,
			&item.LastNotified, &item.NotifyCount, &item.Acknowledged, &detail) != nil {
			continue
		}
		var meta map[string]any
		if json.Unmarshal(detail, &meta) == nil {
			if message, ok := meta["message"].(string); ok {
				item.Message = message
			}
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) reconcileStorageAlert(ctx context.Context, volume storageVolume, policy storagePolicy, level string, percent float64, freeBytes, totalBytes uint64) {
	if level == "offline" || level == "unknown" || level == "ok" {
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_alerts SET status = 'resolved', last_seen_at = now()
			WHERE volume_id = $1 AND status IN ('active', 'acknowledged')
			  AND level <> 'projected_full'
		`, volume.ID)
		return
	}
	message := fmt.Sprintf("%s is %.1f%% full (%s free of %s).", volume.Name, percent, humanBytes(freeBytes), humanBytes(totalBytes))
	switch level {
	case "critical":
		message = fmt.Sprintf("%s is %.1f%% full; new inbound mail to this volume is deferred. Free %s.", volume.Name, percent, humanBytes(freeBytes))
	case "emergency":
		message = fmt.Sprintf("%s is %.1f%% full; the storage guard is holding new writes. Free %s.", volume.Name, percent, humanBytes(freeBytes))
	}
	detail, _ := json.Marshal(map[string]any{
		"message": message, "percent": percent, "freeBytes": freeBytes, "totalBytes": totalBytes,
	})
	var status string
	var lastNotified *time.Time
	var notifyCount int
	err := s.db.QueryRow(ctx, `
		SELECT status, last_notified_at, notify_count FROM control.storage_alerts
		WHERE volume_id = $1 AND level = $2
	`, volume.ID, level).Scan(&status, &lastNotified, &notifyCount)
	notify := false
	if err != nil {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO control.storage_alerts
			  (volume_id, level, status, first_seen_at, last_seen_at, detail)
			VALUES ($1, $2, 'active', now(), now(), $3::jsonb)
			ON CONFLICT (volume_id, level) DO UPDATE SET
			  status = 'active', first_seen_at = now(), last_seen_at = now(), detail = EXCLUDED.detail
		`, volume.ID, level, string(detail))
		notify = true
	} else {
		newStatus := status
		if status == "resolved" {
			newStatus = "active"
		}
		if _, err := s.db.Exec(ctx, `
			UPDATE control.storage_alerts
			SET status = $3, last_seen_at = now(), detail = $4::jsonb,
			    first_seen_at = CASE WHEN status = 'resolved' THEN now() ELSE first_seen_at END
			WHERE volume_id = $1 AND level = $2
		`, volume.ID, level, newStatus, string(detail)); err != nil {
			return
		}
		rearm := time.Duration(policy.AlertRearmHours) * time.Hour
		if rearm <= 0 {
			rearm = 24 * time.Hour
		}
		switch {
		case volume.CapacityState != level:
			notify = true
		case lastNotified == nil:
			notify = status == "active"
		case time.Since(*lastNotified) >= rearm:
			notify = status == "active"
		}
		if status == "acknowledged" && volume.CapacityState == level {
			notify = false
		}
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_alerts SET status = 'resolved', last_seen_at = now()
		WHERE volume_id = $1 AND level <> $2 AND level <> 'projected_full'
		  AND status IN ('active', 'acknowledged')
	`, volume.ID, level)
	if !notify {
		return
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_alerts
		SET last_notified_at = now(), notify_count = notify_count + 1
		WHERE volume_id = $1 AND level = $2
	`, volume.ID, level); err != nil {
		return
	}
	s.notifyEvent(ctx, "storage.capacity", map[string]any{
		"volumeId": volume.ID, "volume": volume.Name, "level": level,
		"percent": percent, "freeBytes": freeBytes, "totalBytes": totalBytes,
		"message": message,
	})
}

func (s *Server) runStorageForecastMonitor(ctx context.Context) {
	check := func() {
		policy, err := s.storagePolicyRow(ctx)
		if err != nil {
			return
		}
		volumes, err := s.storageVolumeRows(ctx)
		if err != nil {
			return
		}
		for _, volume := range volumes {
			if volume.State == "retired" || volume.State == "wiped" || volume.CapacityState == "offline" {
				continue
			}
			forecast := s.storageForecast(ctx, volume, policy)
			if forecast.EnoughData && forecast.DaysToFull != nil && *forecast.DaysToFull <= float64(policy.ForecastAlertDays) {
				s.reconcileProjectedFullAlert(ctx, volume, policy, *forecast.DaysToFull, forecast.DailyGrowthBytes)
			} else {
				_, _ = s.db.Exec(ctx, `
					UPDATE control.storage_alerts SET status='resolved', last_seen_at=now()
					WHERE volume_id=$1 AND level='projected_full' AND status IN ('active','acknowledged')
				`, volume.ID)
			}
		}
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Minute):
			check()
		}
	}()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func (s *Server) reconcileProjectedFullAlert(ctx context.Context, volume storageVolume, policy storagePolicy, days float64, growth int64) {
	message := fmt.Sprintf("%s is projected to fill in %.1f days at %s/day. Plan a move or add capacity.", volume.Name, days, humanBytes(uint64(maxInt64(growth, 0))))
	detail, _ := json.Marshal(map[string]any{
		"message": message, "daysToFull": days, "dailyGrowthBytes": growth,
		"percent": volume.CapacityPercent, "freeBytes": volume.CapacityFreeBytes,
	})
	var status string
	var lastNotified *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT status, last_notified_at FROM control.storage_alerts
		WHERE volume_id=$1 AND level='projected_full'
	`, volume.ID).Scan(&status, &lastNotified)
	notify := false
	if err != nil {
		_, _ = s.db.Exec(ctx, `
			INSERT INTO control.storage_alerts (volume_id, level, status, first_seen_at, last_seen_at, detail)
			VALUES ($1, 'projected_full', 'active', now(), now(), $2::jsonb)
			ON CONFLICT (volume_id, level) DO UPDATE SET status='active', first_seen_at=now(), last_seen_at=now(), detail=EXCLUDED.detail
		`, volume.ID, string(detail))
		notify = true
	} else {
		newStatus := status
		if status == "resolved" {
			newStatus = "active"
		}
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_alerts SET status=$2, last_seen_at=now(), detail=$3::jsonb,
			  first_seen_at=CASE WHEN status='resolved' THEN now() ELSE first_seen_at END
			WHERE volume_id=$1 AND level='projected_full'
		`, volume.ID, newStatus, string(detail))
		rearm := time.Duration(policy.AlertRearmHours) * time.Hour
		if rearm <= 0 {
			rearm = 24 * time.Hour
		}
		if status == "active" && (lastNotified == nil || time.Since(*lastNotified) >= rearm) {
			notify = true
		}
	}
	if !notify {
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_alerts SET last_notified_at=now(), notify_count=notify_count+1
		WHERE volume_id=$1 AND level='projected_full'
	`, volume.ID)
	s.notifyEvent(ctx, "storage.capacity", map[string]any{
		"volumeId": volume.ID, "volume": volume.Name, "level": "projected_full",
		"daysToFull": days, "dailyGrowthBytes": growth, "message": message,
	})
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
