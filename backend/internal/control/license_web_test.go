package control

import (
	"strings"
	"testing"
)

// TestLicenseControlWebAssets is a lightweight render contract for the embedded
// Control UI. The module is plain ES-module markup rather than a Go template, so
// this asserts the route wiring and the operator-facing requirements the static
// asset must expose (accessible meter, restart instruction, no token round-trip).
func TestLicenseControlWebAssets(t *testing.T) {
	view, err := webFiles.ReadFile("web/views-license.js")
	if err != nil {
		t.Fatalf("license view is not embedded: %v", err)
	}
	source := string(view)
	for _, want := range []string{
		"export async function renderLicense(ctx)",
		`role="meter"`,
		"aria-valuenow",
		"aria-valuemin",
		"aria-valuemax",
		"aria-valuetext",
		"Restart the Alt8Mail webmail service to activate this license",
		"restartRequired",
		"CONTROL_LICENSE_FILE",
		"LICENSE_NOT_CONFIGURED",
		"LICENSE_TOKEN",
		"LICENSE_EXPIRED",
		"LICENSE_TOO_LARGE",
		"CONTENT_TYPE",
		"LICENSE_WRITE",
		"BAD_JSON",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("license view is missing %q", want)
		}
	}

	app, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("app.js is not embedded: %v", err)
	}
	appSource := string(app)
	for _, want := range []string{
		"import { renderLicense } from './views-license.js'",
		"href: '#/license'",
		"perm: 'settings.write'",
		"license: { title: 'License', crumb: 'Infrastructure', nav: 'license', perm: 'settings.write', render: renderLicense }",
	} {
		if !strings.Contains(appSource, want) {
			t.Fatalf("app.js is missing license wiring %q", want)
		}
	}
}
