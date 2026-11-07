package officeagent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDockerBytesAndPairs(t *testing.T) {
	cases := map[string]uint64{
		"0B": 0, "1kB": 1000, "1KiB": 1024, "1.5MiB": 1572864,
		"2GB": 2000000000, "1GiB": 1073741824,
	}
	for input, expected := range cases {
		value, err := parseDockerBytes(input)
		if err != nil || value != expected {
			t.Fatalf("parseDockerBytes(%q) = %d, %v; want %d", input, value, err, expected)
		}
	}
	if _, err := parseDockerBytes("many bytes"); err == nil {
		t.Fatal("invalid size was accepted")
	}
	first, second, ok := parseDockerPair("123MiB / 1GiB")
	if !ok || first != 128974848 || second != 1073741824 {
		t.Fatalf("parseDockerPair = %d, %d, %v", first, second, ok)
	}
	if got := parseDockerPercent("4.25%"); got != 4.25 {
		t.Fatalf("parseDockerPercent = %v", got)
	}
}

func TestRuntimeMetricsSamplesContainerAndHost(t *testing.T) {
	server, err := New(Config{
		TokenFile:     filepath.Join(t.TempDir(), "token"),
		SecretFile:    filepath.Join(t.TempDir(), "secret"),
		StateFile:     filepath.Join(t.TempDir(), "state.json"),
		ContainerName: "a8m-office",
		Runner: func(_ context.Context, _ time.Duration, name string, args ...string) (string, string, error) {
			joined := name + " " + strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "inspect"):
				return "running\n", "", nil
			case strings.Contains(joined, "stats"):
				return `{"BlockIO":"1MB / 2MB","CPUPerc":"12.5%","MemPerc":"25.0%","MemUsage":"256MiB / 1GiB","NetIO":"3MB / 4MB","PIDs":"17"}` + "\n", "", nil
			case strings.Contains(joined, "nproc"):
				return "4\n", "", nil
			case strings.Contains(joined, "/proc/loadavg"):
				return "0.50 0.40 0.30 1/100 1234\n", "", nil
			case strings.Contains(joined, "/proc/meminfo"):
				return "MemTotal: 4194304\nMemAvailable: 1048576\n", "", nil
			case strings.Contains(joined, "/proc/uptime"):
				return "123456.00 0.00\n", "", nil
			case strings.Contains(joined, "df -Pk /var/lib/a8m-office"):
				return "Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/sda1 1048576 524288 524288 50% /var/lib/a8m-office\n", "", nil
			case strings.Contains(joined, "df -Pk /"):
				return "Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/sda1 2097152 1048576 1048576 50% /\n", "", nil
			}
			return "", "", nil
		},
		Health: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.saveState(state{Image: "onlyoffice/documentserver:9.4.0.1"}); err != nil {
		t.Fatal(err)
	}
	metrics := server.runtimeMetrics(context.Background())
	if !metrics.Running || metrics.ContainerStatus != "running" || metrics.CPUPercent != 12.5 ||
		metrics.MemoryUsedBytes != 256*1024*1024 || metrics.MemoryPercent != 25 ||
		metrics.NetworkRXBytes != 3_000_000 || metrics.PIDs != 17 {
		t.Fatalf("container metrics = %#v", metrics)
	}
	if metrics.HostCPUs != 4 || metrics.HostMemoryTotal != 4<<30 || metrics.HostMemoryUsed != 3<<30 ||
		metrics.UptimeSeconds != 123456 || metrics.DiskPercent != 50 || metrics.DataDiskPercent != 50 {
		t.Fatalf("host metrics = %#v", metrics)
	}
}
