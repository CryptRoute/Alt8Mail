package control

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var backupContentNames = map[string]string{
	"panel_database": "Panel and mail PostgreSQL database",
	"postfix":        "Postfix configuration",
	"dovecot":        "Dovecot configuration",
	"dkim":           "DKIM signing keys",
	"letsencrypt":    "Let's Encrypt certificates",
	"maildirs":       "Maildir message stores",
}

func (s *Server) registerBackupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/backups", s.require("backups.read", s.handleBackups))
	mux.HandleFunc("POST /control/api/backups", s.require("backups.manage", s.handleBackupCreate))
	mux.HandleFunc("GET /control/api/backups/{id}/download", s.require("backups.read", s.handleBackupDownload))
	mux.HandleFunc("DELETE /control/api/backups/{id}", s.require("backups.manage", s.handleBackupDelete))
	mux.HandleFunc("POST /control/api/backups/{id}/restore", s.require("backups.manage", s.handleBackupRestore))
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,name,path,size_bytes,contents,status,detail,created_at,finished_at
		FROM control.backups ORDER BY created_at DESC LIMIT 200
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, path, status, detail string
		var size int64
		var contents []string
		var created time.Time
		var finished *time.Time
		if rows.Scan(&id, &name, &path, &size, &contents, &status, &detail, &created, &finished) == nil {
			item := map[string]any{"id": id, "name": name, "path": path, "sizeBytes": size,
				"contents": contents, "status": status, "detail": detail,
				"createdAt": created.UTC().Format(time.RFC3339)}
			if finished != nil {
				item["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": items, "directory": s.cfg.BackupDir, "contentOptions": backupContentNames})
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name     string   `json:"name"`
		Contents []string `json:"contents"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if len(body.Contents) == 0 {
		body.Contents = []string{"panel_database", "postfix", "dovecot", "dkim", "letsencrypt"}
	}
	for _, content := range body.Contents {
		if _, ok := backupContentNames[content]; !ok {
			writeError(w, http.StatusBadRequest, "CONTENTS", "Unknown backup content: "+content)
			return
		}
	}
	if err := os.MkdirAll(s.cfg.BackupDir, 0o700); err != nil {
		writeFromError(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "alt8mail-backup-" + time.Now().UTC().Format("20060102-150405")
	}
	name = regexpSafeName(name)
	path := filepath.Join(s.cfg.BackupDir, name+".tar.gz")
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.backups (name,path,contents,status,created_by)
		VALUES ($1,$2,$3,'running',$4) RETURNING id::text
	`, name, path, body.Contents, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	go s.runBackup(id, path, body.Contents)
	s.actorAudit(r, sess, "backup.started", name, "Started a backup archive", map[string]any{"contents": body.Contents})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": id, "name": name, "path": path, "status": "running"})
}

func regexpSafeName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			b.WriteRune(char)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "alt8mail-backup"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func (s *Server) runBackup(id, path string, contents []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		s.finishBackup(id, "failed", err.Error(), 0)
		return
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	fail := func(err error) {
		tw.Close()
		gz.Close()
		file.Close()
		s.finishBackup(id, "failed", err.Error(), 0)
	}
	tempDir := filepath.Join(s.cfg.BackupDir, ".work-"+id)
	_ = os.MkdirAll(tempDir, 0o700)
	defer os.RemoveAll(tempDir)
	for _, content := range contents {
		switch content {
		case "panel_database":
			dumpPath := filepath.Join(tempDir, "meridian.dump")
			if err := runCommandToFile(ctx, dumpPath, "runuser", "-u", "postgres", "--", "pg_dump", "-Fc", "meridian"); err != nil {
				fail(fmt.Errorf("database dump: %w", err))
				return
			}
			if err := addPathToTar(tw, dumpPath, "database/meridian.dump"); err != nil {
				fail(err)
				return
			}
		case "postfix":
			if err := addDirectoryToTar(tw, s.cfg.PostfixDir, "config/etc/postfix"); err != nil {
				fail(err)
				return
			}
		case "dovecot":
			if err := addDirectoryToTar(tw, s.cfg.DovecotDir, "config/etc/dovecot"); err != nil {
				fail(err)
				return
			}
		case "dkim":
			if err := addDirectoryToTar(tw, s.cfg.DKIMDir, "config/var/lib/rspamd/dkim"); err != nil {
				fail(err)
				return
			}
		case "letsencrypt":
			if err := addDirectoryToTar(tw, "/etc/letsencrypt", "config/etc/letsencrypt"); err != nil {
				fail(err)
				return
			}
		case "maildirs":
			if err := addDirectoryToTar(tw, s.cfg.VmailRoot, "mail/vmail"); err != nil {
				fail(err)
				return
			}
		}
	}
	if err := tw.Close(); err != nil {
		fail(err)
		return
	}
	if err := gz.Close(); err != nil {
		fail(err)
		file.Close()
		s.finishBackup(id, "failed", err.Error(), 0)
		return
	}
	if err := file.Close(); err != nil {
		s.finishBackup(id, "failed", err.Error(), 0)
		return
	}
	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	s.finishBackup(id, "completed", "Archive created", size)
	ctxNotify, cancelNotify := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelNotify()
	s.notifyEvent(ctxNotify, "backup.completed", map[string]any{"name": filepath.Base(path), "sizeBytes": size})
}

