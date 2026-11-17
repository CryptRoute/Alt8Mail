package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var errStorageMoveCanceled = errors.New("storage move canceled")

type mailHomeStat struct {
	Files    int64
	Messages int64
	Bytes    int64
}

func mailHomeStats(root string) (mailHomeStat, error) {
	var stats mailHomeStat
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("mail home contains a symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		stats.Files++
		stats.Bytes += info.Size()
		parent := filepath.Base(filepath.Dir(path))
		if parent == "cur" || parent == "new" {
			stats.Messages++
		}
		return nil
	})
	return stats, err
}

func humanBytes(value uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	number := float64(value)
	unit := 0
	for number >= 1024 && unit < len(units)-1 {
		number /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", value)
	}
	return fmt.Sprintf("%.1f %s", number, units[unit])
}

func (s *Server) RunStorageOperation(ctx context.Context, operationID string) error {
	op, err := s.loadStorageOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.Status != "queued" && op.Status != "running" {
		return nil
	}
	if strings.HasPrefix(op.Kind, "volume_") {
		return s.RunStorageVolumeOperation(ctx, op)
	}
	if op.Kind == "swap_create" {
		return s.RunStorageSwapOperation(ctx, op)
	}
	if op.Kind != "move_mailboxes" {
		return fmt.Errorf("unsupported storage operation %q", op.Kind)
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'running', step = 'Preparing move', progress = 5,
		    started_at = COALESCE(started_at, now()), updated_at = now(), error = ''
		WHERE id = $1 AND status IN ('queued', 'running')
	`, op.ID); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "Storage runner started.")
	var movePlan storageMovePlan
	_ = json.Unmarshal(op.Plan, &movePlan)
	if movePlan.Scope == "domain" && movePlan.Domain != "" && movePlan.TargetVolumeID != "" {
		if _, err := s.db.Exec(ctx, `
			UPDATE public.mail_domains SET storage_volume_id = $2::uuid WHERE domain = $1
		`, movePlan.Domain, movePlan.TargetVolumeID); err != nil {
			s.failStorageOperation(ctx, op.ID, "Could not set the domain storage default: "+err.Error())
			return err
		}
		s.appendStorageLog(ctx, op.ID, "Set the domain default for "+movePlan.Domain+" to "+movePlan.TargetVolumeName+".")
	}
	items, err := s.loadStorageMoveItems(ctx, op.ID)
	if err != nil {
		s.failStorageOperation(ctx, op.ID, "Could not load move items: "+err.Error())
		return err
	}
	verifyMode := movePlan.Verify
	if verifyMode != "thorough" {
		verifyMode = "standard"
	}
	var doneBytes int64
	for index, item := range items {
		op, _ = s.loadStorageOperation(ctx, op.ID)
		if op.CancelRequested && item.Status == "pending" {
			if !s.operationHasSwitchedItems(ctx, op.ID) {
				s.revertDomainMoveDefault(ctx, movePlan)
				if _, err := s.db.Exec(ctx, `
					UPDATE control.storage_operations
					SET status = 'canceled', step = 'Canceled before switching', finished_at = now(), updated_at = now()
					WHERE id = $1
				`, op.ID); err != nil {
					return err
				}
				s.appendStorageLog(ctx, op.ID, "Canceled before any mailbox switched.")
				return errStorageMoveCanceled
			}
			_, _ = s.db.Exec(ctx, `
				UPDATE control.storage_operations
				SET status = 'failed', step = 'Cancellation requested after partial progress',
				    error = 'One or more mailboxes already switched. Roll back to restore the source.',
				    finished_at = now(), updated_at = now()
				WHERE id = $1
			`, op.ID)
			return nil
		}
		if item.Status == "grace" || item.Status == "finalized" || item.Status == "rolled_back" || item.Status == "canceled" {
			doneBytes += item.Bytes
			continue
		}
		progress := 5 + int(float64(index)/float64(maxInt(1, len(items)))*85)
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_operations
			SET step = $2, progress = $3, updated_at = now()
			WHERE id = $1
		`, op.ID, "Moving "+item.Email, progress)
		if err := s.runMoveItem(ctx, op, item, verifyMode); err != nil {
			if errors.Is(err, errStorageMoveCanceled) {
				if s.operationHasSwitchedItems(ctx, op.ID) {
					_, _ = s.db.Exec(ctx, `
						UPDATE control.storage_operations
						SET status = 'failed', step = 'Cancellation requested after partial progress',
						    error = 'One or more mailboxes already switched. Roll back to restore the source.',
						    finished_at = now(), updated_at = now()
						WHERE id = $1
					`, op.ID)
				} else {
					s.revertDomainMoveDefault(ctx, movePlan)
					_, _ = s.db.Exec(ctx, `
						UPDATE control.storage_operations
						SET status = 'canceled', step = 'Canceled', finished_at = now(), updated_at = now()
						WHERE id = $1
					`, op.ID)
				}
				return nil
			}
			s.failStorageOperation(ctx, op.ID, item.Email+": "+err.Error())
			_, _ = s.db.Exec(ctx, `
				UPDATE control.storage_move_items
				SET status = 'failed', error = $2, finished_at = now(), updated_at = now()
				WHERE id = $1 AND status NOT IN ('grace', 'finalized', 'rolled_back')
			`, item.ID, truncateText(err.Error(), 500))
			s.auditStorageOperation(ctx, op, "storage.move_failed", "Mailbox move failed", map[string]any{"email": item.Email, "error": truncateText(err.Error(), 500)})
			return err
		}
		doneBytes += item.Bytes
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_operations
			SET moved_bytes = $2, progress = $3, updated_at = now()
			WHERE id = $1
		`, op.ID, doneBytes, progress+5)
	}
	op, err = s.loadStorageOperation(ctx, op.ID)
	if err != nil {
		return err
	}
	if op.CancelRequested {
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_operations
			SET status = 'failed', step = 'Cancellation requested after switching; roll back to restore the source',
			    error = 'Cancellation requested after one or more mailboxes switched. Use rollback before finalization.',
			    finished_at = now(), updated_at = now()
			WHERE id = $1
		`, op.ID)
		s.appendStorageLog(ctx, op.ID, "Cancellation requested after a switch; completed items are in the grace window and can be rolled back.")
		return nil
	}
	var plan storageMovePlan
	_ = json.Unmarshal(op.Plan, &plan)
	finalizeAt := time.Now().UTC().Add(time.Duration(maxInt(1, plan.GraceHours)) * time.Hour)
	if _, err := s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'grace', step = 'Verified; source retained for rollback', progress = 100,
		    finalize_at = $2, updated_at = now()
		WHERE id = $1
	`, op.ID, finalizeAt); err != nil {
		return err
	}
	s.appendStorageLog(ctx, op.ID, "All selected mailboxes verified. Source copies retained until finalization.")
	s.auditStorageOperation(ctx, op, "storage.move_verified", "Mailbox move verified; source retained for the grace window", map[string]any{"mailboxes": len(items), "finalizeAt": finalizeAt.Format(time.RFC3339)})
	return nil
}

func (s *Server) runMoveItem(ctx context.Context, op storageOperation, item storageMoveItem, verifyMode string) error {
	if item.SourceHome == "" || item.TargetHome == "" {
		return fmt.Errorf("move item has no source or target home")
	}
	if !pathWithin(item.SourceVolumeRoot, item.SourceHome) || sameStoragePath(item.SourceVolumeRoot, item.SourceHome) {
		return fmt.Errorf("source home is outside its registered volume")
	}
	if !pathWithin(item.TargetVolumeRoot, item.TargetHome) || sameStoragePath(item.TargetVolumeRoot, item.TargetHome) {
		return fmt.Errorf("target home is outside its registered volume")
	}
	if sameStoragePath(item.SourceHome, item.TargetHome) {
		return fmt.Errorf("source and target are the same path")
	}
	if _, err := os.Lstat(item.SourceHome); err != nil {
		return fmt.Errorf("source home is not readable: %w", err)
	}
	if _, err := s.ensureMailHome(item.TargetVolumeRoot, item.TargetHome); err != nil {
		return fmt.Errorf("prepare target home: %w", err)
	}
	mailboxIsTarget, err := s.mailboxPointsToTarget(ctx, item)
	if err != nil {
		return err
	}
	if !mailboxIsTarget {
		_, _ = s.db.Exec(ctx, `UPDATE control.storage_move_items SET status = 'syncing', started_at = COALESCE(started_at, now()), updated_at = now() WHERE id = $1`, item.ID)
		if err := s.syncMailHome(ctx, item.SourceHome, item.TargetHome, "initial"); err != nil {
			return fmt.Errorf("initial sync: %w", err)
		}
		opNow, _ := s.loadStorageOperation(ctx, op.ID)
		if opNow.CancelRequested {
			_ = os.RemoveAll(item.TargetHome)
			return errStorageMoveCanceled
		}
		var moved int64
		if err := s.db.QueryRow(ctx, `
			UPDATE public.mailboxes SET storage_state = 'moving'
			WHERE id = $1 AND storage_state NOT IN ('moving', 'switching')
			RETURNING 1
		`, item.MailboxID).Scan(&moved); err != nil {
			return fmt.Errorf("mark mailbox moving: %w", err)
		}
		s.appendStorageLog(ctx, op.ID, "Quiescing "+item.Email+".")
		_, _ = runCommand(ctx, 20*time.Second, "doveadm", "kick", item.Email)
		_, _ = runCommand(ctx, 20*time.Second, "doveadm", "auth", "cache", "flush")
		if err := s.syncMailHome(ctx, item.SourceHome, item.TargetHome, "authoritative"); err != nil {
			return fmt.Errorf("authoritative sync: %w", err)
		}
		targetStoredHome := item.TargetHome
		targetVolumeID := item.TargetVolumeID
		if item.TargetLegacy {
			targetStoredHome = ""
			targetVolumeID = ""
		}
		var switched int
		err := s.db.QueryRow(ctx, `
			UPDATE public.mailboxes
			SET mail_home = NULLIF($2, ''),
			    maildir_path = $3,
			    storage_volume_id = NULLIF($4, '')::uuid,
			    storage_state = 'switching'
			WHERE id = $1
			  AND mail_home IS NOT DISTINCT FROM NULLIF($5, '')
			  AND COALESCE(storage_volume_id::text, '') = $6
			RETURNING 1
		`, item.MailboxID, targetStoredHome, filepath.Join(item.TargetHome, "Maildir"),
			targetVolumeID, item.SourceMailHome, item.SourceVolumeID).Scan(&switched)
		if err != nil {
			return fmt.Errorf("switch mailbox home: %w", err)
		}
		_, _ = s.db.Exec(ctx, `UPDATE control.storage_move_items SET status = 'switching', updated_at = now() WHERE id = $1`, item.ID)
		s.appendStorageLog(ctx, op.ID, "Switched "+item.Email+" to "+item.TargetVolumeName+".")
		// New logins and deliveries now resolve the target. Copy anything that
		// reached the old home during the switch, without deleting target data.
		if err := s.syncPostSwitchDelta(ctx, item); err != nil {
			return err
		}
	} else if item.Status == "switching" || item.Status == "verifying" || item.Status == "failed" {
		// A previous runner may have switched the database and stopped before
		// copying the last files that reached the old home.
		if err := s.syncPostSwitchDelta(ctx, item); err != nil {
			return err
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE control.storage_move_items SET status = 'verifying', updated_at = now() WHERE id = $1`, item.ID)
	if err := s.verifyMailHome(ctx, item, verifyMode); err != nil {
		if rollbackErr := s.rollbackMoveItem(ctx, item); rollbackErr != nil {
			return fmt.Errorf("verification failed (%v) and rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("verification failed: %w", err)
	}
	stats, _ := mailHomeStats(item.TargetHome)
	_, _ = s.db.Exec(ctx, `
		UPDATE public.mailboxes SET storage_state = 'ready' WHERE id = $1
	`, item.MailboxID)
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_move_items
		SET status = 'grace', bytes = $2, messages = $3, finished_at = now(), updated_at = now()
		WHERE id = $1
	`, item.ID, stats.Bytes, stats.Messages)
	s.appendStorageLog(ctx, op.ID, "Verified "+item.Email+" ("+humanBytes(uint64(stats.Bytes))+").")
	return nil
}

// syncPostSwitchDelta copies old-home files that arrived after the database
// switch and waits until the source has been quiet for two checks. It never
// deletes target data, so a delivery that landed on the new home is safe.
func (s *Server) syncPostSwitchDelta(ctx context.Context, item storageMoveItem) error {
	last := ""
	stable := 0
	for attempt := 0; attempt < 30; attempt++ {
		if err := s.syncMailHome(ctx, item.SourceHome, item.TargetHome, "delta"); err != nil {
			return fmt.Errorf("post-switch delta: %w", err)
		}
		stats, err := mailHomeStats(item.SourceHome)
		if err != nil {
			return fmt.Errorf("measure source after switch: %w", err)
		}
		signature := fmt.Sprintf("%d/%d", stats.Files, stats.Bytes)
		if signature == last {
			stable++
		} else {
			stable = 0
			last = signature
		}
		if stable >= 2 {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("source did not become quiet after the switch")
}

func (s *Server) mailboxPointsToTarget(ctx context.Context, item storageMoveItem) (bool, error) {
	var mailHome, volumeID string
	if err := s.db.QueryRow(ctx, `
		SELECT COALESCE(mail_home, ''), COALESCE(storage_volume_id::text, '')
		FROM public.mailboxes WHERE id = $1
	`, item.MailboxID).Scan(&mailHome, &volumeID); err != nil {
		return false, err
	}
	targetHome := item.TargetHome
	if item.TargetLegacy {
		targetHome = ""
	}
	targetVolume := item.TargetVolumeID
	if item.TargetLegacy {
		targetVolume = ""
	}
	return strings.TrimSpace(mailHome) == strings.TrimSpace(targetHome) && strings.TrimSpace(volumeID) == strings.TrimSpace(targetVolume), nil
}

func (s *Server) syncMailHome(ctx context.Context, source, target, mode string) error {
	if rsyncAvailable() {
		args := []string{"-aHAX", "--numeric-ids", "--info=progress2"}
		switch mode {
		case "authoritative":
			args = append(args, "--delete")
		case "delta":
			args = append(args, "--ignore-existing")
		}
		args = append(args, strings.TrimRight(source, string(os.PathSeparator))+string(os.PathSeparator),
			strings.TrimRight(target, string(os.PathSeparator))+string(os.PathSeparator))
		result, err := runCommand(ctx, 6*time.Hour, "rsync", args...)
		if err != nil {
			return fmt.Errorf("%s", strings.TrimSpace(result.Stderr+" "+result.Stdout))
		}
		return nil
	}
	return copyMailHomeFallback(source, target, mode)
}

func rsyncAvailable() bool {
	_, err := exec.LookPath("rsync")
	return err == nil
}

func copyMailHomeFallback(source, target, mode string) error {
	if mode == "authoritative" {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return err
	}
	uid, gid, ownerErr := mailOwnerLookup()
	if ownerErr != nil {
		uid, gid = -1, -1
	}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("mail home contains a symlink: %s", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			if err := os.MkdirAll(destination, 0o750); err != nil {
				return err
			}
			if uid >= 0 {
				_ = os.Chown(destination, uid, gid)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if mode == "delta" {
			if existing, err := os.Stat(destination); err == nil && existing.Mode().IsRegular() {
				return nil
			}
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(output, input); err != nil {
			output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
		_ = os.Chtimes(destination, info.ModTime(), info.ModTime())
		if uid >= 0 {
			_ = os.Chown(destination, uid, gid)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if uid >= 0 {
		return chownTree(target, uid, gid)
	}
	return nil
}

func (s *Server) verifyMailHome(ctx context.Context, item storageMoveItem, verifyMode string) error {
	if rsyncAvailable() {
		args := []string{"-aHAX", "--numeric-ids", "--dry-run", "--itemize-changes",
			"--exclude=dovecot.index*", "--exclude=dovecot-uidlist",
			"--exclude=dovecot.list.index*", "--exclude=maildirsize"}
		if verifyMode == "thorough" {
			args = append(args, "--checksum")
		}
		args = append(args,
			strings.TrimRight(item.SourceHome, string(os.PathSeparator))+string(os.PathSeparator),
			strings.TrimRight(item.TargetHome, string(os.PathSeparator))+string(os.PathSeparator))
		result, err := runCommand(ctx, 30*time.Minute, "rsync", args...)
		if err != nil {
			return fmt.Errorf("verification rsync failed: %s", strings.TrimSpace(result.Stderr))
		}
		if strings.TrimSpace(result.Stdout) != "" {
			return fmt.Errorf("verification found differing files")
		}
	} else if err := sourceHomePresentInTarget(item.SourceHome, item.TargetHome); err != nil {
		return err
	}
	return nil
}

// sourceHomePresentInTarget proves every source file is present on the target.
// It deliberately permits target-only files: a new delivery that arrives after
// the database switch is valid and must not fail verification or trigger a
// rollback.
func sourceHomePresentInTarget(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, "dovecot.index") || name == "dovecot-uidlist" || name == "maildirsize" {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		sourceInfo, err := entry.Info()
		if err != nil {
			return err
		}
		targetInfo, err := os.Stat(filepath.Join(target, relative))
		if err != nil || targetInfo.Size() != sourceInfo.Size() {
			return fmt.Errorf("source file is missing from the target copy: %s", relative)
		}
		return nil
	})
}

func mailMessageStats(root string) (int64, int64, error) {
	var files, bytes int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if parent != "cur" && parent != "new" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

func (s *Server) operationHasSwitchedItems(ctx context.Context, operationID string) bool {
	var count int
	if err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM control.storage_move_items
		WHERE operation_id = $1 AND status IN ('switching', 'verifying', 'grace', 'finalized')
	`, operationID).Scan(&count); err != nil {
		return true
	}
	return count > 0
}

