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

	"github.com/meridian-mail/meridian/backend/internal/config"
	"github.com/meridian-mail/meridian/backend/internal/store"
)

var pathPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+=-]{0,127}$`)

type mailboxRow struct {
	id, domain, localPart, maildirPath, status string
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
		SELECT m.id::text, m.domain::text, m.local_part::text, m.maildir_path, m.provision_status
		FROM mailboxes m
		JOIN users u ON u.id = m.user_id
		JOIN tenants t ON t.id = m.tenant_id
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
		if rows.Scan(&box.id, &box.domain, &box.localPart, &box.maildirPath, &box.status) == nil {
			boxes = append(boxes, box)
		}
	}
	rows.Close()

	ready, failed := 0, 0
	for _, box := range boxes {
		path, err := provisionMaildir(cfg.MaildirBase, box.domain, box.localPart, uid, gid)
		if err != nil {
			failed++
			_, _ = st.DB.Exec(ctx, `
				UPDATE mailboxes SET provision_status = 'error', provision_attempts = provision_attempts + 1,
				       provision_error = $2 WHERE id = $1
			`, box.id, truncate(err.Error(), 500))
			logger.Warn("mailbox provisioning failed", "id", box.id, "domain", box.domain, "local", box.localPart, "err", err)
			continue
		}
		if _, err := st.DB.Exec(ctx, `
			UPDATE mailboxes SET maildir_path = $2, provision_status = 'ready', provisioned_at = now(),
			       provision_attempts = provision_attempts + 1, provision_error = ''
			WHERE id = $1
		`, box.id, path); err != nil {
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
	root := filepath.Join(base, domain)
	userDir := filepath.Join(root, localPart)
	maildir := filepath.Join(userDir, "Maildir")
	if !strings.HasPrefix(maildir, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("mailbox path escapes the mail root")
	}
	for _, directory := range []string{filepath.Join(base, domain), userDir, maildir} {
		info, err := os.Lstat(directory)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("mailbox path contains a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	for _, directory := range []string{root, userDir, maildir,
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

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
