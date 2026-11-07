package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficeMonitorReadsAgentMetrics(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "office-agent-token")
	if err := os.WriteFile(tokenFile, []byte("test-agent-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-agent-token" {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"checkedAt":"2026-11-07T12:00:00Z","container":"a8m-office","running":true,"cpuPercent":12.5,"memoryPercent":25,"memoryUsedBytes":1048576,"memoryLimitBytes":4194304,"hostCpus":4,"diskPercent":40,"uptimeSeconds":3600}`))
	}))
	defer agent.Close()
	server := &Server{cfg: Config{OfficeAgentURL: agent.URL, OfficeAgentToken: tokenFile}}
	payload := server.officeMonitor(context.Background(), officeSettings{
		MonitoringEnabled: true, DeploymentMode: "local", PublicURL: "https://office.example.com",
	}, 2*time.Second)
	if !payload.Enabled || payload.Error != "" || payload.Metrics == nil {
		t.Fatalf("unexpected monitor payload: %#v", payload)
	}
	if payload.Metrics.CPUPercent != 12.5 || payload.Metrics.MemoryPercent != 25 || payload.Metrics.HostCPUs != 4 {
		t.Fatalf("unexpected metrics: %#v", payload.Metrics)
	}
	disabled := server.officeMonitor(context.Background(), officeSettings{MonitoringEnabled: false}, time.Second)
	if disabled.Enabled || disabled.Metrics != nil || disabled.Error != "" {
		t.Fatalf("disabled monitoring should not sample: %#v", disabled)
	}
}
