package config

import (
	"strings"
	"testing"
)

func TestLoadLicenseTrustAndPortalSettings(t *testing.T) {
	t.Setenv("ALT8MAIL_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("ALT8MAIL_CSRF_SECRET", strings.Repeat("c", 32))
	t.Setenv("ALT8MAIL_LICENSE_PORTAL_URL", "https://license.example/")
	t.Setenv("ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LicensePortalURL != "https://license.example" {
		t.Fatalf("LicensePortalURL = %q", cfg.LicensePortalURL)
	}
	if !cfg.LicenseAllowConfigKey {
		t.Fatal("LicenseAllowConfigKey should be true")
	}
	t.Setenv("ALT8MAIL_LICENSE_ALLOW_CONFIG_KEY", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LicenseAllowConfigKey {
		t.Fatal("LicenseAllowConfigKey should be false")
	}
}
