package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func fileDigest(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func writeManagedFile(path, content string, mode os.FileMode, uid, gid int) (bool, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || strings.Contains(clean, "..") {
		return false, fmt.Errorf("managed file path must be absolute")
	}
	if existing, err := os.ReadFile(clean); err == nil && string(existing) == content {
		if err := os.Chmod(clean, mode); err == nil && (uid >= 0 || gid >= 0) {
			_ = os.Chown(clean, uid, gid)
		}
		return false, nil
	}
	if existing, err := os.ReadFile(clean); err == nil && len(existing) > 0 {
		backupDir := filepath.Join("/var/backups/alt8mail", "config", time.Now().UTC().Format("20060102"))
		if err := os.MkdirAll(backupDir, 0o700); err == nil {
			_ = os.WriteFile(filepath.Join(backupDir, filepath.Base(clean)+"."+time.Now().UTC().Format("150405.000000")), existing, 0o600)
		}
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(clean), ".alt8mail-control-*")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return false, err
	}
	if uid >= 0 || gid >= 0 {
		if err := os.Chown(tempName, uid, gid); err != nil {
			return false, err
		}
	}
	if err := os.Rename(tempName, clean); err != nil {
		return false, err
	}
	return true, nil
}

func removeManagedFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func postmap(ctx context.Context, path string) (commandResult, error) {
	result, err := runCommand(ctx, 20*time.Second, "postmap", path)
	return result, err
}

func postconf(ctx context.Context, args ...string) (commandResult, error) {
	command := append([]string{"-e"}, args...)
	return runCommand(ctx, 20*time.Second, "postconf", command...)
}

func reloadPostfix(ctx context.Context) error {
	result, err := runCommand(ctx, 30*time.Second, "postfix", "reload")
	if err != nil {
		return fmt.Errorf("postfix reload: %s", strings.TrimSpace(result.Stderr))
	}
	return nil
}

// movePath archives or relocates a file tree across filesystems. os.Rename is
// preferred because it is atomic; when the target is on another mount (for
// example /var/backups), cp -a preserves ownership and modes before the source
// is removed. Callers must only use absolute managed paths.
func movePath(ctx context.Context, source, destination string) error {
	if !pathExists(source) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	result, err := runCommand(ctx, 30*time.Minute, "cp", "-a", "--", source, destination)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("copy to archive: %s", detail)
	}
	if err := os.RemoveAll(source); err != nil {
		return fmt.Errorf("remove archived source: %w", err)
	}
	return nil
}
