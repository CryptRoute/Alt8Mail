package control

import (
	"os"
	"strings"
	"testing"
)

func TestStorageControlWebAssets(t *testing.T) {
	view, err := webFiles.ReadFile("web/views-storage.js")
	if err != nil {
		t.Fatalf("storage view is not embedded: %v", err)
	}
	source := string(view)
	for _, want := range []string{
		"export async function renderStorage(ctx)",
		"Disk Management",
		"storage.manage",
		"Move mail",
		"Storage operations",
		"Apply managed query",
		"Existing mail does not move",
		"control/api/storage/overview",
		"control/api/storage/moves/plan",
		"Host provisioning",
		"Add disk",
		"Wipe",
		"control/api/storage/volumes/plan",
		"Capacity outlook",
		"Recommended rebalance",
		"forecastAlertDays",
		"control/api/storage/usage",
		"storage-volume-filter",
		"storage-drive-card",
		"openVolumeDetails",
		"storage-detail-grid",
		"storage-policy-form",
		"Review and save",
		"Select all matching",
		"storage-picker-search",
		"data-op-sort",
		"control/api/storage/mailboxes?",
		"Preview domain plan",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("storage view is missing %q", want)
		}
	}

	app, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("app.js is not embedded: %v", err)
	}
	appSource := string(app)
	for _, want := range []string{
		"import { renderStorage } from './views-storage.js",
		"href: '#/storage'",
		"perm: 'storage.read'",
		"storage: { title: 'Disk Management', crumb: 'Infrastructure', nav: 'storage', perm: 'storage.read', render: renderStorage }",
	} {
		if !strings.Contains(appSource, want) {
			t.Fatalf("app.js is missing storage wiring %q", want)
		}
	}
}

func TestStorageDovecotTemplateIsHomeAware(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/dovecot/dovecot-sql.conf.ext")
	if err != nil {
		t.Skipf("dovecot template is outside the module test working directory: %v", err)
	}
	if !strings.Contains(string(raw), "mail_home") || !strings.Contains(string(raw), "COALESCE(NULLIF(m.mail_home") {
		t.Fatal("Dovecot user_query template does not read mail_home with the legacy fallback")
	}
}
