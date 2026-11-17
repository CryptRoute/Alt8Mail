package httpapi

import (
	"context"
	"fmt"
	"time"
)

// storageUploadGuard reads the cached capacity state maintained by the Control
// Plane sampler. Uploads are refused with 507 only while the state is fresh;
// stale telemetry must never take a working mail service offline.
func (s *Server) storageUploadGuard(ctx context.Context, email string) (bool, string) {
	if s == nil || s.store == nil || s.store.DB == nil || email == "" {
		return false, ""
	}
	var volume, level string
	var percent float64
	var checkedAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		WITH target AS (
		  SELECT COALESCE(
		    m.storage_volume_id,
		    (SELECT mounted.id FROM control.storage_volumes mounted
		     WHERE m.mail_home IS NOT NULL AND m.mail_home <> ''
		       AND (m.mail_home = mounted.mount_path OR m.mail_home LIKE mounted.mount_path || '/%')
		     ORDER BY length(mounted.mount_path) DESC LIMIT 1),
		    (SELECT id FROM control.storage_volumes WHERE mount_path = $2 LIMIT 1)
		  ) AS volume_id
		  FROM public.mailboxes m
		  WHERE m.email = $1 AND m.active = true
		  LIMIT 1
		)
		SELECT v.name, v.capacity_state, v.capacity_percent, v.capacity_checked_at
		FROM target t
		JOIN control.storage_volumes v ON v.id = t.volume_id
		CROSS JOIN control.storage_policy p
		WHERE p.id = true AND p.guard_enabled = true
	`, email, s.cfg.MaildirBase).Scan(&volume, &level, &percent, &checkedAt)
	if err != nil || checkedAt == nil || time.Since(*checkedAt) > 5*time.Minute {
		return false, ""
	}
	switch level {
	case "critical", "emergency":
		return true, volume + " storage is at " + formatPercent(percent) + "; uploads are disabled until space is freed."
	default:
		return false, ""
	}
}

func formatPercent(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f%%", value)
	}
	return fmt.Sprintf("%.1f%%", value)
}
