package control

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type restoreImportFunc func(context.Context, string, string, string, string) (commandResult, error)

type mailboxRestoreRequest struct {
	TenantID string `json:"tenantId"`
	Email    string `json:"email"`
	Folder   string `json:"folder"`
	Since    string `json:"since"`
	Until    string `json:"until"`
}

type mailboxRestoreTarget struct {
	TenantID    string
	UserID      string
	Email       string
	Domain      string
	Local       string
	MaildirPath string
	Folder      string
	MaildirDir  string
	Destination string
}

type mailboxRestorePreview struct {
	Email        string `json:"email"`
	Folder       string `json:"folder"`
	MaildirDir   string `json:"maildirDir"`
	Destination  string `json:"destination"`
	Messages     int    `json:"messages"`
	Conflicts    int    `json:"conflicts"`
	Bytes        int64  `json:"bytes"`
	Earliest     string `json:"earliest,omitempty"`
	Latest       string `json:"latest,omitempty"`
	Since        string `json:"since,omitempty"`
	Until        string `json:"until,omitempty"`
	Archive      string `json:"archive,omitempty"`
	ArchiveBytes int64  `json:"archiveBytes,omitempty"`
}

type restoreJob struct {
	ID        string         `json:"id"`
	BackupID  string         `json:"backupId"`
	TenantID  string         `json:"tenantId"`
	UserID    string         `json:"userId"`
	Email     string         `json:"email"`
	Folder    string         `json:"folder"`
	Mode      string         `json:"mode"`
	Status    string         `json:"status"`
	Preview   map[string]any `json:"preview"`
	Result    map[string]any `json:"result"`
	Detail    string         `json:"detail"`
	CreatedAt time.Time      `json:"createdAt"`
	Finished  *time.Time     `json:"finishedAt,omitempty"`
}

var restoreFolderPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`)

func restoreFolderMapping(raw string) (folder, maildirDir, destination string, err error) {
	folder = strings.ToLower(strings.TrimSpace(raw))
	if folder == "" {
		folder = "inbox"
	}
	switch folder {
	case "inbox":
		return folder, "", "INBOX", nil
	case "sent":
		return folder, ".Sent", "Sent", nil
	case "drafts":
		return folder, ".Drafts", "Drafts", nil
	case "trash":
		return folder, ".Trash", "Trash", nil
	case "spam":
		return folder, ".Junk", "Junk", nil
	case "archive":
		return folder, ".Archive", "Archive", nil
	default:
		if !restoreFolderPattern.MatchString(folder) {
			return "", "", "", errors.New("invalid restore folder")
		}
		return folder, "." + folder, folder, nil
	}
}

func parseRestoreBound(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return &parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("invalid restore date %q", raw)
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return &parsed, nil
}

func (s *Server) resolveMailboxRestoreTarget(ctx context.Context, req mailboxRestoreRequest) (mailboxRestoreTarget, error) {
	var target mailboxRestoreTarget
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return target, errors.New("a mailbox email is required")
	}
	folder, maildirDir, destination, err := restoreFolderMapping(req.Folder)
	if err != nil {
		return target, err
	}
	err = s.db.QueryRow(ctx, `
		SELECT u.id::text, u.tenant_id::text, u.email::text,
		       COALESCE(m.domain::text,''), COALESCE(m.local_part::text,''), COALESCE(m.maildir_path,'')
		FROM public.users u
		JOIN public.tenants t ON t.id=u.tenant_id AND t.status='active'
		JOIN public.mailboxes m ON m.user_id=u.id AND m.active
		WHERE u.email=$1 AND u.status='active' AND u.deleted_at IS NULL
		LIMIT 1
	`, email).Scan(&target.UserID, &target.TenantID, &target.Email, &target.Domain, &target.Local, &target.MaildirPath)
	if err != nil {
		return target, errors.New("active mailbox not found")
	}
	if tenantID := strings.TrimSpace(req.TenantID); tenantID != "" && tenantID != target.TenantID {
		return target, errors.New("mailbox does not belong to the requested tenant")
	}
	if target.Domain == "" || target.Local == "" {
		return target, errors.New("mailbox has no Maildir mapping")
	}
	live := target.MaildirPath
	if live == "" {
		live = filepath.Join(s.cfg.VmailRoot, target.Domain, target.Local, "Maildir")
	}
	resolved, err := filepath.Abs(live)
	if err != nil {
		return target, err
	}
	if !s.isManagedMailPath(ctx, resolved) {
		return target, errors.New("mailbox path is outside the managed mail volumes")
	}
	target.MaildirPath = resolved
	target.Folder = folder
	target.MaildirDir = maildirDir
	target.Destination = destination
	target.Email = email
	return target, nil
}

func (s *Server) restoreBackupPath(ctx context.Context, backupID string) (string, string, error) {
	var name, filePath, status string
	if err := s.db.QueryRow(ctx, `SELECT name, path, status FROM control.backups WHERE id::text=$1`, strings.TrimSpace(backupID)).Scan(&name, &filePath, &status); err != nil {
		return "", "", errors.New("backup archive not found")
	}
	if status != "completed" {
		return "", "", errors.New("only completed backups can be restored")
	}
	clean := filepath.Clean(filePath)
	root, err := filepath.Abs(s.cfg.BackupDir)
	if err != nil {
		return "", "", err
	}
	resolved, err := filepath.Abs(clean)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("backup path is outside the managed backup directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("backup archive is not available")
	}
	return name, resolved, nil
}

func restoreEntryRelative(name, prefix string) (string, bool) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == prefix {
		return "", false
	}
	if !strings.HasPrefix(clean, prefix+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(clean, prefix+"/")
	if strings.HasPrefix(rel, "cur/") || strings.HasPrefix(rel, "new/") {
		rest := strings.TrimPrefix(strings.TrimPrefix(rel, "cur/"), "new/")
		if rest != "" && !strings.Contains(rest, "/") {
			return rel, true
		}
	}
	return "", false
}

func (s *Server) scanMailboxArchive(archivePath string, target mailboxRestoreTarget, since, until *time.Time) (mailboxRestorePreview, error) {
	preview := mailboxRestorePreview{
		Email: target.Email, Folder: target.Folder, MaildirDir: target.MaildirDir,
		Destination: target.Destination, Archive: filepath.Base(archivePath),
	}
	if since != nil {
		preview.Since = since.UTC().Format(time.RFC3339)
	}
	if until != nil {
		preview.Until = until.UTC().Format(time.RFC3339)
	}
	archivePrefix := path.Join("mail", "vmail", target.Domain, target.Local, "Maildir")
	folderPrefix := archivePrefix
	if target.MaildirDir != "" {
		folderPrefix = path.Join(archivePrefix, target.MaildirDir)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return preview, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return preview, errors.New("backup archive is not a readable gzip file")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return preview, err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		rel, ok := restoreEntryRelative(header.Name, folderPrefix)
		if !ok {
			continue
		}
		if since != nil && header.ModTime.Before(*since) {
			continue
		}
		if until != nil && header.ModTime.After(*until) {
			continue
		}
		preview.Messages++
		preview.Bytes += header.Size
		if existing, err := os.Stat(filepath.Join(target.MaildirPath, target.MaildirDir, filepath.FromSlash(rel))); err == nil && existing.Mode().IsRegular() {
			preview.Conflicts++
		}
		if preview.Earliest == "" || header.ModTime.Before(mustParseTime(preview.Earliest)) {
			preview.Earliest = header.ModTime.UTC().Format(time.RFC3339)
		}
		if preview.Latest == "" || header.ModTime.After(mustParseTime(preview.Latest)) {
			preview.Latest = header.ModTime.UTC().Format(time.RFC3339)
		}
	}
	if info, err := os.Stat(archivePath); err == nil {
		preview.ArchiveBytes = info.Size()
	}
	return preview, nil
}

func mustParseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func (s *Server) previewMailboxRestore(ctx context.Context, backupID string, req mailboxRestoreRequest) (mailboxRestorePreview, mailboxRestoreTarget, string, error) {
	name, archivePath, err := s.restoreBackupPath(ctx, backupID)
	if err != nil {
		return mailboxRestorePreview{}, mailboxRestoreTarget{}, "", err
	}
	target, err := s.resolveMailboxRestoreTarget(ctx, req)
	if err != nil {
		return mailboxRestorePreview{}, mailboxRestoreTarget{}, "", err
	}
	since, err := parseRestoreBound(req.Since, false)
	if err != nil {
		return mailboxRestorePreview{}, target, name, err
	}
	until, err := parseRestoreBound(req.Until, true)
	if err != nil {
		return mailboxRestorePreview{}, target, name, err
	}
	preview, err := s.scanMailboxArchive(archivePath, target, since, until)
	return preview, target, name, err
}

func (s *Server) managedMailRoots(ctx context.Context) []string {
	roots := []string{}
	seen := map[string]bool{}
	add := func(root string) {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	add(s.cfg.VmailRoot)
	if volumes, err := s.storageVolumeRows(ctx); err == nil {
		for _, volume := range volumes {
			if volume.Role == "mail" {
				add(volume.MountPath)
			}
		}
	}
	return roots
}

func (s *Server) isManagedMailPath(ctx context.Context, path string) bool {
	for _, root := range s.managedMailRoots(ctx) {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

// isMailboxHomePath rejects the volume root itself. A home must be at least
// <volume>/<domain>/<local>; a malformed database path must never let a move,
// delete or restore operate on the whole mount.
func (s *Server) isMailboxHomePath(ctx context.Context, path string) bool {
	for _, root := range s.managedMailRoots(ctx) {
		if pathWithin(root, path) && !sameStoragePath(root, path) {
			return true
		}
	}
	return false
}

func (s *Server) restoreMaildirTrees(ctx context.Context, stage string) (int, error) {
	sourceRoot := filepath.Join(stage, "mail", "vmail")
	if !pathExists(sourceRoot) {
		return 0, nil
	}
	uid, gid, err := mailOwnerLookup()
	if err != nil {
		return 0, fmt.Errorf("vmail account: %w", err)
	}
	domains, err := os.ReadDir(sourceRoot)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, domainEntry := range domains {
		if !domainEntry.IsDir() {
			continue
		}
		domain := normalizeDomain(domainEntry.Name())
		if !validDomain(domain) {
			continue
		}
		locals, err := os.ReadDir(filepath.Join(sourceRoot, domainEntry.Name()))
		if err != nil {
			return total, err
		}
		for _, localEntry := range locals {
			if !localEntry.IsDir() {
				continue
			}
			local := normalizeLocal(localEntry.Name())
			if !validLocalPart(local) {
				continue
			}
			sourceHome := filepath.Join(sourceRoot, domainEntry.Name(), localEntry.Name())
			targetHome, err := s.restoreHomeForMailbox(ctx, domain, local)
			if err != nil {
				return total, err
			}
			if !s.isMailboxHomePath(ctx, targetHome) {
				return total, fmt.Errorf("restore target %s is outside the managed mail volumes", targetHome)
			}
			count, err := copyTree(sourceHome, targetHome)
			if err != nil {
				return total, err
			}
			if count > 0 {
				if err := chownTree(targetHome, uid, gid); err != nil {
					return total, err
				}
			}
			total += count
		}
	}
	return total, nil
}

func (s *Server) restoreHomeForMailbox(ctx context.Context, domain, local string) (string, error) {
	email := local + "@" + domain
	var home, maildir string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(mail_home, ''), COALESCE(maildir_path, '')
		FROM public.mailboxes WHERE email = $1 LIMIT 1
	`, email).Scan(&home, &maildir)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if strings.TrimSpace(home) != "" {
		return filepath.Clean(home), nil
	}
	if strings.TrimSpace(maildir) != "" {
		return filepath.Dir(filepath.Clean(maildir)), nil
	}
	return filepath.Join(s.cfg.VmailRoot, domain, local), nil
}

