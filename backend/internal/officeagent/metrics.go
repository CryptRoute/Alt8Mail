package officeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RuntimeMetrics is a point-in-time view of the Office container and its host.
// It is intentionally read-only; the agent never mutates Docker while sampling.
type RuntimeMetrics struct {
	CheckedAt          string  `json:"checkedAt"`
	Container          string  `json:"container"`
	ContainerStatus    string  `json:"containerStatus"`
	Running            bool    `json:"running"`
	Image              string  `json:"image"`
	CPUPercent         float64 `json:"cpuPercent"`
	MemoryUsedBytes    uint64  `json:"memoryUsedBytes"`
	MemoryLimitBytes   uint64  `json:"memoryLimitBytes"`
	MemoryPercent      float64 `json:"memoryPercent"`
	NetworkRXBytes     uint64  `json:"networkRxBytes"`
	NetworkTXBytes     uint64  `json:"networkTxBytes"`
	BlockReadBytes     uint64  `json:"blockReadBytes"`
	BlockWriteBytes    uint64  `json:"blockWriteBytes"`
	PIDs               int     `json:"pids"`
	HostCPUs           int     `json:"hostCpus"`
	HostLoad1          float64 `json:"hostLoad1"`
	HostLoad5          float64 `json:"hostLoad5"`
	HostLoad15         float64 `json:"hostLoad15"`
	HostMemoryTotal    uint64  `json:"hostMemoryTotalBytes"`
	HostMemoryUsed     uint64  `json:"hostMemoryUsedBytes"`
	HostMemoryPercent  float64 `json:"hostMemoryPercent"`
	DiskTotalBytes     uint64  `json:"diskTotalBytes"`
	DiskUsedBytes      uint64  `json:"diskUsedBytes"`
	DiskPercent        float64 `json:"diskPercent"`
	DataDiskTotalBytes uint64  `json:"dataDiskTotalBytes"`
	DataDiskUsedBytes  uint64  `json:"dataDiskUsedBytes"`
	DataDiskPercent    float64 `json:"dataDiskPercent"`
	UptimeSeconds      uint64  `json:"uptimeSeconds"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runtimeMetrics(r.Context()))
}

func (s *Server) runtimeMetrics(ctx context.Context) RuntimeMetrics {
	st, _ := s.loadState()
	metrics := RuntimeMetrics{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Container: s.cfg.ContainerName,
		Image:     st.Image,
	}
	if output, _, err := s.run(ctx, 8*time.Second, s.cfg.DockerBin, "inspect", "--format", "{{.State.Status}}", s.cfg.ContainerName); err == nil {
		metrics.ContainerStatus = strings.TrimSpace(output)
		metrics.Running = metrics.ContainerStatus == "running"
	}
	if metrics.Running {
		s.sampleContainerStats(ctx, &metrics)
	}
	s.sampleHostMetrics(ctx, &metrics)
	return metrics
}

func (s *Server) sampleContainerStats(ctx context.Context, metrics *RuntimeMetrics) {
	output, _, err := s.run(ctx, 15*time.Second, s.cfg.DockerBin, "stats", "--no-stream", "--format", "{{json .}}", s.cfg.ContainerName)
	if err != nil {
		return
	}
	var stats struct {
		CPUPerc  string `json:"CPUPerc"`
		MemUsage string `json:"MemUsage"`
		MemPerc  string `json:"MemPerc"`
		NetIO    string `json:"NetIO"`
		BlockIO  string `json:"BlockIO"`
		PIDs     string `json:"PIDs"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &stats) != nil {
		return
	}
	metrics.CPUPercent = parseDockerPercent(stats.CPUPerc)
	metrics.MemoryPercent = parseDockerPercent(stats.MemPerc)
	if used, limit, ok := parseDockerPair(stats.MemUsage); ok {
		metrics.MemoryUsedBytes, metrics.MemoryLimitBytes = used, limit
	}
	if rx, tx, ok := parseDockerPair(stats.NetIO); ok {
		metrics.NetworkRXBytes, metrics.NetworkTXBytes = rx, tx
	}
	if read, write, ok := parseDockerPair(stats.BlockIO); ok {
		metrics.BlockReadBytes, metrics.BlockWriteBytes = read, write
	}
	if pids, parseErr := strconv.Atoi(strings.TrimSpace(stats.PIDs)); parseErr == nil {
		metrics.PIDs = pids
	}
}

