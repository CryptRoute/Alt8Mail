package config

import "testing"

func TestDefaultMemoryLimitMB(t *testing.T) {
	cases := []struct {
		total int
		want  int
	}{
		{0, 0},
		{1024, 384},
		{2048, 682},
		{4096, 1365},
		{16384, 2048},
	}
	for _, tc := range cases {
		if got := defaultMemoryLimitMB(tc.total); got != tc.want {
			t.Fatalf("defaultMemoryLimitMB(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

func TestApplyHostDefaultsCapsSmallHosts(t *testing.T) {
	cfg := Config{MailWorkerConcurrency: 8, DBMaxConns: 20}
	applyHostDefaultsAt(&cfg, 2048)
	if cfg.MailWorkerConcurrency != 2 || cfg.DBMaxConns != 8 {
		t.Fatalf("2 GB host was not capped: %#v", cfg)
	}
	cfg = Config{MailWorkerConcurrency: 8, DBMaxConns: 20}
	applyHostDefaultsAt(&cfg, 4096)
	if cfg.MailWorkerConcurrency != 4 || cfg.DBMaxConns != 12 {
		t.Fatalf("4 GB host was not capped: %#v", cfg)
	}
	cfg = Config{MailWorkerConcurrency: 8, DBMaxConns: 20}
	applyHostDefaultsAt(&cfg, 16384)
	if cfg.MailWorkerConcurrency != 8 || cfg.DBMaxConns != 20 {
		t.Fatalf("large host should keep configured values: %#v", cfg)
	}
}
