package httpapi

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

func TestMailConnectionAddressSafety(t *testing.T) {
	safe := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, value := range safe {
		addr, err := netip.ParseAddr(value)
		if err != nil || !publicMailAddress(addr) {
			t.Errorf("public address %s was rejected", value)
		}
	}
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1",
		"::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "0.0.0.0", "224.0.0.1",
		"192.0.2.10", "198.18.0.1", "198.51.100.1", "203.0.113.9", "240.0.0.1",
		"2001:db8::1", "2002:7f00:1::1", "64:ff9b::7f00:1",
	}
	for _, value := range blocked {
		addr, err := netip.ParseAddr(value)
		if err != nil {
			t.Fatalf("parse %s: %v", value, err)
		}
		if publicMailAddress(addr) {
			t.Errorf("local/private address %s was accepted", value)
		}
	}
	for _, value := range []string{"imap.example.com", "smtp.example.co.uk", "8.8.8.8"} {
		if !validMailHostname(value) {
			t.Errorf("hostname %q was rejected", value)
		}
	}
	for _, value := range []string{"", "localhost", "127.0.0.1:993", "imap example.com", "-bad.example.com", "bad-.example.com", "a/b", "example.com.", "user@example.com"} {
		if validMailHostname(value) {
			t.Errorf("hostname %q was accepted", value)
		}
	}
	if err := validateMailPort(25); err == nil {
		t.Error("port 25 should be rejected")
	}
	for _, port := range []int{143, 465, 587, 993, 2525} {
		if err := validateMailPort(port); err != nil {
			t.Errorf("port %d was rejected: %v", port, err)
		}
	}
}

func TestProviderMailPresets(t *testing.T) {
	cases := []struct {
		input, provider, imapHost, smtpHost string
		smtpPort                            int
	}{
		{"hotmail", "microsoft", "outlook.office365.com", "smtp.office365.com", 587},
		{"Outlook", "microsoft", "outlook.office365.com", "smtp.office365.com", 587},
		{"yahoo", "yahoo", "imap.mail.yahoo.com", "smtp.mail.yahoo.com", 465},
		{"icloud", "icloud", "imap.mail.me.com", "smtp.mail.me.com", 587},
		{"aol", "aol", "imap.aol.com", "smtp.aol.com", 465},
		{"zoho", "zoho", "imap.zoho.com", "smtp.zoho.com", 465},
		{"gmx", "gmx", "imap.gmx.com", "smtp.gmx.com", 587},
		{"yandex", "yandex", "imap.yandex.com", "smtp.yandex.com", 465},
		{"fastmail", "fastmail", "imap.fastmail.com", "smtp.fastmail.com", 465},
		{"mail.com", "mailcom", "imap.mail.com", "smtp.mail.com", 465},
	}
	for _, testCase := range cases {
		provider := normalizeAccountProvider(testCase.input)
		if provider != testCase.provider {
			t.Fatalf("normalizeAccountProvider(%q) = %q, want %q", testCase.input, provider, testCase.provider)
		}
		imapHost, _, smtpHost, smtpPort := providerMailDefaults(provider)
		if imapHost != testCase.imapHost || smtpHost != testCase.smtpHost || smtpPort != testCase.smtpPort {
			t.Fatalf("%s defaults = %q %q %d", provider, imapHost, smtpHost, smtpPort)
		}
	}
	if normalizeAccountProvider("proton") != "" {
		t.Fatal("unsupported provider must be rejected")
	}
}

func TestExternalAccountRecordValidation(t *testing.T) {
	server := &Server{}
	user := userRecord{ID: "user-1", TenantID: "tenant-1", Email: "owner@example.test", Name: "Owner"}
	valid := mailAccountCreateBody{
		Provider: "imap", Email: "person@example.test", Password: "app-password",
		IMAPHost: "8.8.8.8", IMAPPort: 993, IMAPTLS: "tls",
		SMTPHost: "1.1.1.1", SMTPPort: 587, SMTPTLS: "starttls",
	}
	account, password, err := server.externalAccountRecordFromBody(context.Background(), user, valid)
	if err != nil {
		t.Fatalf("valid account was rejected: %v", err)
	}
	if password != "app-password" || account.Email != "person@example.test" ||
		account.Kind != mailAccountKindExternal || account.IMAPHost != "8.8.8.8" {
		t.Fatalf("unexpected account: %#v", account)
	}
	cases := []struct {
		name   string
		change func(*mailAccountCreateBody)
		want   string
	}{
		{"private imap", func(body *mailAccountCreateBody) { body.IMAPHost = "127.0.0.1" }, "private"},
		{"port 25", func(body *mailAccountCreateBody) { body.SMTPPort = 25 }, "port 25"},
		{"wrong imap tls", func(body *mailAccountCreateBody) { body.IMAPTLS = "starttls" }, "993"},
		{"wrong smtp tls", func(body *mailAccountCreateBody) { body.SMTPTLS = "tls" }, "587"},
		{"missing password", func(body *mailAccountCreateBody) { body.Password = "" }, "password"},
		{"bad email", func(body *mailAccountCreateBody) { body.Email = "not-an-email" }, "email"},
	}
	for _, testCase := range cases {
		body := valid
		testCase.change(&body)
		if _, _, err := server.externalAccountRecordFromBody(context.Background(), user, body); err == nil ||
			!strings.Contains(strings.ToLower(err.Error()), testCase.want) {
			t.Errorf("%s: error = %v, want containing %q", testCase.name, err, testCase.want)
		}
	}
}

func TestIMAPActionCarriesConnectedAccountAndRemoteFolder(t *testing.T) {
	action := imapActionForMessage(map[string]any{
		"accountId": "acct-gmail", "messageId": "<m@example.test>", "imapUid": float64(42),
		"imapFolder": "[Gmail]/All Mail", "folder": "archive",
	})
	if action.AccountID != "acct-gmail" || action.MessageID != "<m@example.test>" ||
		action.SourceUID != 42 || action.SourceFolder != "[Gmail]/All Mail" ||
		action.SourceIMAPFolder != "[Gmail]/All Mail" {
		t.Fatalf("unexpected action: %#v", action)
	}
}
