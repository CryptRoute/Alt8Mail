package control

import "testing"

func TestParseAptUpgradable(t *testing.T) {
	output := `Listing...
bash/bookworm-security 5.2.15-2+b7 amd64 [upgradable from: 5.2.15-2+b6]
curl/bookworm 8.1.0-1 amd64 [upgradable from: 8.0.0-1]
libc6/bookworm-security,now 2.36-9+deb12u9 amd64 [installed,upgradable from: 2.36-9+deb12u8]
WARNING: apt does not have a stable CLI interface yet. Use with caution in scripts.
`
	packages := parseAptUpgradable(output)
	if len(packages) != 3 {
		t.Fatalf("expected 3 packages, got %d: %#v", len(packages), packages)
	}
	// Security packages sort before standard packages.
	if packages[0].Name != "bash" || packages[1].Name != "libc6" || packages[2].Name != "curl" {
		t.Fatalf("unexpected package order: %#v", []string{packages[0].Name, packages[1].Name, packages[2].Name})
	}
	if !packages[0].Security || packages[0].Candidate != "5.2.15-2+b7" || packages[0].Current != "5.2.15-2+b6" || packages[0].Arch != "amd64" {
		t.Fatalf("security package was parsed incorrectly: %#v", packages[0])
	}
	if packages[2].Security {
		t.Fatalf("standard repository package was marked security: %#v", packages[2])
	}
}

func TestParseAptUpgradableDeduplicates(t *testing.T) {
	packages := parseAptUpgradable("bash/bookworm 1 amd64 [upgradable from: 0]\nbash/bookworm 1 amd64 [upgradable from: 0]\n")
	if len(packages) != 1 {
		t.Fatalf("expected one package, got %d", len(packages))
	}
}

func TestPackageNamePattern(t *testing.T) {
	for _, valid := range []string{"bash", "libc6", "linux-image-6.1.0-13-amd64", "postgresql-15-main", "team+extra"} {
		if !packageNamePattern.MatchString(valid) {
			t.Errorf("expected %q to be a valid package name", valid)
		}
	}
	for _, invalid := range []string{"", "pkg;reboot", "$(id)", "../etc/passwd", "pkg name", "postgresql@15-main"} {
		if packageNamePattern.MatchString(invalid) {
			t.Errorf("expected %q to be rejected", invalid)
		}
	}
}

func TestReversePrimaryOrder(t *testing.T) {
	if got := reversePrimaryOrder("m.email ASC, m.id ASC"); got != "m.email DESC, m.id ASC" {
		t.Fatalf("unexpected reverse: %q", got)
	}
	if got := reversePrimaryOrder("u.used_bytes DESC, m.email ASC"); got != "u.used_bytes ASC, m.email ASC" {
		t.Fatalf("unexpected reverse: %q", got)
	}
	if got := reversePrimaryOrder("u.last_login_at DESC NULLS LAST, m.email ASC"); got != "u.last_login_at ASC NULLS LAST, m.email ASC" {
		t.Fatalf("unexpected NULLS reverse: %q", got)
	}
}
