package control

import "testing"

func TestAddonLogoAssetsEmbedded(t *testing.T) {
	for _, name := range []string{
		"deepl.svg", "todoist.svg", "slack.png", "hubspot.svg", "asana.svg",
		"pagerduty.svg", "zoom.svg", "pipedrive.png", "zoho.svg", "clickup.svg", "zendesk.svg",
		"salesforce.svg", "freshdesk.svg", "intercom.svg", "ticktick.svg", "calendly.svg", "jira.svg", "linear.svg",
	} {
		if _, err := webFiles.ReadFile("web/addon-logos/" + name); err != nil {
			t.Fatalf("add-on logo %s is not embedded: %v", name, err)
		}
	}
}
