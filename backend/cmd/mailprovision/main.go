package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
)

var pathPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+=-]{0,127}$`)

type mailboxRow struct {
	id, domain, localPart, maildirPath, status, mailHome, volumeID, volumeMount string
}

func main() {
	verifyAll := flag.Bool("all", false, "re-verify every active mailbox instead of only pending or failed rows")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("storage initialization failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	uid, gid, err := vaultOwner()
	if err != nil {
		logger.Error("mailbox owner lookup failed", "err", err)
		os.Exit(1)
	}
	query := `
		SELECT m.id::text, m.domain::text, m.local_part::text, m.maildir_path, m.provision_status,
		       COALESCE(m.mail_home, ''), COALESCE(m.storage_volume_id::text, ''),
		       COALESCE(v.mount_path, '')
		FROM mailboxes m
		JOIN users u ON u.id = m.user_id
		JOIN tenants t ON t.id = m.tenant_id
		LEFT JOIN control.storage_volumes v ON v.id = m.storage_volume_id
		WHERE m.active = true AND u.status = 'active' AND u.deleted_at IS NULL AND t.status = 'active'`
	if !*verifyAll {
		query += ` AND m.provision_status <> 'ready'`
	}
	query += ` ORDER BY m.id`
	rows, err := st.DB.Query(ctx, query)
	if err != nil {
		logger.Error("mailbox query failed", "err", err)
		os.Exit(1)
	}
	boxes := []mailboxRow{}
	for rows.Next() {
		var box mailboxRow
		if rows.Scan(&box.id, &box.domain, &box.localPart, &box.maildirPath, &box.status,
			&box.mailHome, &box.volumeID, &box.volumeMount) == nil {
			boxes = append(boxes, box)
		}
	}
	rows.Close()

	ready, failed := 0, 0
	for _, box := range boxes {
		home := strings.TrimSpace(box.mailHome)
		volumeRoot := strings.TrimSpace(box.volumeMount)
		if home == "" {
			if volumeRoot != "" {
				home = filepath.Join(volumeRoot, box.domain, box.localPart)
			} else {
				home = filepath.Join(cfg.MaildirBase, box.domain, box.localPart)
			}
		}
		// A database path is not trusted on its own. A placed home must be
		// inside the mount path of the volume it claims to belong to.
		if volumeRoot != "" && !pathInside(volumeRoot, home) {
			failed++
			_, _ = st.DB.Exec(ctx, `
				UPDATE mailboxes SET provision_status = 'error', provision_attempts = provision_attempts + 1,
				       provision_error = $2 WHERE id = $1
			`, box.id, "mail home is outside its registered volume")
			logger.Warn("mailbox provisioning rejected an out-of-volume path", "id", box.id, "home", home, "volume", volumeRoot)
			continue
		}
		path, err := provisionMailHome(home, uid, gid)
		if err != nil {
			failed++
			_, _ = st.DB.Exec(ctx, `
				UPDATE mailboxes SET provision_status = 'error', provision_attempts = provision_attempts + 1,
				       provision_error = $2 WHERE id = $1
			`, box.id, truncate(err.Error(), 500))
			logger.Warn("mailbox provisioning failed", "id", box.id, "domain", box.domain, "local", box.localPart, "err", err)
			continue
		}
		persistHome := ""
		if strings.TrimSpace(box.mailHome) != "" || (volumeRoot != "" && filepath.Clean(volumeRoot) != filepath.Clean(cfg.MaildirBase)) {
			persistHome = home
		}
		if _, err := st.DB.Exec(ctx, `
			UPDATE mailboxes SET maildir_path = $2, provision_status = 'ready', provisioned_at = now(),
			       provision_attempts = provision_attempts + 1, provision_error = '',
			       mail_home = CASE WHEN $3 <> '' THEN COALESCE(NULLIF(mail_home, ''), $3) ELSE mail_home END
			WHERE id = $1
		`, box.id, path, persistHome); err != nil {
			failed++
			logger.Warn("mailbox state update failed", "id", box.id, "err", err)
			continue
		}
		ready++
	}
	logger.Info("mailbox provisioning pass complete", "ready", ready, "failed", failed, "total", len(boxes))
}

func vaultOwner() (int, int, error) {
	entry, err := user.Lookup("vmail")
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(entry.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(entry.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func provisionMaildir(base, domain, localPart string, uid, gid int) (string, error) {
	if !pathPartPattern.MatchString(domain) || !pathPartPattern.MatchString(localPart) {
		return "", fmt.Errorf("unsafe mailbox path component")
	}
	base = filepath.Clean(base)
	return provisionMailHome(filepath.Join(base, domain, localPart), uid, gid)
}

func provisionMailHome(home string, uid, gid int) (string, error) {
	home = filepath.Clean(home)
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("mail home must be an absolute path")
	}
	domainDir := filepath.Dir(home)
	if domainDir == string(os.PathSeparator) || filepath.Dir(domainDir) == domainDir {
		return "", fmt.Errorf("mail home must live under a domain directory")
	}
	if !pathPartPattern.MatchString(filepath.Base(domainDir)) || !pathPartPattern.MatchString(filepath.Base(home)) {
		return "", fmt.Errorf("unsafe mailbox path component")
	}
	maildir := filepath.Join(home, "Maildir")
	for _, directory := range []string{domainDir, home, maildir} {
		info, err := os.Lstat(directory)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("mailbox path contains a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	for _, directory := range []string{domainDir, home, maildir,
		filepath.Join(maildir, "cur"), filepath.Join(maildir, "new"), filepath.Join(maildir, "tmp")} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return "", err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("mailbox path is not a real directory")
		}
		if err := os.Chown(directory, uid, gid); err != nil {
			return "", err
		}
		if err := os.Chmod(directory, 0o750); err != nil {
			return "", err
		}
	}
	for _, folder := range []string{"Sent", "Drafts", "Archive", "Junk", "Trash"} {
		folderDir := filepath.Join(maildir, "."+folder)
		for _, child := range []string{"", "cur", "new", "tmp"} {
			directory := folderDir
			if child != "" {
				directory = filepath.Join(folderDir, child)
			}
			if err := os.MkdirAll(directory, 0o750); err != nil {
				return "", err
			}
			if err := os.Chown(directory, uid, gid); err != nil {
				return "", err
			}
			if err := os.Chmod(directory, 0o750); err != nil {
				return "", err
			}
		}
	}
	return maildir, nil
}

func pathInside(root, child string) bool {
	root = filepath.Clean(root)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
