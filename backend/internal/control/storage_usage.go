package control

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

var storageUsageCommand = runCommand

type storageUsageRecord struct {
	VolumeID   string    `json:"volumeId"`
	VolumeName string    `json:"volumeName"`
	MailboxID  string    `json:"mailboxId"`
	Email      string    `json:"email"`
	Domain     string    `json:"domain"`
	Bytes      int64     `json:"bytes"`
	Messages   int64     `json:"messages"`
	ScannedAt  time.Time `json:"scannedAt"`
}

type storageDomainUsage struct {
	Domain     string `json:"domain"`
	VolumeID   string `json:"volumeId"`
	VolumeName string `json:"volumeName"`
	Bytes      int64  `json:"bytes"`
	Mailboxes  int64  `json:"mailboxes"`
}

type storageRecommendation struct {
	SourceVolumeID      string  `json:"sourceVolumeId"`
	SourceVolumeName    string  `json:"sourceVolumeName"`
	TargetVolumeID      string  `json:"targetVolumeId"`
	TargetVolumeName    string  `json:"targetVolumeName"`
	Scope               string  `json:"scope"`
	Domain              string  `json:"domain,omitempty"`
	MailboxID           string  `json:"mailboxId,omitempty"`
	Email               string  `json:"email,omitempty"`
	Bytes               int64   `json:"bytes"`
	Mailboxes           int64   `json:"mailboxes"`
	SourceBeforePercent float64 `json:"sourceBeforePercent"`
	SourceAfterPercent  float64 `json:"sourceAfterPercent"`
	TargetBeforePercent float64 `json:"targetBeforePercent"`
	TargetAfterPercent  float64 `json:"targetAfterPercent"`
	EstimatedSeconds    int64   `json:"estimatedSeconds"`
	RetentionBytes      int64   `json:"retentionBytes"`
	Message             string  `json:"message"`
}

type storageUsagePayload struct {
	Usage          []storageUsageRecord   `json:"usage"`
	Domains        []storageDomainUsage   `json:"domains"`
	Recommendation *storageRecommendation `json:"recommendation,omitempty"`
	ScannedAt      *time.Time             `json:"scannedAt,omitempty"`
	Scanning       bool                   `json:"scanning"`
}

func (s *Server) registerStorageUsageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/storage/usage", s.require("storage.read", s.handleStorageUsage))
	mux.HandleFunc("POST /control/api/storage/usage/scan", s.require("storage.manage", s.handleStorageUsageScan))
	mux.HandleFunc("POST /control/api/storage/alerts/{id}/acknowledge", s.require("storage.manage", s.handleStorageAlertAcknowledge))
}

func (s *Server) beginStorageUsageScan() bool {
	s.storageUsageMu.Lock()
	defer s.storageUsageMu.Unlock()
	if s.storageUsageRunning {
		return false
	}
	s.storageUsageRunning = true
	return true
}

func (s *Server) endStorageUsageScan() {
	s.storageUsageMu.Lock()
	s.storageUsageRunning = false
	s.storageUsageMu.Unlock()
}

func (s *Server) storageUsageScanning() bool {
	s.storageUsageMu.Lock()
	defer s.storageUsageMu.Unlock()
	return s.storageUsageRunning
}

type storageUsageTarget struct {
	MailboxID string
	Email     string
	Domain    string
	Home      string
	VolumeID  string
	Volume    string
}

