package config

import (
	"strings"
	"testing"
)

func TestLoadLicenseTenant(t *testing.T) {
	// The loader requires session secrets and skips demo mode by default.
	t.Setenv("ALT8MAIL_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("ALT8MAIL_CSRF_SECRET", strings.Repeat("c", 32))
	t.Setenv("ALT8MAIL_LICENSE_TENANT", "  tenant-a  ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LicenseTenant != "tenant-a" {
		t.Fatalf("LicenseTenant = %q, want tenant-a", cfg.LicenseTenant)
	}

	t.Setenv("ALT8MAIL_LICENSE_TENANT", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load without tenant: %v", err)
	}
	if cfg.LicenseTenant != "" {
		t.Fatalf("LicenseTenant = %q, want empty", cfg.LicenseTenant)
	}
}