func (s *Server) finishBackup(id, status, detail string, size int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var name string
	_ = s.db.QueryRow(ctx, `SELECT name FROM control.backups WHERE id=$1`, id).Scan(&name)
	_, _ = s.db.Exec(ctx, `UPDATE control.backups SET status=$2, detail=$3, size_bytes=$4, finished_at=now() WHERE id=$1`, id, status, detail, size)
	if status == "failed" {
		s.notifyEvent(ctx, "backup.failed", map[string]any{"name": name, "error": detail})
	}
}

func addDirectoryToTar(tw *tar.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) || os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSocket != 0 || info.Mode()&os.ModeDevice != 0 {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join(prefix, relative))
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(path); err == nil {
				header.Linkname = target
			}
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return nil
			}
			_, copyErr := io.Copy(tw, file)
			file.Close()
			return copyErr
		}
		return nil
	})
}

func addPathToTar(tw *tar.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(tw, file)
	return err
}

func runCommandToFile(ctx context.Context, path, name string, args ...string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdout = file
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var path, status, name string
	err := s.db.QueryRow(r.Context(), `SELECT path,status,name FROM control.backups WHERE id=$1`, id).Scan(&path, &status, &name)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if status != "completed" {
		writeError(w, http.StatusConflict, "BACKUP", "The backup archive is not ready")
		return
	}
	cleanPath, _ := filepath.Abs(path)
	base, _ := filepath.Abs(s.cfg.BackupDir)
	if !strings.HasPrefix(cleanPath, base+string(os.PathSeparator)) {
		writeError(w, http.StatusForbidden, "BACKUP", "Backup path is outside the managed directory")
		return
	}
	s.actorAudit(r, sess, "backup.downloaded", name, "Downloaded a backup archive", nil)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(cleanPath)+"\"")
	http.ServeFile(w, r, cleanPath)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var path, name, status string
	err := s.db.QueryRow(r.Context(), `SELECT path,name,status FROM control.backups WHERE id=$1`, id).Scan(&path, &name, &status)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if status == "running" {
		writeError(w, http.StatusConflict, "BACKUP", "Wait for the backup to finish before deleting it")
		return
	}
	cleanPath, _ := filepath.Abs(path)
	base, _ := filepath.Abs(s.cfg.BackupDir)
	if strings.HasPrefix(cleanPath, base+string(os.PathSeparator)) {
		_ = os.Remove(cleanPath)
	}
	_, _ = s.db.Exec(r.Context(), `DELETE FROM control.backups WHERE id=$1`, id)
	s.actorAudit(r, sess, "backup.deleted", name, "Deleted a backup archive", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Confirm  string   `json:"confirm"`
		Contents []string `json:"contents"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if strings.TrimSpace(body.Confirm) != "RESTORE" {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type RESTORE to confirm")
		return
	}
	var path, name, status string
	if err := s.db.QueryRow(r.Context(), `SELECT path,name,status FROM control.backups WHERE id=$1`, id).Scan(&path, &name, &status); err != nil {
		writeFromError(w, err)
		return
	}
	if status != "completed" {
		writeError(w, http.StatusConflict, "BACKUP", "That archive is not available for restore")
		return
	}
	selected := map[string]bool{}
	for _, content := range body.Contents {
		if _, ok := backupContentNames[content]; ok {
			selected[content] = true
		}
	}
	if len(selected) == 0 {
		selected["panel_database"] = true
		selected["postfix"] = true
		selected["dovecot"] = true
		selected["dkim"] = true
		selected["letsencrypt"] = true
	}
	stage := filepath.Join(s.cfg.BackupDir, ".restore-"+id+"-"+time.Now().UTC().Format("20060102150405"))
	if err := os.MkdirAll(stage, 0o700); err != nil {
		writeFromError(w, err)
		return
	}
	defer os.RemoveAll(stage)
	if err := extractBackup(path, stage, selected); err != nil {
		writeError(w, http.StatusBadRequest, "RESTORE", "Archive extraction failed: "+err.Error())
		return
	}
	replaced := []string{}
	counts := map[string]int{}
	if selected["panel_database"] {
		dump := filepath.Join(stage, "database", "meridian.dump")
		result, err := runCommand(r.Context(), 10*time.Minute, "runuser", "-u", "postgres", "--",
			"pg_restore", "--clean", "--if-exists", "--no-owner", "--no-privileges", "-d", "meridian", dump)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "Database restore failed: "+strings.TrimSpace(result.Stderr))
			return
		}
		replaced = append(replaced, "PostgreSQL meridian database")
	}
	if selected["postfix"] {
		count, err := copyTree(filepath.Join(stage, "config/etc/postfix"), s.cfg.PostfixDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "Postfix config restore failed: "+err.Error())
			return
		}
		counts["postfix"] = count
		replaced = append(replaced, fmt.Sprintf("%d Postfix files", count))
	}
	if selected["dovecot"] {
		count, err := copyTree(filepath.Join(stage, "config/etc/dovecot"), s.cfg.DovecotDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "Dovecot config restore failed: "+err.Error())
			return
		}
		counts["dovecot"] = count
		replaced = append(replaced, fmt.Sprintf("%d Dovecot files", count))
	}
	if selected["dkim"] {
		count, err := copyTree(filepath.Join(stage, "config/var/lib/rspamd/dkim"), s.cfg.DKIMDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "DKIM restore failed: "+err.Error())
			return
		}
		counts["dkim"] = count
		replaced = append(replaced, fmt.Sprintf("%d DKIM files", count))
	}
	if selected["letsencrypt"] {
		count, err := copyTree(filepath.Join(stage, "config/etc/letsencrypt"), "/etc/letsencrypt")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "Certificate restore failed: "+err.Error())
			return
		}
		counts["letsencrypt"] = count
		replaced = append(replaced, fmt.Sprintf("%d certificate files", count))
	}
	if selected["maildirs"] {
		count, err := copyTree(filepath.Join(stage, "mail/vmail"), s.cfg.VmailRoot)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "RESTORE", "Maildir restore failed: "+err.Error())
			return
		}
		counts["maildirs"] = count
		replaced = append(replaced, fmt.Sprintf("%d Maildir files", count))
	}
	_ = reloadPostfix(r.Context())
	_, _ = runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "dovecot")
	_, _ = runCommand(r.Context(), 30*time.Second, "systemctl", "reload", "rspamd")
	s.actorAudit(r, sess, "backup.restored", name, "Restored a backup archive", map[string]any{"replaced": replaced, "counts": counts})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": replaced, "counts": counts, "archive": name})
}

func extractBackup(path, stage string, selected map[string]bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	allowed := func(header *tar.Header) bool {
		name := filepath.ToSlash(header.Name)
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return false
		}
		switch {
		case strings.HasPrefix(clean, "database/") && selected["panel_database"]:
			return true
		case strings.HasPrefix(clean, "config/etc/postfix/") && selected["postfix"]:
			return true
		case strings.HasPrefix(clean, "config/etc/dovecot/") && selected["dovecot"]:
			return true
		case strings.HasPrefix(clean, "config/var/lib/rspamd/dkim/") && selected["dkim"]:
			return true
		case strings.HasPrefix(clean, "config/etc/letsencrypt/") && selected["letsencrypt"]:
			return true
		case strings.HasPrefix(clean, "mail/vmail/") && selected["maildirs"]:
			return true
		default:
			return false
		}
	}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !allowed(header) {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(header.Name))
		if !strings.HasPrefix(target, filepath.Clean(stage)+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes the restore directory")
		}
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeSymlink {
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
			continue
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

func copyTree(source, destination string) (int, error) {
	if !pathExists(source) {
		return 0, nil
	}
	count := 0
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			_ = os.Symlink(link, target)
			count++
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		output.Close()
		if copyErr == nil {
			count++
		}
		return copyErr
	})
	return count, err
}
