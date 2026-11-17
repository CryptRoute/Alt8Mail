package mailcrypto

import (
	"strings"
	"testing"
	"time"
)

func mimeMessage(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n"))
}

func TestAnalyzePlainMessage(t *testing.T) {
	raw := mimeMessage(
		"From: Elena Vasquez <elena@example.test>",
		"To: Avery Chen <avery@example.test>",
		"Subject: =?UTF-8?Q?Quarterly_plan_=E2=80=94_review?=",
		"Message-ID: <plain-1@example.test>",
		"Date: Wed, 28 Oct 2026 09:00:00 +0000",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"The plan is ready.",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.Encrypted || analysis.Protection.Signed {
		t.Fatalf("plain message detected as protected: %#v", analysis.Protection)
	}
	if analysis.Headers.Subject != "Quarterly plan — review" {
		t.Fatalf("subject = %q", analysis.Headers.Subject)
	}
	if len(analysis.Headers.From) != 1 || !strings.Contains(analysis.Headers.From[0], "elena@example.test") {
		t.Fatalf("from = %#v", analysis.Headers.From)
	}
	if len(analysis.Parts) != 1 || analysis.Parts[0].MediaType != "text/plain" {
		t.Fatalf("parts = %#v", analysis.Parts)
	}
}

func TestAnalyzeSMIMEEnveloped(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Encrypted invoice",
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m",
		"Content-Transfer-Encoding: base64",
		"",
		"MIIB...ciphertext...",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Signed {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.Protocol != ProtocolSMIME || analysis.Protection.Kind != KindSMIMEEnveloped {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.SMIME == nil || analysis.Protection.SMIME.Type != "enveloped-data" {
		t.Fatalf("S/MIME info = %#v", analysis.Protection.SMIME)
	}
}

func TestAnalyzeSMIMESigned(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Signed note",
		`Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary="signed-boundary"`,
		"",
		"--signed-boundary",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"The signed text.",
		"--signed-boundary",
		"Content-Type: application/pkcs7-signature; name=smime.p7s",
		"Content-Transfer-Encoding: base64",
		"",
		"MIIB...signature...",
		"--signed-boundary--",
		"",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Signed || analysis.Protection.Encrypted {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.Protocol != ProtocolSMIME || analysis.Protection.Kind != KindSMIMESigned {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.SignedContent == nil || analysis.Protection.Signature == nil {
		t.Fatalf("missing signed parts: %#v", analysis.Protection)
	}
	if !strings.Contains(string(analysis.Protection.SignedContent.Raw), "The signed text.") {
		t.Fatalf("signed content = %q", analysis.Protection.SignedContent.Raw)
	}
	if !strings.Contains(string(analysis.Protection.Signature.Raw), "application/pkcs7-signature") {
		t.Fatalf("signature part = %q", analysis.Protection.Signature.Raw)
	}
	if analysis.Protection.SMIME == nil || analysis.Protection.SMIME.DigestAlgorithm != "sha-256" {
		t.Fatalf("S/MIME info = %#v", analysis.Protection.SMIME)
	}
}

func TestAnalyzePGPMIMEEncrypted(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Encrypted note",
		`Content-Type: multipart/encrypted; protocol="application/pgp-encrypted"; boundary="pgp-boundary"`,
		"",
		"--pgp-boundary",
		"Content-Type: application/pgp-encrypted",
		"",
		"Version: 1",
		"--pgp-boundary",
		"Content-Type: application/octet-stream; name=encrypted.asc",
		"Content-Transfer-Encoding: base64",
		"",
		"hQEMA...ciphertext...",
		"--pgp-boundary--",
		"",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Signed {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.Protocol != ProtocolOpenPGP || analysis.Protection.Kind != KindPGPMIMEEncrypted {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.PGP == nil || analysis.Protection.PGP.Version != "1" {
		t.Fatalf("PGP info = %#v", analysis.Protection.PGP)
	}
	if analysis.Protection.EncryptedPart == nil || !strings.Contains(string(analysis.Protection.EncryptedPart.Raw), "hQEMA") {
		t.Fatalf("encrypted part = %#v", analysis.Protection.EncryptedPart)
	}
}

func TestAnalyzePGPMIMESigned(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Signed note",
		`Content-Type: multipart/signed; protocol="application/pgp-signature"; micalg=pgp-sha256; boundary="pgp-signed"`,
		"",
		"--pgp-signed",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"The signed text.",
		"--pgp-signed",
		"Content-Type: application/pgp-signature; name=signature.asc",
		"",
		"-----BEGIN PGP SIGNATURE-----",
		"",
		"abc",
		"-----END PGP SIGNATURE-----",
		"--pgp-signed--",
		"",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Signed || analysis.Protection.Encrypted {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.Protocol != ProtocolOpenPGP || analysis.Protection.Kind != KindPGPMIMESigned {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.PGP == nil || analysis.Protection.SignedContent == nil {
		t.Fatalf("PGP info = %#v", analysis.Protection)
	}
}

func TestAnalyzePGPInline(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Inline PGP",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"-----BEGIN PGP MESSAGE-----",
		"Version: 1",
		"",
		"hQEMA...",
		"-----END PGP MESSAGE-----",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Protocol != ProtocolOpenPGP || analysis.Protection.Kind != KindPGPInlineEncrypted {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.PGP == nil || !analysis.Protection.PGP.Inline {
		t.Fatalf("PGP info = %#v", analysis.Protection.PGP)
	}
}

func TestAnalyzeSignedWithEncryptedContent(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Signed and encrypted",
		`Content-Type: multipart/signed; protocol="application/pkcs7-signature"; boundary="outer-signed"`,
		"",
		"--outer-signed",
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m",
		"Content-Transfer-Encoding: base64",
		"",
		"MIIB...ciphertext...",
		"--outer-signed",
		"Content-Type: application/pkcs7-signature",
		"",
		"MIIB...signature...",
		"--outer-signed--",
		"",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.Protection.Encrypted || !analysis.Protection.Signed {
		t.Fatalf("protection = %#v", analysis.Protection)
	}
	if analysis.Protection.Kind != KindSMIMESignedEncrypted {
		t.Fatalf("kind = %q", analysis.Protection.Kind)
	}
}

func TestEncryptedAttachmentDoesNotMarkBodyEncrypted(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Encrypted attachment",
		`Content-Type: multipart/mixed; boundary="mixed"`,
		"",
		"--mixed",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"See the attached encrypted message.",
		"--mixed",
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m",
		"Content-Disposition: attachment; filename=smime.p7m",
		"Content-Transfer-Encoding: base64",
		"",
		"MIIB...ciphertext...",
		"--mixed--",
		"",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.Encrypted || analysis.Protection.Signed {
		t.Fatalf("body protection = %#v", analysis.Protection)
	}
	if !analysis.Protection.HasEncryptedAttachment {
		t.Fatalf("encrypted attachment not detected: %#v", analysis.Protection)
	}
	foundWarning := false
	for _, warning := range analysis.Warnings {
		if strings.Contains(warning, "encrypted attachment") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("no attachment warning: %#v", analysis.Warnings)
	}
}

func TestMalformedMultipartIsReportedNotPanicked(t *testing.T) {
	raw := mimeMessage(
		"From: sender@example.test",
		"To: recipient@example.test",
		"Subject: Broken signature",
		`Content-Type: multipart/signed; protocol="application/pgp-signature"; boundary="broken"`,
		"",
		"--broken",
		"Content-Type: text/plain",
		"",
		"Only one part.",
	)
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.Kind != KindMalformed {
		t.Fatalf("kind = %q", analysis.Protection.Kind)
	}
	if len(analysis.Warnings) == 0 {
		t.Fatal("expected a malformed multipart warning")
	}
}

func TestRawMultipartSegmentsPreserveExactBytes(t *testing.T) {
	body := []byte("preamble\r\n--b\r\nContent-Type: text/plain\r\n\r\nhello\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>hi</p>\r\n--b--\r\nepilogue")
	segments, closed, err := rawMultipartSegments(body, "b")
	if err != nil {
		t.Fatal(err)
	}
	if !closed || len(segments) != 2 {
		t.Fatalf("segments = %d closed=%v", len(segments), closed)
	}
	if string(segments[0]) != "Content-Type: text/plain\r\n\r\nhello" {
		t.Fatalf("first segment = %q", segments[0])
	}
	if string(segments[1]) != "Content-Type: text/html\r\n\r\n<p>hi</p>" {
		t.Fatalf("second segment = %q", segments[1])
	}
}

func TestCanonicalizeCRLF(t *testing.T) {
	if got := string(CanonicalizeCRLF([]byte("a\nb\r\nc\rd"))); got != "a\r\nb\r\nc\r\nd" {
		t.Fatalf("canonical = %q", got)
	}
	if got := string(EnsureTrailingCRLF([]byte("a\r\n\r\n"))); got != "a\r\n" {
		t.Fatalf("trailing = %q", got)
	}
}

func TestPolicyValidation(t *testing.T) {
	policy := Policy{Mode: ModeServerManaged, AllowedProtocols: []Protocol{ProtocolOpenPGP}, RequireVerifiedRecipient: true}
	recipient := RecipientKey{Protocol: ProtocolOpenPGP, Trust: TrustVerified, NotAfter: time.Now().Add(time.Hour)}
	if reason := policy.ValidateOutbound(Protection{Encrypted: true, Protocol: ProtocolOpenPGP}, []RecipientKey{recipient}); reason != "" {
		t.Fatalf("valid message rejected: %s", reason)
	}
	if reason := policy.ValidateOutbound(Protection{Encrypted: true, Protocol: ProtocolSMIME}, []RecipientKey{recipient}); reason == "" {
		t.Fatal("protocol mismatch was accepted")
	}
	untrusted := recipient
	untrusted.Trust = TrustUnknown
	if reason := policy.ValidateOutbound(Protection{Encrypted: true, Protocol: ProtocolOpenPGP}, []RecipientKey{untrusted}); reason == "" {
		t.Fatal("unverified recipient was accepted")
	}
	revoked := recipient
	revoked.Revoked = true
	if reason := policy.ValidateOutbound(Protection{Encrypted: true, Protocol: ProtocolOpenPGP}, []RecipientKey{revoked}); reason == "" {
		t.Fatal("revoked recipient key was accepted")
	}
}
