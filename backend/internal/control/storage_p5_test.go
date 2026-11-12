package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStorageBlockTopologyProtection(t *testing.T) {
	normalized := normalizeStorageDisk(lsblkNode{
		Name: "sdb", Path: "/dev/sdb", Type: "disk", Size: float64(100 << 30),
		Children: []lsblkNode{
			{Name: "sdb1", Path: "/dev/sdb1", Type: "part"},
			{Name: "md0", Path: "/dev/md0", Type: "raid1"},
		},
	}, "")
	if normalized.Topology != "local" || !normalized.SystemProtected || len(normalized.Children) != 2 {
		t.Fatalf("unexpected normalized disk: %+v", normalized)
	}
	if normalized.Children[1].Topology != "raid" {
		t.Fatalf("RAID child topology = %q", normalized.Children[1].Topology)
	}

	cryptDisk := normalizeStorageDisk(lsblkNode{
		Name: "sdc", Path: "/dev/sdc", Type: "disk",
		Children: []lsblkNode{{Name: "crypt0", Path: "/dev/mapper/crypt0", Type: "crypt"}},
	}, "")
	if !cryptDisk.SystemProtected || !strings.Contains(cryptDisk.TopologyReason, "LUKS") {
		t.Fatalf("LUKS child was not protected: %+v", cryptDisk)
	}
	mpathDisk := normalizeStorageDisk(lsblkNode{
		Name: "sdd", Path: "/dev/sdd", Type: "disk",
		Children: []lsblkNode{{Name: "mpatha", Path: "/dev/mapper/mpatha", Type: "mpath"}},
	}, "")
	if !mpathDisk.SystemProtected || mpathDisk.TopologyReason == "" {
		t.Fatalf("multipath child was not protected: %+v", mpathDisk)
	}
}

func TestStorageDiskHealthParsing(t *testing.T) {
	oldCommand := storageHealthCommand
	oldLookPath := storageLookPath
	oldGOOS := storageHostGOOS
	storageHostGOOS = "linux"
	storageLookPath = func(string) (string, error) { return "/usr/sbin/smartctl", nil }
	storageHealthCommand = func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
		if name != "smartctl" || len(args) == 0 {
			t.Fatalf("unexpected health command: %s %v", name, args)
		}
		switch args[len(args)-1] {
		case "/dev/sda":
			return commandResult{Stdout: `{"smart_status":{"passed":true},"temperature":{"current":34},"power_on_time":{"hours":1200},"ata_smart_attributes":{"table":[{"id":5,"raw":{"value":0}}]}}`}, nil
		case "/dev/sdb":
			return commandResult{Stdout: `{"smart_status":{"passed":false},"temperature":{"current":62}}`}, nil
		case "/dev/sdc":
			return commandResult{Stdout: `{"smart_status":{"passed":true},"ata_smart_attributes":{"table":[{"id":197,"raw":{"value":2}}]}}`}, nil
		default:
			return commandResult{Stderr: "unsupported device"}, errors.New("exit status 1")
		}
	}
	t.Cleanup(func() {
		storageHealthCommand = oldCommand
		storageLookPath = oldLookPath
		storageHostGOOS = oldGOOS
	})
	server := &Server{}
	healthy := server.storageDiskHealth(context.Background(), storageDisk{Type: "disk", Path: "/dev/sda", Serial: "A"})
	if healthy == nil || healthy.State != "ok" || healthy.TemperatureC == nil || *healthy.TemperatureC != 34 {
		t.Fatalf("healthy SMART result = %+v", healthy)
	}
	failed := server.storageDiskHealth(context.Background(), storageDisk{Type: "disk", Path: "/dev/sdb", Serial: "B"})
	if failed == nil || failed.State != "critical" {
		t.Fatalf("failed SMART result = %+v", failed)
	}
	pending := server.storageDiskHealth(context.Background(), storageDisk{Type: "disk", Path: "/dev/sdc", Serial: "C"})
	if pending == nil || pending.State != "warning" || pending.Pending == nil || *pending.Pending != 2 {
		t.Fatalf("pending-sector SMART result = %+v", pending)
	}
	unknown := server.storageDiskHealth(context.Background(), storageDisk{Type: "disk", Path: "/dev/sdd", Serial: "D"})
	if unknown == nil || unknown.State != "unknown" {
		t.Fatalf("unknown SMART result = %+v", unknown)
	}
	if got := server.storageDiskHealth(context.Background(), storageDisk{Type: "part", Path: "/dev/sda1"}); got != nil {
		t.Fatal("SMART was queried for a partition")
	}
}
