package control

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	rspamdMultimapPath = "/etc/rspamd/local.d/multimap.conf"
	rspamdSettingsPath = "/etc/rspamd/local.d/settings.conf"
	localDomainsMap    = "/var/lib/rspamd/local_domains.map"
)

// EnsureMailGuard installs the local-domain anti-spoofing rules on hosts that
// were updated from a release that predates them. The updater cannot run the
// new release's configuration steps before switching versions, so the control
// service performs this idempotent repair before it starts (ExecStartPre).
func EnsureMailGuard(logger *slog.Logger) error {
	releaseRoot := mailGuardReleaseRoot()
	multimapSource := filepath.Join(releaseRoot, "deploy", "rspamd", "multimap.conf")
	settingsSource := filepath.Join(releaseRoot, "deploy", "rspamd", "settings.conf")
	if _, err := os.Stat(multimapSource); err != nil {
		logger.Warn("mail guard release files are unavailable", "root", releaseRoot, "err", err)
		return nil
	}
	type restore struct {
		path    string
		content string
		exists  bool
	}
	restores := []restore{}
	record := func(path string) {
		content, err := os.ReadFile(path)
		restores = append(restores, restore{path: path, content: string(content), exists: err == nil})
	}
	rollback := func() {
		for _, item := range restores {
			if item.exists {
				_ = os.WriteFile(item.path, []byte(item.content), 0o644)
			} else {
				_ = os.Remove(item.path)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(rspamdMultimapPath), 0o755); err != nil {
		return err
	}
	record(rspamdMultimapPath)
	record(rspamdSettingsPath)
	if err := ensureRspamdFile(rspamdMultimapPath, multimapSource, "A8M_REPORTED_SENDER", "A8M_LOCAL_DOMAIN_SPOOF", "# Domains hosted by this Alt8Mail instance"); err != nil {
		rollback()
		return err
	}
	if _, err := os.Stat(settingsSource); err == nil {
		if err := ensureRspamdFile(rspamdSettingsPath, settingsSource, "a8m_authenticated_submission", "a8m_authenticated_submission", ""); err != nil {
			rollback()
			return err
		}
	}
	if err := touchFile(localDomainsMap); err != nil {
		logger.Warn("local domain map touch failed", "err", err)
	}
	if entry, err := user.Lookup("_rspamd"); err == nil {
		uid, _ := strconv.Atoi(entry.Uid)
		gid, _ := strconv.Atoi(entry.Gid)
		_ = os.Chown(localDomainsMap, uid, gid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if result, err := runCommand(ctx, 25*time.Second, "rspamadm", "configtest"); err != nil {
		rollback()
		logger.Warn("Rspamd rejected the local-domain guard; previous configuration restored",
			"err", err, "output", strings.TrimSpace(result.Stderr+" "+result.Stdout))
		return nil
	}
	if _, err := runCommand(ctx, 20*time.Second, "systemctl", "try-reload-or-restart", "rspamd"); err != nil {
		logger.Warn("Rspamd reload after local-domain guard failed", "err", err)
	}
	// Populate local_domains.map immediately instead of waiting for the timer.
	// The provisioning service also creates any missing DKIM keys.
	if _, err := runCommand(ctx, 90*time.Second, "systemctl", "start", "a8m-dkimprovision.service"); err != nil {
		logger.Warn("DKIM/local-domain map provisioning did not run", "err", err)
	}
	return nil
}

func mailGuardReleaseRoot() string {
	if executable, err := os.Executable(); err == nil {
		root := filepath.Dir(filepath.Dir(executable))
		if _, err := os.Stat(filepath.Join(root, "deploy", "rspamd", "multimap.conf")); err == nil {
			return root
		}
	}
	return "/opt/a8m/current"
}

// ensureRspamdFile copies the source when the target is missing. When the
// target exists it appends only the missing Alt8Mail block so administrator
// rules are preserved.
func ensureRspamdFile(target, source, fullMarker, blockMarker, blockStart string) error {
	desired, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	current, readErr := os.ReadFile(target)
	if readErr != nil {
		return os.WriteFile(target, desired, 0o644)
	}
	text := string(current)
	if strings.Contains(text, blockMarker) {
		return nil
	}
	addition := string(desired)
	if blockStart != "" {
		index := strings.Index(addition, blockStart)
		if index < 0 {
			return fmt.Errorf("missing %q in %s", blockStart, source)
		}
		addition = addition[index:]
	} else if strings.Contains(text, fullMarker) {
		// The file already contains a different edition of the source block.
		return nil
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n# Alt8Mail local-domain guard\n" + addition
	return os.WriteFile(target, []byte(text), 0o644)
}

func touchFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}
