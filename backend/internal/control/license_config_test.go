package control

import "testing"

func TestControlLicenseTrustAndPortalConfig(t *testing.T) {
	licenseTestEnvironment(t)
	t.Setenv("CONTROL_LICENSE_PORTAL_URL", "https://license.example/")
	t.Setenv("CONTROL_LICENSE_ALLOW_CONFIG_KEY", "true")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicensePortalURL != "https://license.example" || !cfg.LicenseAllowConfigKey {
		t.Fatalf("unexpected trust/portal config: %#v", cfg)
	}
	t.Setenv("CONTROL_LICENSE_ALLOW_CONFIG_KEY", "false")
	t.Setenv("CONTROL_LICENSE_PORTAL_URL", "")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LicenseAllowConfigKey || cfg.LicensePortalURL != "" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}
