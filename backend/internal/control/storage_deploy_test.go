package control

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStorageSandboxPathIsOptionalForUpgrades(t *testing.T) {
	guard := regexp.MustCompile(`(^|\s)/srv/a8m-storage`)
	for _, service := range []string{"alt8mail-control.service", "a8m-mailprovision.service"} {
		raw, err := os.ReadFile("../../../deploy/systemd/" + service)
		if err != nil {
			t.Skipf("systemd unit is outside the module test working directory: %v", err)
		}
		text := string(raw)
		if !strings.Contains(text, "-/srv/a8m-storage") {
			t.Fatalf("%s must make the managed storage root optional in ReadWritePaths", service)
		}
		if guard.MatchString(text) {
			t.Fatalf("%s has an unguarded /srv/a8m-storage sandbox path", service)
		}
	}
}

func TestControlUnitCreatesStorageRootInsideSandbox(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/systemd/alt8mail-control.service")
	if err != nil {
		t.Skipf("systemd unit is outside the module test working directory: %v", err)
	}
	if !strings.Contains(string(raw), "ExecStartPre=/opt/a8m/current/scripts/alt8mail-ensure-storage-root.sh") {
		t.Fatal("control unit does not create the managed storage root before startup")
	}
	script, err := os.ReadFile("../../../scripts/alt8mail-ensure-storage-root.sh")
	if err != nil {
		t.Skipf("storage root helper is outside the module test working directory: %v", err)
	}
	text := string(script)
	if !strings.Contains(text, "CONTROL_STORAGE_ROOT") || !strings.Contains(text, `install -d -m 0750 -o root -g vmail "$root"`) {
		t.Fatal("storage root helper does not create the configured root as root:vmail 0750")
	}
}

func TestUpdaterCreatesStorageRootBeforeRestart(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/alt8mail-update.sh")
	if err != nil {
		t.Skipf("updater is outside the module test working directory: %v", err)
	}
	text := string(raw)
	create := strings.Index(text, `install -d -m 0750 -o root -g vmail "$storage_root"`)
	restart := strings.Index(text, `systemctl restart "$svc"`)
	if create < 0 {
		t.Fatal("updater does not create the managed storage root")
	}
	if restart < 0 {
		t.Fatal("updater restart loop was not found")
	}
	if create > restart {
		t.Fatal("updater creates the storage root after restarting services")
	}
	if !strings.Contains(text, "CONTROL_STORAGE_ROOT") {
		t.Fatal("updater does not honor a configured storage root")
	}
}
