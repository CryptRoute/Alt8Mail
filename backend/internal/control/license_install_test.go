package control

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readRepositoryScript(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// TestInstallScriptPreservesLicenseSettings guards the installer's license
// resolution logic. The tenant pin is security-relevant: a reinstall that
// dropped it would silently accept a global/wrong-tenant token. The fixture
// evaluates the exact assignment lines from install.sh with representative
// preserved/env inputs, so regressions in precedence fail here without running
// the root-only installer.
func TestInstallScriptPreservesLicenseSettings(t *testing.T) {
	source := readRepositoryScript(t, "install.sh")
	assignments := []string{
		`CONTROL_LICENSE_FILE="${PRESERVE_CONTROL_LICENSE_FILE:-${PRESERVE_ALT8MAIL_LICENSE_FILE:-$LICENSE_FILE}}"`,
		`APP_LICENSE_FILE="${PRESERVE_ALT8MAIL_LICENSE_FILE:-$CONTROL_LICENSE_FILE}"`,
		`APP_LICENSE_TENANT="${LICENSE_TENANT:-$PRESERVE_ALT8MAIL_LICENSE_TENANT}"`,
	}
	for _, line := range assignments {
		if !strings.Contains(source, line) {
			t.Fatalf("install.sh is missing the license assignment %q", line)
		}
	}
	for _, want := range []string{
		`LICENSE_TENANT="${A8M_LICENSE_TENANT:-}"`,
		`PRESERVE_ALT8MAIL_LICENSE_TENANT="$(env_file_value ALT8MAIL_LICENSE_TENANT)"`,
		`restore_env_file_value ALT8MAIL_LICENSE_TENANT "$APP_LICENSE_TENANT"`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("install.sh is missing the tenant preservation line %q", want)
		}
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("installer fixture requires bash")
	}

	cases := []struct {
		name        string
		setup       string
		wantControl string
		wantApp     string
		wantTenant  string
	}{
		{
			name: "fresh install provisions the shared default path",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_TENANT=
PRESERVE_CONTROL_LICENSE_FILE=
PRESERVE_ALT8MAIL_LICENSE_FILE=
PRESERVE_ALT8MAIL_LICENSE_TENANT=`,
			wantControl: "/etc/a8m/license.token",
			wantApp:     "/etc/a8m/license.token",
			wantTenant:  "",
		},
		{
			name: "control is seeded from the webmail file",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_TENANT=
PRESERVE_CONTROL_LICENSE_FILE=
PRESERVE_ALT8MAIL_LICENSE_FILE=/srv/a8m/license.token
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-a`,
			wantControl: "/srv/a8m/license.token",
			wantApp:     "/srv/a8m/license.token",
			wantTenant:  "tenant-a",
		},
		{
			name: "reinstall preserves explicit control and webmail values",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_TENANT=
PRESERVE_CONTROL_LICENSE_FILE=/etc/a8m/custom.token
PRESERVE_ALT8MAIL_LICENSE_FILE=/srv/other.token
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-a`,
			wantControl: "/etc/a8m/custom.token",
			wantApp:     "/srv/other.token",
			wantTenant:  "tenant-a",
		},
		{
			name: "explicit initial tenant overrides the preserved pin",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_TENANT=tenant-new
PRESERVE_CONTROL_LICENSE_FILE=
PRESERVE_ALT8MAIL_LICENSE_FILE=
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-old`,
			wantControl: "/etc/a8m/license.token",
			wantApp:     "/etc/a8m/license.token",
			wantTenant:  "tenant-new",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Use a unit separator so an empty tenant pin remains a field.
			script := "set -euo pipefail\n" + testCase.setup + "\n" +
				strings.Join(assignments, "\n") +
				"\nprintf '%s\\x1f%s\\x1f%s' \"$CONTROL_LICENSE_FILE\" \"$APP_LICENSE_FILE\" \"$APP_LICENSE_TENANT\""
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture failed: %v: %s", err, output)
			}
			got := strings.Split(strings.TrimSpace(string(output)), "\x1f")
			if len(got) != 3 {
				t.Fatalf("fixture output = %q", output)
			}
			if got[0] != testCase.wantControl || got[1] != testCase.wantApp || got[2] != testCase.wantTenant {
				t.Fatalf("resolved control=%q app=%q tenant=%q; want %q %q %q",
					got[0], got[1], got[2], testCase.wantControl, testCase.wantApp, testCase.wantTenant)
			}
		})
	}
}

// TestUpdateScriptReplacesEmptyLicenseValues ensures the updater matches
// install.sh: an existing empty KEY= line is treated as unset and replaced by
// the fallback instead of blocking the seed.
func TestUpdateScriptReplacesEmptyLicenseValues(t *testing.T) {
	source := readRepositoryScript(t, filepath.Join("scripts", "alt8mail-update.sh"))
	for _, want := range []string{
		`grep -qE "^${key}=.+"`,
		`sed -i "/^${key}=$/d"`,
		`set_env_if_missing "$CONTROL_ENV_FILE" CONTROL_LICENSE_FILE "$shared_license_file"`,
		`set_env_if_missing "$APP_ENV_FILE" ALT8MAIL_LICENSE_FILE "$shared_license_file"`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("alt8mail-update.sh is missing %q", want)
		}
	}
}
