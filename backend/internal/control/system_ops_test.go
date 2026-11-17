package control

import "testing"

func TestParseUpdateCheck(t *testing.T) {
	tests := []struct {
		name, output, current, latest string
		ok                            bool
	}{
		{
			name:    "status line",
			output:  "[12:00:00] installed=0.3.12 latest=0.3.13\n[12:00:00] update-available 0.3.12 -> 0.3.13",
			current: "0.3.12", latest: "0.3.13", ok: true,
		},
		{
			name:    "up to date",
			output:  "[12:00:00] installed=0.3.13 latest=0.3.13\n[12:00:00] up-to-date 0.3.13",
			current: "0.3.13", latest: "0.3.13", ok: true,
		},
		{
			name:    "legacy up to date",
			output:  "up-to-date 0.3.13",
			current: "0.3.13", latest: "0.3.13", ok: true,
		},
		{
			name:    "legacy available",
			output:  "update-available 0.3.12 -> 0.3.13",
			current: "0.3.12", latest: "0.3.13", ok: true,
		},
		{name: "unparseable", output: "could not reach repository", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, latest, ok := parseUpdateCheck(test.output)
			if ok != test.ok || current != test.current || latest != test.latest {
				t.Fatalf("got current=%q latest=%q ok=%t, want current=%q latest=%q ok=%t",
					current, latest, ok, test.current, test.latest, test.ok)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"0.3.12", "0.3.12", 0},
		{"0.3.13", "0.3.12", 1},
		{"0.3.12", "0.3.13", -1},
		{"v0.4.0", "0.3.99", 1},
		{"1.0.0", "0.99.99", 1},
	}
	for _, test := range tests {
		if got := compareVersions(test.left, test.right); got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestAppVersionTagValidation(t *testing.T) {
	valid := []string{"v0.3.13", "v1.0.0", "v0.3.13-rc.1"}
	invalid := []string{"0.3.13", "v0.3", "v0.3.13; reboot", "../v0.3.13", "v0.3.13 --force"}
	for _, value := range valid {
		if !appVersionTag.MatchString(value) {
			t.Fatalf("expected %q to be a valid target tag", value)
		}
	}
	for _, value := range invalid {
		if appVersionTag.MatchString(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