func (s *Server) revertDomainMoveDefault(ctx context.Context, plan storageMovePlan) {
	if plan.Scope != "domain" || plan.Domain == "" || plan.TargetVolumeID == "" {
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE public.mail_domains
		SET storage_volume_id = NULLIF($3, '')::uuid
		WHERE domain = $1 AND COALESCE(storage_volume_id::text, '') = $2
	`, plan.Domain, plan.TargetVolumeID, plan.PreviousDomainVolumeID)
}

func (s *Server) rollbackMoveItem(ctx context.Context, item storageMoveItem) error {
	pointsTarget, err := s.mailboxPointsToTarget(ctx, item)
	if err != nil {
		return err
	}
	if pointsTarget {
		if err := s.syncMailHome(ctx, item.TargetHome, item.SourceHome, "delta"); err != nil {
			return fmt.Errorf("reverse sync: %w", err)
		}
		if _, err := s.db.Exec(ctx, `
			UPDATE public.mailboxes
			SET mail_home = NULLIF($2, ''),
			    maildir_path = $3,
			    storage_volume_id = NULLIF($4, '')::uuid,
			    storage_state = 'ready'
			WHERE id = $1
		`, item.MailboxID, item.SourceMailHome, filepath.Join(item.SourceHome, "Maildir"),
			item.SourceVolumeID); err != nil {
			return err
		}
	}
	// The source is authoritative again and any target-only files have been
	// copied back. Remove the disposable target copy so a retry can start from
	// a clean target and cannot confuse a later move.
	if item.TargetHome != "" && pathWithin(item.TargetVolumeRoot, item.TargetHome) &&
		!sameStoragePath(item.TargetVolumeRoot, item.TargetHome) && !sameStoragePath(item.SourceHome, item.TargetHome) {
		if err := os.RemoveAll(item.TargetHome); err != nil {
			return fmt.Errorf("remove target copy: %w", err)
		}
		_ = os.Remove(filepath.Dir(item.TargetHome))
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_move_items SET status = 'rolled_back', finished_at = now(), updated_at = now()
		WHERE id = $1
	`, item.ID)
	return nil
}

