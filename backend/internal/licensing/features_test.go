package licensing

import (
	"reflect"
	"testing"
)

func TestFeatureCatalogIsComplete(t *testing.T) {
	want := []string{
		"sso", "saml", "scim", "white_label", "backup_restore",
		"shared_inbox", "shared_drafts", "team_templates", "automation",
		"recipient_scheduling", "mail_followups", "mail_merge", "booking_pages",
		"template_gallery", "deliverability", "campaign_tracking",
		"ai_triage", "ai_obligations", "ai_search", "ai_meeting_prep",
		"office_ai", "ai_policy_assist", "mcp_governance", "ai_budget",
		"dlp_advanced", "ediscovery", "byok", "file_governance", "audit_export",
		"crm_sync", "migration", "analytics", "security_awareness", "support_ops",
	}
	if !reflect.DeepEqual(PaidFeatureKeys, want) {
		t.Fatalf("PaidFeatureKeys = %#v, want %#v", PaidFeatureKeys, want)
	}
	seen := map[string]bool{}
	for _, feature := range FeatureCatalog {
		if feature.Key == "" || feature.Label == "" || feature.Group == "" || feature.Plan == "" {
			t.Fatalf("incomplete catalog entry: %#v", feature)
		}
		if seen[feature.Key] {
			t.Fatalf("duplicate feature key %q", feature.Key)
		}
		seen[feature.Key] = true
		if _, ok := FeatureByKey(feature.Key); !ok {
			t.Fatalf("FeatureByKey(%q) not found", feature.Key)
		}
	}
	for _, key := range want {
		if !seen[key] {
			t.Fatalf("catalog is missing %q", key)
		}
	}
}

func TestFeatureLookupIsCaseInsensitive(t *testing.T) {
	if feature, ok := FeatureByKey("  AI_TRIAGE "); !ok || feature.Key != FeatureAITriage {
		t.Fatalf("FeatureByKey returned %#v, %v", feature, ok)
	}
	if _, ok := FeatureByKey("not_a_feature"); ok {
		t.Fatal("unknown feature was found")
	}
}

func TestTeamTemplatesBundledWithSharedInbox(t *testing.T) {
	license := License{Features: []string{FeatureSharedInbox}}
	if !license.HasFeature(FeatureTeamTemplates) {
		t.Fatal("shared_inbox must include team_templates")
	}
	if license.HasFeature(FeatureAutomation) {
		t.Fatal("shared_inbox must not include automation")
	}
}

func TestMissingFeaturesUsesCompleteCatalog(t *testing.T) {
	missing := License{}.MissingFeatures()
	if !reflect.DeepEqual(missing, PaidFeatureKeys) {
		t.Fatalf("empty license missing = %#v", missing)
	}
}
