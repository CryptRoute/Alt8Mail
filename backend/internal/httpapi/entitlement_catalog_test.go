package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

func TestLiteralEntitlementCatalogCoverage(t *testing.T) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`s\.entitled\("([a-z0-9_]+)"\)`),
		regexp.MustCompile(`featureGate\(s,\s*"([a-z0-9_]+)"`),
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
				if _, ok := licensing.FeatureByKey(match[1]); !ok {
					t.Errorf("%s uses entitlement %q missing from licensing.FeatureCatalog", entry.Name(), match[1])
				}
			}
		}
	}
}

func TestFeatureDetailsContainCatalogMetadata(t *testing.T) {
	server := &Server{cfg: config.Config{LicensePortalURL: "https://license.example"}}
	details := server.featureDetails(licensing.FeatureSSO)
	if details["feature"] != licensing.FeatureSSO || details["label"] == "" || details["group"] == "" {
		t.Fatalf("feature details = %#v", details)
	}
	if details["upgradeUrl"] != "https://license.example/upgrade?feature=sso" {
		t.Fatalf("upgradeUrl = %#v", details["upgradeUrl"])
	}
}