func (s *Server) FinalizeStorageOperation(ctx context.Context, operationID string, manual bool) error {
	op, err := s.loadStorageOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.Status != "grace" {
		return fmt.Errorf("operation is not in its grace window")
	}
	if !manual && op.FinalizeAt != nil && time.Now().Before(*op.FinalizeAt) {
		return nil
	}
	items, err := s.loadStorageMoveItems(ctx, op.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status != "grace" && item.Status != "finalized" {
			return fmt.Errorf("%s is not in the grace window", item.Email)
		}
	}
	for _, item := range items {
		if item.Status != "grace" {
			continue
		}
		pointsTarget, err := s.mailboxPointsToTarget(ctx, item)
		if err != nil {
			return err
		}
		if !pointsTarget {
			return fmt.Errorf("%s no longer points at the target; refusing to delete the source", item.Email)
		}
		if !pathWithin(item.SourceVolumeRoot, item.SourceHome) || sameStoragePath(item.SourceVolumeRoot, item.SourceHome) {
			return fmt.Errorf("source home for %s is outside its registered volume", item.Email)
		}
		if sameStoragePath(item.SourceHome, item.TargetHome) {
			return fmt.Errorf("source and target are the same for %s", item.Email)
		}
		_, _ = runCommand(ctx, 20*time.Second, "doveadm", "kick", item.Email)
		// A late delivery can still land in the retained source through a stale
		// auth cache. Copy that final delta to the target before deleting it.
		if err := s.syncMailHome(ctx, item.SourceHome, item.TargetHome, "delta"); err != nil {
			return fmt.Errorf("final delta for %s: %w", item.Email, err)
		}
		if err := sourceHomePresentInTarget(item.SourceHome, item.TargetHome); err != nil {
			return fmt.Errorf("final verification for %s: %w", item.Email, err)
		}
		if err := os.RemoveAll(item.SourceHome); err != nil {
			return fmt.Errorf("remove retained source for %s: %w", item.Email, err)
		}
		_ = os.Remove(filepath.Dir(item.SourceHome))
		_, _ = s.db.Exec(ctx, `
			UPDATE control.storage_move_items SET status = 'finalized', finished_at = now(), updated_at = now()
			WHERE id = $1
		`, item.ID)
		s.appendStorageLog(ctx, op.ID, "Finalized "+item.Email+"; retained source removed.")
	}
	_, err = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'succeeded', step = 'Completed; retained source removed', progress = 100,
		    finished_at = now(), updated_at = now(), finalize_at = now()
		WHERE id = $1
	`, op.ID)
	return err
}

func (s *Server) RollbackStorageOperation(ctx context.Context, operationID string) error {
	op, err := s.loadStorageOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.Status == "succeeded" || op.Status == "rolled_back" || op.Status == "canceled" {
		return fmt.Errorf("operation cannot be rolled back")
	}
	items, err := s.loadStorageMoveItems(ctx, op.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status == "finalized" {
			return fmt.Errorf("%s was already finalized; restore it from a backup", item.Email)
		}
		if item.Status == "rolled_back" || item.Status == "canceled" {
			continue
		}
		if err := s.rollbackMoveItem(ctx, item); err != nil {
			return fmt.Errorf("rollback %s: %w", item.Email, err)
		}
	}
	var plan storageMovePlan
	_ = json.Unmarshal(op.Plan, &plan)
	s.revertDomainMoveDefault(ctx, plan)
	_, err = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'rolled_back', step = 'Rolled back; source is authoritative', finished_at = now(), updated_at = now()
		WHERE id = $1
	`, op.ID)
	if err == nil {
		s.auditStorageOperation(ctx, op, "storage.move_rolled_back", "Rolled back a mailbox move", map[string]any{"mailboxes": len(items)})
	}
	return err
}

