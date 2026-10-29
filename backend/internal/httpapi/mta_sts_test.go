package httpapi

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestMTASTSPolicyBodyAndID(t *testing.T) {
	policy := mtaSTSPolicy{Domain: "example.test", Mode: "enforce", MaxAge: 604800, MailHost: "mail.example.test"}
	body := string(policy.Body())
	for _, want := range []string{"version: STSv1", "mode: enforce", "mx: mail.example.test", "max_age: 604800"} {
		if !strings.Contains(body, want) {
			t.Fatalf("policy body missing %q: %s", want, body)
		}
	}
	id := policy.ID()
	testingPolicy := mtaSTSPolicy{Domain: "example.test", Mode: "testing", MaxAge: 604800, MailHost: "mail.example.test"}
	if id == "" || id == testingPolicy.ID() {
		t.Fatal("policy id must change when the mode changes")
	}
	if policy.URL() != "https://mta-sts.example.test/.well-known/mta-sts.txt" {
		t.Fatalf("unexpected policy URL %q", policy.URL())
	}
	if verifyMTASTSPolicyRecord(body, "example.test", "enforce", 604800, "mail.example.test") != nil {
		t.Fatal("generated policy should validate")
	}
	if err := verifyMTASTSPolicyRecord(strings.Replace(body, "mode: enforce", "mode: testing", 1), "example.test", "enforce", 604800, "mail.example.test"); err == nil {
		t.Fatal("mode mismatch must fail")
	}
	if err := verifyMTASTSPolicyRecord(strings.Replace(body, "mx: mail.example.test", "mx: mx.other.test", 1), "example.test", "enforce", 604800, "mail.example.test"); err == nil {
		t.Fatal("mx mismatch must fail")
	}
}

func TestVerifyDomainDNSMTASTS(t *testing.T) {
	policy := mtaSTSPolicy{Domain: "example.test", Mode: "enforce", MaxAge: 604800, MailHost: "mail.example.test"}
	lookup := fakeDomainLookup{
		txt: map[string][]string{
			"_meridian-verification.example.test": {"meridian-verify=tok_123"},
			"example.test":                        {"v=spf1 mx -all"},
			"meridian._domainkey.example.test":    {"v=DKIM1; k=rsa; p=REALKEY"},
			"_dmarc.example.test":                 {"v=DMARC1; p=reject"},
			"_mta-sts.example.test":               {"v=STSv1; id=" + policy.ID()},
		},
		mx: map[string][]*net.MX{
			"example.test": {{Host: "mail.example.test."}},
		},
	}
	fetch := func(context.Context, string) (string, error) { return string(policy.Body()), nil }
	checks := verifyDomainDNSFull(context.Background(), lookup, "example.test", "meridian", "tok_123",
		"v=DKIM1; k=rsa; p=REALKEY", "mail.example.test", "enforce", 604800, fetch)
	if !checks.Ready || !checks.MTASTS {
		t.Fatalf("MTA-STS should verify: %#v", checks)
	}

	staleLookup := lookup
	staleLookup.txt = map[string][]string{}
	for key, value := range lookup.txt {
		staleLookup.txt[key] = value
	}
	staleLookup.txt["_mta-sts.example.test"] = []string{"v=STSv1; id=stale"}
	checks = verifyDomainDNSFull(context.Background(), staleLookup, "example.test", "meridian", "tok_123",
		"v=DKIM1; k=rsa; p=REALKEY", "mail.example.test", "enforce", 604800, fetch)
	if checks.MTASTS || checks.Ready || !strings.Contains(checks.Details["mta-sts"], "id=") {
		t.Fatalf("stale MTA-STS id must fail: %#v", checks)
	}

	checks = verifyDomainDNSFull(context.Background(), lookup, "example.test", "meridian", "tok_123",
		"v=DKIM1; k=rsa; p=REALKEY", "mail.example.test", "enforce", 604800,
		func(context.Context, string) (string, error) {
			return "version: STSv1\r\nmode: testing\r\nmx: mail.example.test\r\nmax_age: 604800\r\n", nil
		})
	if checks.MTASTS || checks.Ready {
		t.Fatalf("policy mode mismatch must fail: %#v", checks)
	}
}

func TestDomainRecordsIncludeMTASTS(t *testing.T) {
	records := domainRecordsWithMTA("example.test", "meridian", "tok_123", "mail.example.test",
		"v=DKIM1; k=rsa; p=KEY", "reject", "enforce", 604800)
	var txt, policy map[string]string
	for _, record := range records {
		if strings.HasPrefix(record["name"], "_mta-sts.") {
			txt = record
		}
		if record["type"] == "POLICY" {
			policy = record
		}
	}
	if txt == nil || !strings.HasPrefix(txt["value"], "v=STSv1; id=") {
		t.Fatalf("MTA-STS TXT record missing: %#v", records)
	}
	if policy == nil || !strings.Contains(policy["value"], "mode: enforce") {
		t.Fatalf("MTA-STS policy record missing: %#v", records)
	}
}

func TestIsPublicIP(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.1.1", "100.64.0.1", "::1", "fc00::1"} {
		if isPublicIP(net.ParseIP(value)) {
			t.Fatalf("%s should not be considered public", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublicIP(net.ParseIP(value)) {
			t.Fatalf("%s should be public", value)
		}
	}
}
