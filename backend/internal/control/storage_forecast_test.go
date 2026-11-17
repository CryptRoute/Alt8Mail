package control

import (
	"math"
	"testing"
	"time"
)

func TestStorageRobustDailyGrowth(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	linear := make([]storageForecastSample, 0, 10)
	for i := 0; i < 10; i++ {
		linear = append(linear, storageForecastSample{
			at: base.Add(time.Duration(i) * 24 * time.Hour), used: uint64(500 + i*10),
		})
	}
	if got := storageRobustDailyGrowth(linear); math.Abs(got-10) > 0.001 {
		t.Fatalf("linear growth = %v, want 10", got)
	}
	// A single backup/restore spike must not become the projected trend.
	outlier := append([]storageForecastSample{}, linear...)
	outlier = append(outlier, storageForecastSample{
		at: base.Add(10 * 24 * time.Hour), used: 5000,
	})
	if got := storageRobustDailyGrowth(outlier); got > 50 {
		t.Fatalf("outlier growth = %v, want a robust value near 10", got)
	}
	flat := []storageForecastSample{
		{at: base, used: 500},
		{at: base.Add(24 * time.Hour), used: 500},
		{at: base.Add(48 * time.Hour), used: 500},
	}
	if got := storageRobustDailyGrowth(flat); got != 0 {
		t.Fatalf("flat growth = %v, want 0", got)
	}
	step := []storageForecastSample{
		{at: base, used: 18_000},
		{at: base.Add(24 * time.Hour), used: 19_000},
		{at: base.Add(48 * time.Hour), used: 23_000},
		{at: base.Add(72 * time.Hour), used: 23_000},
		{at: base.Add(96 * time.Hour), used: 23_000},
		{at: base.Add(120 * time.Hour), used: 23_000},
		{at: base.Add(144 * time.Hour), used: 23_000},
	}
	if got := storageRobustDailyGrowth(step); got != 0 {
		t.Fatalf("one-step growth = %v, want 0", got)
	}
}