func (s *Server) failStorageOperation(ctx context.Context, id, message string) {
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'failed', step = 'Failed; source retained', error = $2,
		    finished_at = now(), updated_at = now()
		WHERE id = $1
	`, id, truncateText(message, 1000))
	s.appendStorageLog(ctx, id, "Failed: "+truncateText(message, 500))
}

func (s *Server) appendStorageLog(ctx context.Context, id, line string) {
	if s.db == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET log = right(log || $2 || E'\n', 60000), updated_at = now()
		WHERE id = $1
	`, id, line)
}

func (s *Server) runStorageFinalizer(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := s.db.Query(ctx, `
				SELECT id::text FROM control.storage_operations
				WHERE status = 'grace' AND finalize_at IS NOT NULL AND finalize_at <= now()
				ORDER BY finalize_at LIMIT 10
			`)
			if err != nil {
				continue
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			for _, id := range ids {
				if err := s.FinalizeStorageOperation(ctx, id, false); err != nil {
					s.logger.Debug("storage finalizer failed", "operation", id, "err", err)
				}
			}
		}
	}
}

func (s *Server) reconcileStorageOperations(ctx context.Context) {
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'failed', step = 'Interrupted',
		    error = 'The storage runner stopped before completion. Source mail was retained; review the operation and roll back if needed.',
		    finished_at = now(), updated_at = now()
		WHERE status = 'running' AND updated_at < now() - interval '15 minutes'
	`)
	_, _ = s.db.Exec(ctx, `
		UPDATE control.storage_operations
		SET status = 'failed', step = 'Runner did not start',
		    error = 'The storage runner did not start within ten minutes. Check a8m-storage-runner.service and the control environment.',
		    finished_at = now(), updated_at = now()
		WHERE status = 'queued' AND created_at < now() - interval '10 minutes'
	`)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
