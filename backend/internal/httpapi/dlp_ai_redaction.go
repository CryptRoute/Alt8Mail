package httpapi

import (
	"regexp"
	"strings"
)

// aiPolicyTerms returns the enabled literal DLP terms that must never reach an
// external AI provider.
func aiPolicyTerms(policy adminTenantPolicy) []string {
	terms := []string{}
	for _, rule := range policy.DLP.Rules {
		if !rule.Enabled {
			continue
		}
		for _, term := range rule.Terms {
			if trimmed := strings.TrimSpace(term); trimmed != "" {
				terms = append(terms, trimmed)
			}
		}
	}
	return terms
}

func redactAIPolicyTerms(value string, terms []string) string {
	for _, term := range terms {
		if term == "" {
			continue
		}
		replacement := "[REDACTED]"
		if strings.Contains(strings.ToLower(replacement), strings.ToLower(term)) {
			replacement = "█"
		}
		value = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(term)).ReplaceAllString(value, replacement)
	}
	return value
}
