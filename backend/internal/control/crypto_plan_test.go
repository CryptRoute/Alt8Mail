package control

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCanonicalPlanHashSurvivesJSONBRoundTrip(t *testing.T) {
	plan := storageVolumePlan{
		Kind: "volume_add", Mode: "initialize", Name: "mail-lab",
		MountPath: "/srv/a8m-storage/mail-lab", DevicePath: "/dev/vdb",
		FSType: "ext4", DiskSizeBytes: 53687091200,
		ConfirmValue: "vdb",
		Commands:     []string{"sgdisk --zap-all /dev/vdb", "mkfs.ext4 /dev/vdb1"},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	// PostgreSQL jsonb does not preserve the original key order. Simulate the
	// round-trip by decoding into maps, which Go marshals in sorted key order.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	if canonicalPlanHash(raw) != canonicalPlanHash(normalized) {
		t.Fatal("canonical plan hash changed across a JSONB-shaped round-trip")
	}
	if hashToken(string(raw)) == hashToken(string(normalized)) {
		t.Log("fixture preserved key order, but the canonical hash comparison above still ran")
	}

	plan.Name = "mail-other"
	changed, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalPlanHash(raw) == canonicalPlanHash(changed) {
		t.Fatal("canonical plan hash did not change when the plan changed")
	}
}
