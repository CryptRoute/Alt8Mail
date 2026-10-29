package httpapi

import (
	"context"
	"net"
	"strings"
	"testing"
)

type fakeDomainLookup struct {
	txt map[string][]string
	mx  map[string][]*net.MX
}

func (f fakeDomainLookup) LookupTXT(_ context.Context, name string) ([]string, error) {
	if values, ok := f.txt[name]; ok {
		return values, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDomainLookup) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if values, ok := f.mx[name]; ok {
		return values, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestVerifyDomainDNSExactDKIMAndMailHost(t *testing.T) {
	expected := "v=DKIM1; k=rsa; p=AbCdEf0123456789+/="
	lookup := fakeDomainLookup{
		txt: map[string][]string{
			"_a8m-verification.example.test": {"a8m-verify=tok_123"},
			"example.test":                        {"v=spf1 mx -all"},
			"a8m._domainkey.example.test":    {"v=DKIM1; k=rsa; p=AbCdEf0123456789+/="},
			"_dmarc.example.test":                 {"v=DMARC1; p=quarantine; rua=mailto:dmarc@example.test"},
		},
		mx: map[string][]*net.MX{
			"example.test": {{Host: "mail.a8m.test.", Pref: 10}},
		},
	}
	checks := verifyDomainDNSWith(context.Background(), lookup, "example.test", "a8m", "tok_123", expected, "mail.a8m.test")
	if !checks.Ready || !checks.Verification || !checks.MX || !checks.SPF || !checks.DKIM || !checks.DMARC {
		t.Fatalf("expected all checks to pass: %#v", checks)
	}
	if checks.DMARCPolicy != "quarantine" {
		t.Fatalf("unexpected DMARC policy %q", checks.DMARCPolicy)
	}
}

func TestVerifyDomainDNSRejectsWrongDKIMKey(t *testing.T) {
	lookup := fakeDomainLookup{
		txt: map[string][]string{
			"_a8m-verification.example.test": {"a8m-verify=tok_123"},
			"example.test":                        {"v=spf1 mx -all"},
			"a8m._domainkey.example.test":    {"v=DKIM1; k=rsa; p=OTHERKEY"},
			"_dmarc.example.test":                 {"v=DMARC1; p=reject"},
		},
		mx: map[string][]*net.MX{
			"example.test": {{Host: "mail.a8m.test."}},
		},
	}
	checks := verifyDomainDNSWith(context.Background(), lookup, "example.test", "a8m", "tok_123", "v=DKIM1; k=rsa; p=EXPECTEDKEY", "mail.a8m.test")
	if checks.DKIM || checks.Ready {
		t.Fatalf("wrong p= must not verify: %#v", checks)
	}
	if !strings.Contains(checks.Details["dkim"], "does not match") {
		t.Fatalf("missing mismatch detail: %#v", checks.Details)
	}
}

func TestVerifyDomainDNSMissingKeyAndWrongMX(t *testing.T) {
	lookup := fakeDomainLookup{
		txt: map[string][]string{
			"_a8m-verification.example.test": {"a8m-verify=tok_123"},
			"example.test":                        {"v=spf1 mx -all"},
			"_dmarc.example.test":                 {"v=DMARC1; p=none"},
		},
		mx: map[string][]*net.MX{
			"example.test": {{Host: "mx.other.test."}},
		},
	}
	checks := verifyDomainDNSWith(context.Background(), lookup, "example.test", "a8m", "tok_123", "", "mail.a8m.test")
	if checks.MX || checks.DKIM || checks.Ready {
		t.Fatalf("wrong MX and missing key must fail: %#v", checks)
	}
	if !strings.Contains(checks.Details["dkim"], "not been provisioned") {
		t.Fatalf("missing key detail: %#v", checks.Details)
	}
	if !strings.Contains(checks.Details["mx"], "expected mail.a8m.test") {
		t.Fatalf("MX detail: %#v", checks.Details)
	}
}

func TestDomainRecordsUseRealDKIMKey(t *testing.T) {
	records := domainRecords("example.test", "a8m", "tok_123", "mail.example.test",
		"v=DKIM1; k=rsa; p=REAL_PUBLIC_KEY", "reject")
	var dkim map[string]string
	for _, record := range records {
		if strings.Contains(record["name"], "_domainkey") {
			dkim = record
		}
	}
	if dkim == nil {
		t.Fatal("DKIM record missing")
	}
	if dkim["value"] != "v=DKIM1; k=rsa; p=REAL_PUBLIC_KEY" || dkim["status"] != "ready" {
		t.Fatalf("DKIM record is not the real key: %#v", dkim)
	}
	if strings.Contains(dkim["value"], "<public key") {
		t.Fatal("placeholder leaked into DNS records")
	}
	pending := domainRecords("example.test", "a8m", "tok_123", "mail.example.test", "", "none")
	for _, record := range pending {
		if strings.Contains(record["name"], "_domainkey") && (record["status"] != "provisioning" || record["value"] != "") {
			t.Fatalf("pending DKIM record should not invent a key: %#v", record)
		}
	}
}

func TestDKIMRecordParsing(t *testing.T) {
	tags := parseDKIMTags(" v=DKIM1; k=rsa; p=ABC \n DEF ;")
	if tags["v"] != "DKIM1" || tags["k"] != "rsa" || normalizeDKIMBase64(tags["p"]) != "ABCDEF" {
		t.Fatalf("unexpected DKIM tags: %#v", tags)
	}
}
