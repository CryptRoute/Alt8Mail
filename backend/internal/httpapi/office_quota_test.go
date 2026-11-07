package httpapi

import "testing"

func TestRetentionPrunedBytes(t *testing.T) {
	sizes := []int64{10, 20, 30}
	cases := []struct {
		retention int
		want      int64
	}{
		{0, 0},
		{1, 60},
		{2, 50},
		{3, 30},
		{4, 0},
	}
	for _, test := range cases {
		if got := retentionPrunedBytes(sizes, test.retention); got != test.want {
			t.Fatalf("retention %d = %d, want %d", test.retention, got, test.want)
		}
	}
}
