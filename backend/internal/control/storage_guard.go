package control

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type storageGuardState struct {
	VolumeName string
	Level      string
	Percent    float64
	FreeBytes  uint64
	CheckedAt  *time.Time
	Guard      bool
}

func (s *Server) storageGuardForEmail(ctx context.Context, email string) (bool, string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return false, ""
	}
	var state storageGuardState
	err := s.db.QueryRow(ctx, `
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
		SELECT v.name, v.capacity_state, v.capacity_percent, v.capacity_free_bytes,
		       v.capacity_checked_at, p.guard_enabled
		FROM target t
		JOIN control.storage_volumes v ON v.id = t.volume_id
		CROSS JOIN control.storage_policy p
		WHERE p.id = true
	`, email, s.cfg.VmailRoot).Scan(
		&state.VolumeName, &state.Level, &state.Percent, &state.FreeBytes,
		&state.CheckedAt, &state.Guard,
	)
	if err != nil {
		return false, ""
	}
	return storageGuardDecision(state, time.Now())
}

func storageGuardDecision(state storageGuardState, now time.Time) (bool, string) {
	if !state.Guard || state.CheckedAt == nil || now.Sub(*state.CheckedAt) > 5*time.Minute {
		return false, ""
	}
	switch state.Level {
	case "critical", "emergency":
		return true, fmt.Sprintf("Mailbox storage volume %s is %.1f%% full; new mail is temporarily deferred.", state.VolumeName, state.Percent)
	default:
		return false, ""
	}
}