func (s *Server) scanStorageUsage(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT m.id::text, m.email::text, m.domain::text,
		       COALESCE(NULLIF(m.mail_home, ''), '/var/vmail/' || m.domain || '/' || m.local_part) AS home,
		       COALESCE(v.id::text, ''), COALESCE(v.name, '')
		FROM public.mailboxes m
		LEFT JOIN control.storage_volumes v ON v.id = COALESCE(
		  m.storage_volume_id,
		  (SELECT mounted.id FROM control.storage_volumes mounted
		   WHERE m.mail_home IS NOT NULL AND m.mail_home <> ''
		     AND (m.mail_home = mounted.mount_path OR m.mail_home LIKE mounted.mount_path || '/%')
		   ORDER BY length(mounted.mount_path) DESC LIMIT 1),
		  (SELECT id FROM control.storage_volumes WHERE mount_path = $1 LIMIT 1)
		)
		WHERE m.active = true
		ORDER BY m.email
	`, s.cfg.VmailRoot)
	if err != nil {
		return 0, err
	}
	targets := []storageUsageTarget{}
	for rows.Next() {
		var target storageUsageTarget
		if rows.Scan(&target.MailboxID, &target.Email, &target.Domain, &target.Home,
			&target.VolumeID, &target.Volume) == nil && target.VolumeID != "" {
			targets = append(targets, target)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	scanned := 0
	for _, target := range targets {
		bytes, messages, measureErr := s.measureMailboxUsage(ctx, target.Home)
		if measureErr != nil {
			s.logger.Warn("storage usage measurement failed", "mailbox", target.Email, "err", measureErr)
			continue
		}
		if _, err := s.db.Exec(ctx, `
			INSERT INTO control.storage_usage
			  (volume_id, mailbox_id, email, domain, bytes, messages, scanned_at)
			VALUES ($1, $2, $3, $4, $5, $6, now())
			ON CONFLICT (volume_id, mailbox_id) DO UPDATE SET
			  email = EXCLUDED.email, domain = EXCLUDED.domain, bytes = EXCLUDED.bytes,
			  messages = EXCLUDED.messages, scanned_at = now()
		`, target.VolumeID, target.MailboxID, target.Email, target.Domain, bytes, messages); err != nil {
			s.logger.Warn("storage usage write failed", "mailbox", target.Email, "err", err)
			continue
		}
		scanned++
	}
	_, _ = s.db.Exec(ctx, `
		DELETE FROM control.storage_usage u
		WHERE NOT EXISTS (SELECT 1 FROM public.mailboxes m WHERE m.id = u.mailbox_id)
	`)
	s.logger.Info("storage usage scan complete", "mailboxes", scanned)
	return scanned, nil
}

func (s *Server) measureMailboxUsage(ctx context.Context, home string) (int64, int64, error) {
	clean := filepath.Clean(strings.TrimSpace(home))
	if clean == "" || !filepath.IsAbs(clean) {
		return 0, 0, fmt.Errorf("mailbox home is not absolute")
	}
	duName := "du"
	duArgs := []string{"-sb", "--one-file-system", clean}
	if _, err := storageLookPath("nice"); err == nil {
		duName = "nice"
		duArgs = []string{"-n", "19", "du", "-sb", "--one-file-system", clean}
		if _, err := storageLookPath("ionice"); err == nil {
			duArgs = []string{"-n", "19", "ionice", "-c", "2", "-n", "7", "du", "-sb", "--one-file-system", clean}
		}
	}
	result, err := storageUsageCommand(ctx, 20*time.Minute, duName, duArgs...)
	if err != nil {
		return 0, 0, fmt.Errorf("du failed: %s", truncateText(strings.TrimSpace(result.Stderr+" "+result.Stdout), 300))
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) == 0 {
		return 0, 0, fmt.Errorf("du returned no size")
	}
	var bytes int64
	if _, err := fmt.Sscanf(fields[0], "%d", &bytes); err != nil || bytes < 0 {
		return 0, 0, fmt.Errorf("could not parse du output")
	}
	messages := int64(0)
	for _, folder := range []string{filepath.Join(clean, "Maildir", "new"), filepath.Join(clean, "Maildir", "cur")} {
		findResult, findErr := storageUsageCommand(ctx, 10*time.Minute, "find", folder, "-type", "f")
		if findErr != nil {
			continue
		}
		messages += int64(len(strings.Split(strings.TrimSpace(findResult.Stdout), "\n")))
		if strings.TrimSpace(findResult.Stdout) == "" {
			messages--
		}
	}
	if messages < 0 {
		messages = 0
	}
	return bytes, messages, nil
}

func (s *Server) storageUsagePayload(ctx context.Context) (storageUsagePayload, error) {
	payload := storageUsagePayload{
		Usage: []storageUsageRecord{}, Domains: []storageDomainUsage{},
		Scanning: s.storageUsageScanning(),
	}
	rows, err := s.db.Query(ctx, `
		SELECT u.volume_id::text, v.name, u.mailbox_id::text, u.email, u.domain,
		       u.bytes, u.messages, u.scanned_at
		FROM control.storage_usage u
		JOIN control.storage_volumes v ON v.id = u.volume_id
		ORDER BY u.bytes DESC
		LIMIT 100
	`)
	if err != nil {
		return payload, err
	}
	for rows.Next() {
		var item storageUsageRecord
		if rows.Scan(&item.VolumeID, &item.VolumeName, &item.MailboxID, &item.Email,
			&item.Domain, &item.Bytes, &item.Messages, &item.ScannedAt) == nil {
			payload.Usage = append(payload.Usage, item)
			if payload.ScannedAt == nil || item.ScannedAt.After(*payload.ScannedAt) {
				at := item.ScannedAt
				payload.ScannedAt = &at
			}
		}
	}
	rows.Close()
	domainRows, err := s.db.Query(ctx, `
		SELECT u.domain, u.volume_id::text, v.name, sum(u.bytes), count(*)
		FROM control.storage_usage u
		JOIN control.storage_volumes v ON v.id = u.volume_id
		GROUP BY u.domain, u.volume_id, v.name
		ORDER BY sum(u.bytes) DESC
		LIMIT 50
	`)
	if err == nil {
		for domainRows.Next() {
			var item storageDomainUsage
			if domainRows.Scan(&item.Domain, &item.VolumeID, &item.VolumeName, &item.Bytes, &item.Mailboxes) == nil {
				payload.Domains = append(payload.Domains, item)
			}
		}
		domainRows.Close()
	}
	payload.Recommendation = s.storageRecommendation(ctx)
	return payload, nil
}

func (s *Server) storageRecommendation(ctx context.Context) *storageRecommendation {
	pol, err := s.storagePolicyRow(ctx)
	if err != nil {
		return nil
	}
	volumes, err := s.storageVolumeRows(ctx)
	if err != nil {
		return nil
	}
	byID := map[string]storageVolume{}
	for _, volume := range volumes {
		if volume.State == "retired" || volume.State == "wiped" || volume.CapacityState == "offline" {
			continue
		}
		byID[volume.ID] = volume
	}
	var source storageVolume
	foundSource := false
	for _, volume := range byID {
		if volume.CapacityTotalBytes == 0 {
			continue
		}
		if !foundSource || volume.CapacityPercent > source.CapacityPercent {
			source, foundSource = volume, true
		}
	}
	if !foundSource {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT mailbox_id::text, email, domain, bytes
		FROM control.storage_usage WHERE volume_id = $1
		ORDER BY bytes DESC LIMIT 20
	`, source.ID)
	if err != nil {
		return nil
	}
	type mailboxUse struct {
		mailboxID string
		email     string
		domain    string
		bytes     int64
	}
	mailboxes := []mailboxUse{}
	for rows.Next() {
		var item mailboxUse
		if rows.Scan(&item.mailboxID, &item.email, &item.domain, &item.bytes) == nil && item.bytes > 0 {
			mailboxes = append(mailboxes, item)
		}
	}
	rows.Close()
	if len(mailboxes) == 0 {
		return nil
	}
	var target storageVolume
	foundTarget := false
	for _, volume := range byID {
		if volume.ID == source.ID || volume.CapacityTotalBytes == 0 || volume.CapacityState == "offline" {
			continue
		}
		if !foundTarget || volume.CapacityFreeBytes > target.CapacityFreeBytes {
			target, foundTarget = volume, true
		}
	}
	if !foundTarget {
		return nil
	}
	for _, mailbox := range mailboxes {
		reserve := storageReserveBytes(pol, target.CapacityTotalBytes)
		if uint64(mailbox.bytes)+reserve > target.CapacityFreeBytes {
			continue
		}
		sourceAfter := percentAfter(source.CapacityUsedBytes, source.CapacityTotalBytes, -mailbox.bytes)
		targetAfter := percentAfter(target.CapacityUsedBytes, target.CapacityTotalBytes, mailbox.bytes)
		seconds := mailbox.bytes / (100 * 1024 * 1024)
		message := fmt.Sprintf("Move %s (%s) from %s to %s: %s → %s, %s → %s. Source retained for the rollback window.",
			mailbox.email, humanBytes(uint64(mailbox.bytes)),
			source.Name, target.Name,
			humanPercent(source.CapacityPercent), humanPercent(sourceAfter),
			humanPercent(target.CapacityPercent), humanPercent(targetAfter))
		return &storageRecommendation{
			SourceVolumeID: source.ID, SourceVolumeName: source.Name,
			TargetVolumeID: target.ID, TargetVolumeName: target.Name,
			Scope: "mailbox", Domain: mailbox.domain, MailboxID: mailbox.mailboxID, Email: mailbox.email,
			Bytes: mailbox.bytes, Mailboxes: 1,
			SourceBeforePercent: source.CapacityPercent, SourceAfterPercent: sourceAfter,
			TargetBeforePercent: target.CapacityPercent, TargetAfterPercent: targetAfter,
			EstimatedSeconds: seconds, RetentionBytes: mailbox.bytes, Message: message,
		}
	}
	return nil
}

