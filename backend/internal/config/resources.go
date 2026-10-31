package config

import (
	"os"
	"strconv"
	"strings"
)

// hostMemoryMB returns the effective memory ceiling for this process. It
// prefers a finite cgroup limit (containers and systemd slices) and falls back
// to /proc/meminfo on a normal host. Zero means the platform did not expose a
// value.
func hostMemoryMB() int {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || value == "max" {
			continue
		}
		bytes, err := strconv.ParseInt(value, 10, 64)
		if err != nil || bytes <= 0 || bytes > 1<<50 {
			continue
		}
		return int(bytes / (1024 * 1024))
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && kb > 0 {
			return int(kb / 1024)
		}
	}
	return 0
}

// applyHostDefaults keeps the service conservative on small mail hosts. These
// are upper bounds, not reservations: a 2 GB server gets at most two IMAP sync
// workers and eight database connections, while larger hosts keep the values
// supplied by the operator.
func applyHostDefaults(cfg *Config) {
	applyHostDefaultsAt(cfg, hostMemoryMB())
}

func applyHostDefaultsAt(cfg *Config, total int) {
	if total <= 0 {
		return
	}
	switch {
	case total < 3072:
		if cfg.MailWorkerConcurrency > 2 {
			cfg.MailWorkerConcurrency = 2
		}
		if cfg.DBMaxConns > 8 {
			cfg.DBMaxConns = 8
		}
	case total < 6144:
		if cfg.MailWorkerConcurrency > 4 {
			cfg.MailWorkerConcurrency = 4
		}
		if cfg.DBMaxConns > 12 {
			cfg.DBMaxConns = 12
		}
	}
}

// defaultMemoryLimitMB derives a soft Go heap ceiling. GOMEMLIMIT is a GC
// target, not a hard allocation cap, so it keeps the runtime from treating a
// small host's free memory as an invitation to grow the heap after a sync.
func defaultMemoryLimitMB(totalMB int) int {
	if totalMB <= 0 {
		return 0
	}
	limit := totalMB / 3
	if limit < 384 {
		limit = 384
	}
	if limit > 2048 {
		limit = 2048
	}
	return limit
}
