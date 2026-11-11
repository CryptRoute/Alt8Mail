package control

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	clamdConfPath    = "/etc/clamav/clamd.conf"
	clamavLimitBytes = int64(320 * 1024 * 1024)
)

var clamavLimitKeys = []string{"StreamMaxLength", "MaxFileSize", "MaxScanSize"}

// EnsureClamAVLimits raises clamd's stream and file scan limits so Cabinet
// uploads up to 300 MB can be scanned. It is idempotent, runs as root from the
// control-plane unit, and restores the previous config if clamd rejects it.
func EnsureClamAVLimits(logger *slog.Logger) error {
	if os.Geteuid() != 0 {
		return nil
	}
	path := strings.TrimSpace(os.Getenv("A8M_CLAMD_CONF"))
	if path == "" {
		path = clamdConfPath
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read clamd.conf: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	updated := append([]string(nil), lines...)
	changed := false
	for _, key := range clamavLimitKeys {
		found := false
		for index, line := range updated {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) < 2 || !strings.EqualFold(fields[0], key) {
				continue
			}
			found = true
			if !clamavLimitSufficient(fields[1]) {
				updated[index] = key + " 320M"
				changed = true
			}
		}
		if !found {
			updated = append(updated, key+" 320M")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(path+".a8m.bak", raw, mode); err != nil {
		return fmt.Errorf("backup clamd.conf: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(updated, "\n")), mode); err != nil {
		return fmt.Errorf("write clamd.conf: %w", err)
	}
	if err := restartClamAV(); err != nil {
		_ = os.WriteFile(path, raw, mode)
		_ = restartClamAV()
		return err
	}
	if logger != nil {
		logger.Info("raised ClamAV large-file scan limits", "streamMaxLength", "320M", "maxFileSize", "320M", "maxScanSize", "320M")
	}
	return nil
}

func clamavLimitSufficient(value string) bool {
	size := strings.ToUpper(strings.TrimSpace(value))
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(size, "G"):
		multiplier = 1024 * 1024 * 1024
		size = strings.TrimSuffix(size, "G")
	case strings.HasSuffix(size, "M"):
		multiplier = 1024 * 1024
		size = strings.TrimSuffix(size, "M")
	case strings.HasSuffix(size, "K"):
		multiplier = 1024
		size = strings.TrimSuffix(size, "K")
	}
	number, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	return err == nil && number*multiplier >= clamavLimitBytes
}

func restartClamAV() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", "restart", "clamav-daemon").CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart clamav-daemon: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
