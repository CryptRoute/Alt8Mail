package httpapi

import (
	"strings"
	"testing"
)

func TestDLPDetectorCorpus(t *testing.T) {
	if !luhnValid("4111 1111 1111 1111") || luhnValid("4111 1111 1111 1112") {
		t.Fatal("Luhn validation is wrong")
	}
	if !ibanValid("GB82WEST12345698765432") || ibanValid("GB82WEST12345698765431") {
		t.Fatal("IBAN validation is wrong")
	}
	cardHits := dlpDetectorMatches("credit_card", adminDLPRule{}, "pay with 4111 1111 1111 1111 today")
	if len(cardHits) != 1 || strings.Contains(cardHits[0].Display, "4111 1111") || !strings.Contains(cardHits[0].Display, "1111") {
		t.Fatalf("card hits = %#v", cardHits)
	}
	ibanHits := dlpDetectorMatches("iban", adminDLPRule{}, "account GB82WEST12345698765432")
	if len(ibanHits) != 1 || strings.Contains(ibanHits[0].Display, "GB82WEST") {
		t.Fatalf("iban hits = %#v", ibanHits)
	}
	national := dlpDetectorMatches("national_id", adminDLPRule{}, "SSN 123-45-6789 and NI AB123456C")
	if len(national) != 2 {
		t.Fatalf("national id hits = %#v", national)
	}
	dictionary := dlpDetectorMatches("dictionary", adminDLPRule{Terms: []string{"project atlas"}}, "the Project Atlas launch")
	if len(dictionary) != 1 {
		t.Fatalf("dictionary hits = %#v", dictionary)
	}
	regex := dlpDetectorMatches("regex", adminDLPRule{Pattern: `AKIA[0-9A-Z]{16}`}, "key AKIAABCDEFGHIJKLMNOP")
	if len(regex) != 1 || regex[0].Display != "[redacted]" {
		t.Fatalf("regex hits = %#v", regex)
	}
}

func TestDLPDetectorPolicyDoesNotLeakValues(t *testing.T) {
	policy := adminTenantPolicy{DLP: adminDLPPolicy{
		Enabled: true, DefaultClassification: "Internal",
		Rules: []adminDLPRule{{
			ID: "card", Name: "Card numbers", Detector: "credit_card",
			Fields: []string{"body"}, Classification: "Restricted", Action: "block",
			Direction: "outbound", Enabled: true,
		}},
	}}
	classification, warnings, matches, blocked := evaluateOutboundDLP(policy, "Payment", "card 4111 1111 1111 1111", nil, "")
	if classification != "Restricted" || !blocked || len(matches) != 1 || matches[0].Detector != "credit_card" {
		t.Fatalf("classification=%s blocked=%v matches=%#v", classification, blocked, matches)
	}
	for _, warning := range warnings {
		if strings.Contains(warning, "4111 1111 1111 1111") {
			t.Fatalf("warning leaked card number: %s", warning)
		}
	}
	redacted := redactDLPDetectors("card 4111 1111 1111 1111", policy)
	if strings.Contains(redacted, "4111") || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("redaction = %q", redacted)
	}
}