func chownTree(root string, uid, gid int) error {
	if uid < 0 {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chown(path, uid, gid)
	})
}

func (s *Server) extractMaildirArchive(archivePath string, target mailboxRestoreTarget, stage string, since, until *time.Time) (int, error) {
	archivePrefix := path.Join("mail", "vmail", target.Domain, target.Local, "Maildir")
	folderPrefix := archivePrefix
	if target.MaildirDir != "" {
		folderPrefix = path.Join(archivePrefix, target.MaildirDir)
	}
	stageRoot, err := filepath.Abs(stage)
	if err != nil {
		return 0, err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 0, errors.New("backup archive is not a readable gzip file")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, err
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 {
			continue
		}
		rel, ok := restoreEntryRelative(header.Name, folderPrefix)
		if !ok {
			continue
		}
		if since != nil && header.ModTime.Before(*since) {
			continue
		}
		if until != nil && header.ModTime.After(*until) {
			continue
		}
		// Additive restore: an exact Maildir filename already present in the
		// live mailbox is never imported again.
		if existing, err := os.Stat(filepath.Join(target.MaildirPath, target.MaildirDir, filepath.FromSlash(rel))); err == nil && existing.Mode().IsRegular() {
			continue
		}
		targetPath := filepath.Join(stageRoot, filepath.FromSlash(rel))
		resolved, err := filepath.Abs(targetPath)
		if err != nil || !strings.HasPrefix(resolved, stageRoot+string(os.PathSeparator)) {
			return count, errors.New("archive entry escapes the restore staging directory")
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
			return count, err
		}
		out, err := os.OpenFile(resolved, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return count, err
		}
		if _, err := io.CopyN(out, tr, header.Size); err != nil {
			_ = out.Close()
			return count, err
		}
		if err := out.Close(); err != nil {
			return count, err
		}
		_ = os.Chtimes(resolved, header.ModTime, header.ModTime)
		count++
	}
	return count, nil
}

