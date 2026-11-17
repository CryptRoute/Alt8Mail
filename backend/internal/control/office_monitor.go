package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type officeRuntimeMetrics struct {
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

type officeMonitorPayload struct {
	Enabled        bool                  `json:"enabled"`
	DeploymentMode string                `json:"deploymentMode"`
	PublicURL      string                `json:"publicUrl"`
	Metrics        *officeRuntimeMetrics `json:"metrics,omitempty"`
	Error          string                `json:"error,omitempty"`
	SampledAt      string                `json:"sampledAt,omitempty"`
}

func (s *Server) officeMonitor(ctx context.Context, settings officeSettings, timeout time.Duration) officeMonitorPayload {
	payload := officeMonitorPayload{
		Enabled:        settings.MonitoringEnabled,
		DeploymentMode: settings.DeploymentMode,
		PublicURL:      settings.PublicURL,
	}
	if !settings.MonitoringEnabled {
		return payload
	}
	monitorCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, status, err := s.officeAgentRaw(monitorCtx, http.MethodGet, "/metrics", nil, timeout)
	if err != nil {
		payload.Error = err.Error()
		return payload
	}
	if status != http.StatusOK {
		payload.Error = "Office agent metrics returned HTTP " + http.StatusText(status)
		return payload
	}
	var metrics officeRuntimeMetrics
	if err := json.Unmarshal(raw, &metrics); err != nil {
		payload.Error = "Office agent returned unreadable metrics"
		return payload
	}
	payload.Metrics = &metrics
	payload.SampledAt = metrics.CheckedAt
	return payload
}

func (s *Server) handleOfficeMetrics(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	settings, err := s.officeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not load office settings")
		return
	}
	writeJSON(w, http.StatusOK, s.officeMonitor(r.Context(), settings, 10*time.Second))
}

func (s *Server) officeMonitoringEnabled(ctx context.Context) (officeSettings, bool) {
	settings, err := s.officeSettings(ctx)
	if err != nil || !settings.MonitoringEnabled {
		return settings, false
	}
	running := settings.Status == "running" || settings.ContainerStatus == "running"
	return settings, running && strings.TrimSpace(settings.PublicURL) != ""
}
