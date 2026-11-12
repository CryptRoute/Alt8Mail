package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoragePercentLevel(t *testing.T) {
	policy := storagePolicy{AdvisoryPercent: 80, WarningPercent: 85, SeriousPercent: 90, CriticalPercent: 95, EmergencyPercent: 98}
	cases := []struct {
		percent float64
		want    string
	}{
		{79.9, "ok"},
		{80, "advisory"},
		{84.9, "advisory"},
		{85, "warning"},
		{90, "serious"},
		{95, "critical"},
		{98, "emergency"},
		{100, "emergency"},
	}
	for _, tc := range cases {
		if got := storagePercentLevel(policy, tc.percent); got != tc.want {
			t.Fatalf("storagePercentLevel(%.1f) = %q, want %q", tc.percent, got, tc.want)
		}
	}
}

func TestStorageGuardDecision(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Minute)
	stale := now.Add(-10 * time.Minute)
	cases := []struct {
		name  string
		state storageGuardState
		block bool
	}{
		{"fresh critical", storageGuardState{Guard: true, Level: "critical", CheckedAt: &fresh}, true},
		{"fresh emergency", storageGuardState{Guard: true, Level: "emergency", CheckedAt: &fresh}, true},
		{"fresh warning", storageGuardState{Guard: true, Level: "warning", CheckedAt: &fresh}, false},
		{"stale critical", storageGuardState{Guard: true, Level: "critical", CheckedAt: &stale}, false},
		{"guard disabled", storageGuardState{Guard: false, Level: "critical", CheckedAt: &fresh}, false},
		{"never checked", storageGuardState{Guard: true, Level: "critical"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocked, reason := storageGuardDecision(tc.state, now)
			if blocked != tc.block {
				t.Fatalf("blocked = %v, want %v", blocked, tc.block)
			}
			if tc.block && !strings.Contains(reason, "deferred") {
				t.Fatalf("unexpected guard reason: %q", reason)
			}
		})
	}
}

func TestValidateStoragePolicy(t *testing.T) {
	valid := storagePolicy{
		AdvisoryPercent: 80, WarningPercent: 85, SeriousPercent: 90,
		CriticalPercent: 95, EmergencyPercent: 98, ReservePercent: 5,
		MinFreeBytes: 1 << 30, MigrationGraceHours: 24, MaxConcurrentMoves: 2,
		AlertRearmHours: 24, ForecastAlertDays: 30,
	}
	if err := validateStoragePolicy(valid); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	outOfOrder := valid
	outOfOrder.CriticalPercent = 86
	if err := validateStoragePolicy(outOfOrder); err == nil {
		t.Fatal("out-of-order thresholds were accepted")
	}
	badReserve := valid
	badReserve.ReservePercent = 60
	if err := validateStoragePolicy(badReserve); err == nil {
		t.Fatal("invalid reserve was accepted")
	}
}

func TestMeasureMailboxUsage(t *testing.T) {
	oldCommand := storageUsageCommand
	oldLookPath := storageLookPath
	storageLookPath = func(string) (string, error) { return "", errors.New("not installed") }
	storageUsageCommand = func(_ context.Context, _ time.Duration, name string, args ...string) (commandResult, error) {
		switch name {
		case "du":
			if len(args) < 2 || args[0] != "-sb" {
				t.Fatalf("unexpected du arguments: %v", args)
			}
			return commandResult{Stdout: "12345\t/tmp/mailbox\n"}, nil
		case "find":
			return commandResult{Stdout: "a\nb\n"}, nil
		default:
			t.Fatalf("unexpected command %q", name)
			return commandResult{}, nil
		}
	}
	t.Cleanup(func() {
		storageUsageCommand = oldCommand
		storageLookPath = oldLookPath
	})
	home := filepath.Join(t.TempDir(), "mailbox")
	server := &Server{}
	bytes, messages, err := server.measureMailboxUsage(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 12345 || messages != 4 {
		t.Fatalf("measurement = %d bytes / %d messages; want 12345 / 4", bytes, messages)
	}
}