func percentAfter(used uint64, total uint64, delta int64) float64 {
	if total == 0 {
		return 0
	}
	next := int64(used) + delta
	if next < 0 {
		next = 0
	}
	return float64(next) / float64(total) * 100
}

func humanPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func (s *Server) handleStorageUsage(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	payload, err := s.storageUsagePayload(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleStorageUsageScan(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	if !s.beginStorageUsageScan() {
		writeError(w, http.StatusConflict, "SCAN", "A storage usage scan is already running")
		return
	}
	go func() {
		defer s.endStorageUsageScan()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		if _, err := s.scanStorageUsage(ctx); err != nil {
			s.logger.Warn("storage usage scan failed", "err", err)
			s.notifyEvent(context.Background(), "storage.usage_scan_failed", map[string]any{"error": err.Error()})
		}
	}()
	s.actorAudit(r, sess, "storage.usage_scan", "global", "Started a mailbox usage scan", nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "Usage scan started. Sizes appear as each mailbox completes."})
}

func (s *Server) handleStorageAlertAcknowledge(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var volume, level string
	if err := s.db.QueryRow(r.Context(), `
		UPDATE control.storage_alerts a
		SET status = 'acknowledged', acknowledged_by = NULLIF($2, '')::uuid, acknowledged_at = now(), last_seen_at = now()
		FROM control.storage_volumes v
		WHERE a.id = $1 AND v.id = a.volume_id
		RETURNING v.name, a.level
	`, id, sess.User.ID).Scan(&volume, &level); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "storage.alert_acknowledged", volume, "Acknowledged a storage capacity alert", map[string]any{"level": level})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) runStorageUsageScheduler(ctx context.Context) {
	check := func() {
		var last *time.Time
		_ = s.db.QueryRow(ctx, `SELECT max(scanned_at) FROM control.storage_usage`).Scan(&last)
		hour := s.settingInt(ctx, "storage.usage_scan_hour", 2)
		if hour < 0 || hour > 23 {
			hour = 2
		}
		now := time.Now()
		if last != nil && now.Sub(*last) < 23*time.Hour {
			return
		}
		if last != nil && now.Hour() < hour {
			return
		}
		if !s.beginStorageUsageScan() {
			return
		}
		go func() {
			defer s.endStorageUsageScan()
			if _, err := s.scanStorageUsage(context.Background()); err != nil {
				s.logger.Warn("scheduled storage usage scan failed", "err", err)
				s.notifyEvent(context.Background(), "storage.usage_scan_failed", map[string]any{"error": err.Error()})
			}
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
			check()
		}
	}()
	ticker := time.NewTicker(time.Hour)
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
