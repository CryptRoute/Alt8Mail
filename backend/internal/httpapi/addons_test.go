package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAddonDefinitionsAreUniqueAndComplete(t *testing.T) {
	if len(addonDefinitions) != 18 {
		t.Fatalf("add-on catalog has %d connectors, want 18", len(addonDefinitions))
	}
	seen := map[string]bool{}
	for _, definition := range addonDefinitions {
		if definition.ID == "" || definition.Name == "" {
			t.Fatalf("add-on is missing id or name: %#v", definition)
		}
		if seen[definition.ID] {
			t.Fatalf("duplicate add-on id %q", definition.ID)
		}
		seen[definition.ID] = true
		if definition.ConnectionScope != "user" && definition.ConnectionScope != "tenant" {
			t.Fatalf("invalid connection scope for %s: %q", definition.ID, definition.ConnectionScope)
		}
		if definition.AuthType != "api_key" && definition.AuthType != "webhook" && definition.AuthType != "oauth2" {
			t.Fatalf("invalid auth type for %s: %q", definition.ID, definition.AuthType)
		}
		if len(definition.Actions) == 0 {
			t.Fatalf("%s has no actions", definition.ID)
		}
		if len(definition.Fields) == 0 {
			t.Fatalf("%s has no setup fields", definition.ID)
		}
		if definition.Tagline == "" || definition.ConnectorVersion == "" {
			t.Fatalf("%s is missing provider identity metadata", definition.ID)
		}
		for label, value := range map[string]string{
			"website": definition.WebsiteURL, "support": definition.SupportURL,
			"privacy": definition.PrivacyURL, "docs": definition.DocsURL,
		} {
			if !strings.HasPrefix(value, "https://") {
				t.Fatalf("%s has an invalid %s URL %q", definition.ID, label, value)
			}
		}
		if !strings.HasSuffix(definition.LogoAsset, ".svg") && !strings.HasSuffix(definition.LogoAsset, ".png") {
			t.Fatalf("%s has an unsupported logo asset %q", definition.ID, definition.LogoAsset)
		}
		for _, field := range definition.Fields {
			if field.Key == "" || field.Label == "" {
				t.Fatalf("%s has an incomplete field: %#v", definition.ID, field)
			}
			if field.Type == "select" && len(field.Options) == 0 {
				t.Fatalf("%s select field %q has no options", definition.ID, field.Key)
			}
		}
	}
}

func TestValidateSlackWebhook(t *testing.T) {
	valid := []string{
		"https://hooks.slack.com/services/T000/B000/XXXX",
		"https://hooks.slack-gov.com/services/T000/B000/XXXX",
	}
	for _, value := range valid {
		if err := validateSlackWebhook(value); err != nil {
			t.Errorf("validateSlackWebhook(%q) = %v", value, err)
		}
	}
	invalid := []string{
		"http://hooks.slack.com/services/T000/B000/XXXX",
		"https://example.com/services/T000/B000/XXXX",
		"https://hooks.slack.com/not-services/T000/B000/XXXX",
		"not a url",
	}
	for _, value := range invalid {
		if err := validateSlackWebhook(value); err == nil {
			t.Errorf("validateSlackWebhook(%q) accepted an invalid URL", value)
		}
	}
}

func TestAddonConnectorValidationHelpers(t *testing.T) {
	if !validAsanaGID("1201234567890123") || validAsanaGID("project/other") || validAsanaGID("") {
		t.Error("Asana GID validation is incorrect")
	}
	for _, severity := range []string{"critical", "error", "warning", "info"} {
		if !validPagerDutySeverity(severity) {
			t.Errorf("PagerDuty severity %q was rejected", severity)
		}
	}
	if validPagerDutySeverity("urgent") {
		t.Error("unsupported PagerDuty severity was accepted")
	}
}

func TestWave2ConnectorValidationHelpers(t *testing.T) {
	for _, region := range []string{"com", "eu", "in", "com.au", "jp"} {
		if !validZohoRegion(region) {
			t.Errorf("Zoho region %q was rejected", region)
		}
	}
	if validZohoRegion("ca") || validZohoRegion("") {
		t.Error("unsupported Zoho region was accepted")
	}
	if accounts, api, ok := zohoEndpoints("eu"); !ok || accounts != "accounts.zoho.eu" || api != "www.zohoapis.eu" {
		t.Errorf("Zoho EU endpoints = %q, %q, %v", accounts, api, ok)
	}
	if !validClickUpList("901234567890") || validClickUpList("list/other") || validClickUpList("") {
		t.Error("ClickUp list validation is incorrect")
	}
	if !validZendeskSubdomain("acme") || !validZendeskSubdomain("help-emea") {
		t.Error("valid Zendesk subdomains were rejected")
	}
	for _, value := range []string{"", "-acme", "acme-", "acme_zendesk", "acme.example"} {
		if validZendeskSubdomain(value) {
			t.Errorf("invalid Zendesk subdomain %q was accepted", value)
		}
	}
}