func (s *Server) createRestoreJob(ctx context.Context, backupID string, target mailboxRestoreTarget, mode string, preview mailboxRestorePreview, createdBy string) (string, error) {
	previewJSON, _ := json.Marshal(preview)
	var id string
	err := s.db.QueryRow(ctx, `
		INSERT INTO control.restore_jobs (backup_id, tenant_id, user_id, email, folder, mode, preview, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, NULLIF($8,'')::uuid)
		RETURNING id::text
	`, backupID, target.TenantID, target.UserID, target.Email, target.Folder, mode, previewJSON, createdBy).Scan(&id)
	return id, err
}

func (s *Server) updateRestoreJob(id, status string, result any, detail string) {
	resultJSON, _ := json.Marshal(result)
	_, _ = s.db.Exec(context.Background(), `
		UPDATE control.restore_jobs SET status=$2, result=$3::jsonb, detail=$4, finished_at=now() WHERE id=$1
	`, id, status, resultJSON, truncateText(detail, 800))
}

func (s *Server) runMailboxRestore(jobID, backupID string, req mailboxRestoreRequest, actor *adminUser) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	name, archivePath, err := s.restoreBackupPath(ctx, backupID)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	target, err := s.resolveMailboxRestoreTarget(ctx, req)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	since, err := parseRestoreBound(req.Since, false)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	until, err := parseRestoreBound(req.Until, true)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	preview, err := s.scanMailboxArchive(archivePath, target, since, until)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	stage, err := s.prepareImportStage(jobID)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, "maildir")
	extracted, err := s.extractMaildirArchive(archivePath, target, staged, since, until)
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	if err := s.importStageOwner(staged); err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{}, err.Error())
		return
	}
	if target.Destination != "INBOX" {
		_, _ = runCommand(ctx, 30*time.Second, "doveadm", "mailbox", "create", "-u", target.Email, target.Destination)
	}
	importer := s.restoreImporter
	if importer == nil {
		importer = s.runDoveadmImport
	}
	importResult, err := importer(ctx, target.Email, "maildir:"+staged, target.Destination, "ALL")
	if err != nil {
		s.updateRestoreJob(jobID, "failed", map[string]any{"messages": preview.Messages, "files": extracted}, err.Error())
		return
	}
	result := map[string]any{
		"folder": target.Folder, "destination": target.Destination, "messages": preview.Messages,
		"conflicts": preview.Conflicts, "files": extracted, "backup": name,
		"doveadmOutput": truncateText(strings.TrimSpace(importResult.Stdout), 300),
	}
	s.updateRestoreJob(jobID, "completed", result, "Maildir restored and queued for webmail indexing")
	if actor != nil {
		s.audit(context.Background(), actor, "backup.mailbox_restored", target.Email,
			"Restored folder "+target.Folder+" from "+name, net.IP(nil), "", result)
	}
}

func (s *Server) loadRestoreJob(ctx context.Context, id string) (restoreJob, error) {
	var job restoreJob
	var previewJSON, resultJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT id::text, backup_id::text, tenant_id::text, user_id::text, email::text, folder, mode, status,
		       preview, result, detail, created_at, finished_at
		FROM control.restore_jobs WHERE id::text=$1
	`, strings.TrimSpace(id)).Scan(&job.ID, &job.BackupID, &job.TenantID, &job.UserID, &job.Email,
		&job.Folder, &job.Mode, &job.Status, &previewJSON, &resultJSON, &job.Detail, &job.CreatedAt, &job.Finished)
	if err == nil {
		_ = json.Unmarshal(previewJSON, &job.Preview)
		_ = json.Unmarshal(resultJSON, &job.Result)
	}
	return job, err
}
