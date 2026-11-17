package control

import (
	"strings"
	"testing"
)

const masterFixture = `# Postfix master process configuration file.
#
smtp       inet  n       -       n       -       -       smtpd
submission inet n       -       n       -       -       smtpd
  -o syslog_name=postfix/submission
  -o smtpd_tls_security_level=encrypt
  -o smtpd_sasl_auth_enable=yes
  -o smtpd_client_restrictions=permit_sasl_authenticated,reject
  -o smtpd_relay_restrictions=permit_sasl_authenticated,reject
  -o milter_macro_daemon_name=ORIGINATING
pickup     unix  n       -       n       60      1       pickup
cleanup    unix  n       -       n       -       0       cleanup
`

func fixtureListeners() []listenerConfig {
	return []listenerConfig{
		{Protocol: "smtp", Port: 25, TLS: "starttls"},
		{Protocol: "submission", Port: 587, TLS: "starttls", SASL: true},
		{Protocol: "smtps", Port: 465, TLS: "implicit", SASL: true},
		{Protocol: "imap", Port: 143, TLS: "starttls", SASL: true},
		{Protocol: "imaps", Port: 993, TLS: "implicit", SASL: true},
		{Protocol: "pop3", Port: 110, TLS: "starttls", SASL: true},
		{Protocol: "pop3s", Port: 995, TLS: "implicit", SASL: true},
	}
}

func TestRenderMasterCfOnlyManagesSmtpServices(t *testing.T) {
	output := renderMasterCfFrom(masterFixture, fixtureListeners())
	if strings.Contains(output, "143 inet") || strings.Contains(output, "993 inet") ||
		strings.Contains(output, "110 inet") || strings.Contains(output, "995 inet") {
		t.Fatalf("renderer tried to add IMAP/POP3 listeners to Postfix:\n%s", output)
	}
	if !strings.Contains(output, "submission inet") {
		t.Fatalf("submission block missing:\n%s", output)
	}
	if !strings.Contains(output, "smtps") || !strings.Contains(output, "smtpd_tls_wrappermode=yes") {
		t.Fatalf("SMTPS block not generated with implicit TLS:\n%s", output)
	}
	if !strings.Contains(output, "milter_macro_daemon_name=ORIGINATING") {
		t.Fatalf("submission milter macro was lost:\n%s", output)
	}
	if strings.Count(output, " smtpd\n")+strings.Count(output, " smtpd") < 3 {
		t.Fatalf("expected three smtpd services:\n%s", output)
	}
	if !strings.Contains(output, "pickup") || !strings.Contains(output, "cleanup") {
		t.Fatalf("unrelated master.cf services were removed:\n%s", output)
	}
}

func TestRenderMasterCfStable(t *testing.T) {
	first := renderMasterCfFrom(masterFixture, fixtureListeners())
	second := renderMasterCfFrom(first, fixtureListeners())
	if first != second {
		t.Fatalf("listener render is not idempotent\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestRenderMasterCfChangesPorts(t *testing.T) {
	items := fixtureListeners()
	items[0].Port = 2525
	items[1].Port = 2587
	items[2].Port = 2465
	output := renderMasterCfFrom(masterFixture, items)
	for _, expected := range []string{"2525", "2587", "2465"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected port %s in output:\n%s", expected, output)
		}
	}
	if strings.Contains(output, "\nsmtp       inet") || strings.Contains(output, "\nsubmission inet") {
		t.Fatalf("old named service remained after a port change:\n%s", output)
	}
}

func TestRenderMasterCfRemovesLegacyManagedBlock(t *testing.T) {
	raw := masterFixture + `
# BEGIN ALT8MAIL LISTENERS
143 inet n - n - - smtpd
# END ALT8MAIL LISTENERS
`
	output := renderMasterCfFrom(raw, fixtureListeners())
	if strings.Contains(output, "# BEGIN ALT8MAIL LISTENERS") || strings.Contains(output, "143 inet") {
		t.Fatalf("legacy managed block was retained:\n%s", output)
	}
}

func TestValidateListeners(t *testing.T) {
	items := fixtureListeners()
	if err := validateListeners(items); err != nil {
		t.Fatalf("valid listeners rejected: %v", err)
	}
	bad := fixtureListeners()
	bad[3].Port = 25
	if err := validateListeners(bad); err == nil {
		t.Fatal("duplicate port was accepted")
	}
	bad = fixtureListeners()
	bad[0].TLS = "implicit"
	if err := validateListeners(bad); err == nil {
		t.Fatal("implicit TLS on inbound SMTP was accepted")
	}
}