func TestWave3ConnectorValidationHelpers(t *testing.T) {
	if !validSalesforceLogin("https://login.salesforce.com") || !validSalesforceLogin("https://test.salesforce.com") {
		t.Error("valid Salesforce login URLs were rejected")
	}
	if validSalesforceLogin("https://example.com") {
		t.Error("unsupported Salesforce login URL was accepted")
	}
	for _, region := range []string{"us", "eu", "au"} {
		if !validIntercomRegion(region) {
			t.Errorf("Intercom region %q was rejected", region)
		}
	}
	if validIntercomRegion("ca") {
		t.Error("unsupported Intercom region was accepted")
	}
	if !validTickTickProject("665f0b1e8b3e4a0012345678") || validTickTickProject("bad/project") {
		t.Error("TickTick project validation is incorrect")
	}
	if !validCalendlyURI("https://api.calendly.com/event_types/ABC123", "/event_types/") ||
		!validCalendlyURI("https://api.calendly.com/users/XYZ789", "/users/") {
		t.Error("valid Calendly URIs were rejected")
	}
	if validCalendlyURI("https://example.com/event_types/ABC", "/event_types/") {
		t.Error("external Calendly URI host was accepted")
	}
	if !validJiraSite("acme.atlassian.net") || validJiraSite("acme.example.com") || validJiraSite("ACME.atlassian.net") {
		t.Error("Jira site validation is incorrect")
	}
	if !validJiraProjectKey("ENG") || !validJiraProjectKey("OPS2") || validJiraProjectKey("1BAD") || validJiraProjectKey("A") {
		t.Error("Jira project key validation is incorrect")
	}
	if !validLinearTeam("12345678-1234-1234-1234-123456789abc") || validLinearTeam("team-1") {
		t.Error("Linear team validation is incorrect")
	}
}

func TestAllowedDeepLTarget(t *testing.T) {
	if !allowedDeepLTarget("EN-US") || !allowedDeepLTarget("FR") || !allowedDeepLTarget("PT-BR") {
		t.Error("expected English, French and Brazilian Portuguese to be supported")
	}
	if allowedDeepLTarget("XX") || allowedDeepLTarget("") {
		t.Error("unsupported target language was accepted")
	}
}

func TestAddonEffectiveTenantEnabled(t *testing.T) {
	if !addonEffectiveTenantEnabled("user", false, false) {
		t.Error("personal add-ons should default to enabled when no tenant policy exists")
	}
	if addonEffectiveTenantEnabled("tenant", false, false) {
		t.Error("workspace add-ons must require setup when no tenant policy exists")
	}
	if addonEffectiveTenantEnabled("tenant", true, false) {
		t.Error("a disabled tenant policy must disable the add-on")
	}
	if !addonEffectiveTenantEnabled("tenant", true, true) {
		t.Error("an enabled tenant policy should enable the add-on")
	}
}

func TestAddonConnectionPublicConfigOmitsSecrets(t *testing.T) {
	config := addonConnectionPublicConfig(addonDefinition{
		ConnectionScope: "user",
	}, addonConnectionRecord{Config: map[string]any{
		"targetLanguage": "FR",
		"apiToken":       "secret-token",
		"password":       "secret-password",
	}}, addonConnectionRecord{})
	if config["targetLanguage"] != "FR" {
		t.Fatalf("target language missing from public config: %#v", config)
	}
	if _, exists := config["apiToken"]; exists {
		t.Error("token leaked through public add-on config")
	}
	if _, exists := config["password"]; exists {
		t.Error("password leaked through public add-on config")
	}
}

func TestVerifySlackSignature(t *testing.T) {
	secret := "slack-signing-secret"
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	body := []byte(`{"type":"event_callback","team_id":"T1"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + string(body)))
	signature := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySlackSignature(secret, timestamp, signature, body) {
		t.Fatal("valid Slack signature was rejected")
	}
	if verifySlackSignature(secret, timestamp, signature, []byte(`{"tampered":true}`)) {
		t.Fatal("tampered Slack body was accepted")
	}
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	oldMac := hmac.New(sha256.New, []byte(secret))
	_, _ = oldMac.Write([]byte("v0:" + oldTimestamp + ":" + string(body)))
	oldSignature := "v0=" + hex.EncodeToString(oldMac.Sum(nil))
	if verifySlackSignature(secret, oldTimestamp, oldSignature, body) {
		t.Fatal("stale Slack signature was accepted")
	}
}