func (s *Server) sampleHostMetrics(ctx context.Context, metrics *RuntimeMetrics) {
	if output, _, err := s.run(ctx, 5*time.Second, "nproc"); err == nil {
		if cpus, parseErr := strconv.Atoi(strings.TrimSpace(output)); parseErr == nil {
			metrics.HostCPUs = cpus
		}
	}
	if output, _, err := s.run(ctx, 5*time.Second, "cat", "/proc/loadavg"); err == nil {
		fields := strings.Fields(output)
		if len(fields) >= 3 {
			metrics.HostLoad1, _ = strconv.ParseFloat(fields[0], 64)
			metrics.HostLoad5, _ = strconv.ParseFloat(fields[1], 64)
			metrics.HostLoad15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if output, _, err := s.run(ctx, 5*time.Second, "awk", "/MemTotal|MemAvailable/ {print $1\" \"$2}", "/proc/meminfo"); err == nil {
		values := map[string]uint64{}
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if value, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
				values[strings.TrimSuffix(fields[0], ":")] = value * 1024
			}
		}
		metrics.HostMemoryTotal = values["MemTotal"]
		available := values["MemAvailable"]
		if metrics.HostMemoryTotal >= available {
			metrics.HostMemoryUsed = metrics.HostMemoryTotal - available
		}
		metrics.HostMemoryPercent = percent(metrics.HostMemoryUsed, metrics.HostMemoryTotal)
	}
	if output, _, err := s.run(ctx, 5*time.Second, "cat", "/proc/uptime"); err == nil {
		fields := strings.Fields(output)
		if len(fields) > 0 {
			if seconds, parseErr := strconv.ParseFloat(fields[0], 64); parseErr == nil && seconds >= 0 {
				metrics.UptimeSeconds = uint64(seconds)
			}
		}
	}
	fillDisk := func(path string, total, used *uint64, pct *float64) {
		output, _, err := s.run(ctx, 8*time.Second, "df", "-Pk", path)
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) < 2 {
			return
		}
		fields := strings.Fields(lines[len(lines)-1])
		if len(fields) < 3 {
			return
		}
		blocks, blocksErr := strconv.ParseUint(fields[1], 10, 64)
		usedBlocks, usedErr := strconv.ParseUint(fields[2], 10, 64)
		if blocksErr != nil || usedErr != nil {
			return
		}
		*total = blocks * 1024
		*used = usedBlocks * 1024
		*pct = percent(*used, *total)
	}
	fillDisk("/", &metrics.DiskTotalBytes, &metrics.DiskUsedBytes, &metrics.DiskPercent)
	fillDisk("/var/lib/a8m-office", &metrics.DataDiskTotalBytes, &metrics.DataDiskUsedBytes, &metrics.DataDiskPercent)
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func parseDockerPercent(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
	parsed, _ := strconv.ParseFloat(value, 64)
	return parsed
}

func parseDockerPair(value string) (uint64, uint64, bool) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	first, firstErr := parseDockerBytes(parts[0])
	second, secondErr := parseDockerBytes(parts[1])
	if firstErr != nil || secondErr != nil {
		return 0, 0, false
	}
	return first, second, true
}

func parseDockerBytes(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	index := 0
	for index < len(value) && ((value[index] >= '0' && value[index] <= '9') || value[index] == '.' || value[index] == '-') {
		index++
	}
	number, err := strconv.ParseFloat(value[:index], 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	unit := strings.TrimSpace(value[index:])
	multiplier := float64(1)
	switch strings.ToLower(unit) {
	case "", "b":
		multiplier = 1
	case "kb":
		multiplier = 1000
	case "kib":
		multiplier = 1024
	case "mb":
		multiplier = 1000 * 1000
	case "mib":
		multiplier = 1024 * 1024
	case "gb":
		multiplier = 1000 * 1000 * 1000
	case "gib":
		multiplier = 1024 * 1024 * 1024
	case "tb":
		multiplier = 1000 * 1000 * 1000 * 1000
	case "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit %q", unit)
	}
	return uint64(number * multiplier), nil
}
