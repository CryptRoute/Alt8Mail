package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// validStoredAttachmentName keeps cleanup code inside the upload directory.
// Local attachment objects are always allocated as att_<random> by newID.
func validStoredAttachmentName(name string) bool {
	return strings.HasPrefix(name, "att_") &&
		name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) &&
		filepath.Base(name) == name
}

// removeUncommittedAttachments deletes files that were written for records
// created during a withMailState call that did not commit. The database is
// checked first so an ambiguous commit or an existing attachment with the same
// generated id can never turn a failed write into data loss.
func (s *Server) removeUncommittedAttachments(state map[string]any, before map[string]map[string]any) {
	if s == nil || s.store == nil || s.store.DB == nil || strings.TrimSpace(s.cfg.UploadDir) == "" {
		return
	}
	known, _ := state["attachments"].(map[string]any)
	if len(known) == 0 {
		return
	}
	ids := make([]string, 0, len(known))
	names := make([]string, 0, len(known))
	for id, raw := range known {
		if _, existed := before[id]; existed {
			continue
		}
		record, _ := raw.(map[string]any)
		if record == nil {
			continue
		}
		storedName := stateString(record, "storedName")
		if storedName == "" {
			storedName = stateString(record, "storedPath")
		}
		if !validStoredAttachmentName(storedName) {
			continue
		}
		ids = append(ids, id)
		names = append(names, storedName)
	}
	if len(ids) == 0 {
		return
	}

	queryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.store.DB.Query(queryCtx, `SELECT id FROM attachments WHERE id = ANY($1::text[])`, ids)
	if err != nil {
		s.logger.Warn("could not verify uncommitted attachments; leaving files in place", "err", err)
		return
	}
	referenced := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			referenced[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Warn("could not verify uncommitted attachments; leaving files in place", "err", err)
		return
	}
	rows.Close()

	removed, freed := 0, int64(0)
	for i, id := range ids {
		if referenced[id] {
			continue
		}
		path := filepath.Join(s.cfg.UploadDir, names[i])
		info, statErr := os.Stat(path)
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				s.logger.Warn("could not inspect uncommitted attachment", "file", names[i], "err", statErr)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				s.logger.Warn("could not remove uncommitted attachment", "file", names[i], "err", err)
			}
			continue
		}
		removed++
		freed += info.Size()
	}
	if removed > 0 {
		s.logger.Info("removed uncommitted attachment files", "files", removed, "bytes", freed)
	}
}

// orphanAttachmentCandidate is deliberately conservative: only regular local
// objects with the att_ prefix and an age beyond the grace period are removed.
func orphanAttachmentCandidate(name string, modTime time.Time, regular bool, cutoff time.Time, referenced map[string]bool) bool {
	return regular &&
		validStoredAttachmentName(name) &&
		modTime.Before(cutoff) &&
		!referenced[name]
}

// sweepOrphanAttachments removes local attachment files that are not referenced
// by either the mail attachment table or the workspace files table. It runs on
// a schedule so a crash between writing a file and committing its row cannot
// permanently consume disk.
func (s *Server) sweepOrphanAttachments(ctx context.Context) {
	if s == nil || s.store == nil || s.store.DB == nil || strings.TrimSpace(s.cfg.UploadDir) == "" {
		return
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT stored_path FROM attachments WHERE stored_path <> ''
		UNION
		SELECT storage_key FROM files WHERE provider_id='a8m' AND storage_key <> ''
	`)
	if err != nil {
		s.logger.Warn("orphan attachment sweep skipped: cannot read references", "err", err)
		return
	}
	referenced := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name != "" {
			referenced[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		s.logger.Warn("orphan attachment sweep skipped: reference query failed", "err", err)
		return
	}
	rows.Close()

	grace := s.cfg.OrphanGracePeriod
	if grace <= 0 {
		grace = 5 * time.Minute
	}
	cutoff := time.Now().Add(-grace)
	entries, err := os.ReadDir(s.cfg.UploadDir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.Warn("orphan attachment sweep failed to read upload directory", "err", err)
		}
		return
	}
	removed, freed := 0, int64(0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !orphanAttachmentCandidate(entry.Name(), info.ModTime(), info.Mode().IsRegular(), cutoff, referenced) {
			continue
		}
		path := filepath.Join(s.cfg.UploadDir, entry.Name())
		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				s.logger.Warn("could not remove orphan attachment", "file", entry.Name(), "err", err)
			}
			continue
		}
		removed++
		freed += info.Size()
	}
	if removed > 0 {
		s.logger.Info("orphan attachment sweep removed files", "files", removed, "bytes", freed, "grace", grace.String())
	}
}

func (s *Server) orphanAttachmentSweepWorker(ctx context.Context) {
	// Let the first mail sync finish before the first sweep. The grace period
	// protects files being written by that sync; the delay avoids a startup race.
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	s.sweepOrphanAttachments(ctx)
	interval := s.cfg.OrphanSweepInterval
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOrphanAttachments(ctx)
		}
	}
}
