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

// licenseEnvResolutionBlock extracts the real assignment block from install.sh
// so the fixture below executes the shipped logic instead of a copy.
func licenseEnvResolutionBlock(t *testing.T, source string) string {
	t.Helper()
	const begin = "# BEGIN LICENSE ENV RESOLUTION"
	const end = "# END LICENSE ENV RESOLUTION"
	start := strings.Index(source, begin)
	stop := strings.Index(source, end)
	if start < 0 || stop < 0 || stop <= start {
		t.Fatalf("install.sh does not contain the license env resolution markers")
	}
	start += len(begin)
	// Skip the remainder of the marker comment line.
	if newline := strings.IndexByte(source[start:], '\n'); newline >= 0 {
		start += newline + 1
	}
	return source[start:stop]
}

// TestInstallScriptPreservesLicenseSettings guards the installer's license
// resolution logic. The tenant pin is security-relevant: a reinstall that
// dropped it would silently accept a global/wrong-tenant token. A first-time
// install has no app.env, so the block must also survive all webmail preserve
// variables being unset under `set -u`; the fresh case below proves that with
// the exact shipped lines and asserts the shared default path and empty
// key/tenant.
func TestInstallScriptPreservesLicenseSettings(t *testing.T) {
	source := readRepositoryScript(t, "install.sh")
	for _, want := range []string{
		`PRESERVE_ALT8MAIL_LICENSE_FILE=""`,
		`PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY=""`,
		`PRESERVE_ALT8MAIL_LICENSE_TENANT=""`,
		`PRESERVE_ALT8MAIL_LICENSE_TENANT="$(env_file_value ALT8MAIL_LICENSE_TENANT)"`,
		`restore_env_file_value ALT8MAIL_LICENSE_TENANT "$APP_LICENSE_TENANT"`,
		`restore_env_file_value ALT8MAIL_LICENSE_FILE "$APP_LICENSE_FILE"`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("install.sh is missing the license preservation line %q", want)
		}
	}
	block := licenseEnvResolutionBlock(t, source)
	for _, want := range []string{
		`CONTROL_LICENSE_PUBLIC_KEY="${LICENSE_PUBLIC_KEY:-${PRESERVE_CONTROL_LICENSE_PUBLIC_KEY:-${PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY:-}}}"`,
		`APP_LICENSE_TENANT="${LICENSE_TENANT:-${PRESERVE_ALT8MAIL_LICENSE_TENANT:-}}"`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("license env resolution block is missing safe expansion %q", want)
		}
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("installer fixture requires bash")
	}

	cases := []struct {
		name  string
		setup string
		want  [5]string // control file, control key, app file, app key, app tenant
	}{
		{
			name: "fresh install with no app.env and preserve variables unset",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_PUBLIC_KEY=
LICENSE_GROUP=a8m
LICENSE_TENANT=`,
			want: [5]string{
				"/etc/a8m/license.token", "", "/etc/a8m/license.token", "", "",
			},
		},
		{
			name: "control is seeded from the webmail file, key and tenant",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_PUBLIC_KEY=
LICENSE_GROUP=a8m
LICENSE_TENANT=
PRESERVE_ALT8MAIL_LICENSE_FILE=/srv/a8m/license.token
PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY=webmail-key
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-a`,
			want: [5]string{
				"/srv/a8m/license.token", "webmail-key", "/srv/a8m/license.token", "webmail-key", "tenant-a",
			},
		},
		{
			name: "reinstall preserves explicit control and webmail values",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_PUBLIC_KEY=
LICENSE_GROUP=a8m
LICENSE_TENANT=
PRESERVE_CONTROL_LICENSE_FILE=/etc/a8m/custom.token
PRESERVE_CONTROL_LICENSE_PUBLIC_KEY=control-key
PRESERVE_ALT8MAIL_LICENSE_FILE=/srv/other.token
PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY=webmail-key
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-a`,
			want: [5]string{
				"/etc/a8m/custom.token", "control-key", "/srv/other.token", "webmail-key", "tenant-a",
			},
		},
		{
			name: "explicit initial key and tenant override the preserved values",
			setup: `LICENSE_FILE=/etc/a8m/license.token
LICENSE_PUBLIC_KEY=initial-key
LICENSE_GROUP=a8m
LICENSE_TENANT=tenant-new
PRESERVE_ALT8MAIL_LICENSE_PUBLIC_KEY=old-key
PRESERVE_ALT8MAIL_LICENSE_TENANT=tenant-old`,
			want: [5]string{
				"/etc/a8m/license.token", "initial-key", "/etc/a8m/license.token", "initial-key", "tenant-new",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Use a unit separator so empty fields remain distinct.
			script := "set -euo pipefail\n" + testCase.setup + "\n" + block +
				"\nprintf '%s\\x1f%s\\x1f%s\\x1f%s\\x1f%s' \"$CONTROL_LICENSE_FILE\" \"$CONTROL_LICENSE_PUBLIC_KEY\" \"$APP_LICENSE_FILE\" \"$APP_LICENSE_PUBLIC_KEY\" \"$APP_LICENSE_TENANT\""
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture aborted (unset variable?): %v: %s", err, output)
			}
			got := strings.Split(strings.TrimSpace(string(output)), "\x1f")
			if len(got) != len(testCase.want) {
				t.Fatalf("fixture output = %q", output)
			}
			for i := range testCase.want {
				if got[i] != testCase.want[i] {
					t.Fatalf("field %d = %q, want %q (all: %q)", i, got[i], testCase.want[i], got)
				}
			}
		})
	}
}

// TestUpdateScriptSeedsEmptyLicenseValues ensures the updater matches
// install.sh: an existing empty KEY= line is treated as unset and replaced by
// the fallback, while a non-empty value is never overwritten.
func TestUpdateScriptSeedsEmptyLicenseValues(t *testing.T) {
	source := readRepositoryScript(t, filepath.Join("scripts", "alt8mail-update.sh"))
	for _, want := range []string{
		`set_env_if_missing "$CONTROL_ENV_FILE" CONTROL_LICENSE_FILE "$shared_license_file"`,
		`set_env_if_missing "$APP_ENV_FILE" ALT8MAIL_LICENSE_FILE "$shared_license_file"`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("alt8mail-update.sh is missing %q", want)
		}
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("updater fixture requires bash")
	}

	const marker = "set_env_if_missing() {"
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("alt8mail-update.sh does not define %s", marker)
	}
	rest := source[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatalf("could not extract set_env_if_missing")
	}
	function := rest[:end+2]

	file := filepath.Join(t.TempDir(), "app.env")
	if err := os.WriteFile(file, []byte("CONTROL_LICENSE_FILE=\nALT8MAIL_LICENSE_FILE=/keep/this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\nlog() { :; }\n" + function +
		"\nset_env_if_missing \"$1\" CONTROL_LICENSE_FILE /etc/a8m/license.token" +
		"\nset_env_if_missing \"$1\" ALT8MAIL_LICENSE_FILE /ignored/fallback\ncat \"$1\""
	output, err := exec.Command("bash", "-c", script, "bash", file).CombinedOutput()
	if err != nil {
		t.Fatalf("updater fixture failed: %v: %s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	counts := map[string]int{}
	for _, line := range lines {
		counts[line]++
	}
	if counts["CONTROL_LICENSE_FILE=/etc/a8m/license.token"] != 1 {
		t.Fatalf("empty value was not replaced exactly once: %q", output)
	}
	if counts["ALT8MAIL_LICENSE_FILE=/keep/this"] != 1 {
		t.Fatalf("existing non-empty value was overwritten: %q", output)
	}
	for line := range counts {
		if line == "" || line == "CONTROL_LICENSE_FILE=" || line == "ALT8MAIL_LICENSE_FILE=/ignored/fallback" {
			t.Fatalf("unexpected line %q in %q", line, output)
		}
	}
}
