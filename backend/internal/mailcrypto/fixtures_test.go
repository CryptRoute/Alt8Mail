package mailcrypto

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureManifest struct {
	Fixtures []struct {
		Name        string `json:"name"`
		Protocol    string `json:"protocol"`
		Encrypted   bool   `json:"encrypted"`
		Signed      bool   `json:"signed"`
		Kind        string `json:"kind"`
		Description string `json:"description"`
	} `json:"fixtures"`
}

func TestFixtureCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "fixtures")
	rawManifest, err := os.ReadFile(filepath.Join(dir, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest fixtureManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Fixtures) < 8 {
		t.Fatalf("fixture manifest has only %d entries", len(manifest.Fixtures))
	}
	for _, expected := range manifest.Fixtures {
		t.Run(expected.Name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, expected.Name))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("PRIVATE KEY")) {
				t.Fatalf("%s contains private key material", expected.Name)
			}
			analysis, err := Analyze(raw)
			if err != nil {
				t.Fatal(err)
			}
			if analysis.Protection.Encrypted != expected.Encrypted {
				t.Fatalf("encrypted = %v, want %v (%#v)", analysis.Protection.Encrypted, expected.Encrypted, analysis.Protection)
			}
			if analysis.Protection.Signed != expected.Signed {
				t.Fatalf("signed = %v, want %v (%#v)", analysis.Protection.Signed, expected.Signed, analysis.Protection)
			}
			if string(analysis.Protection.Protocol) != expected.Protocol {
				t.Fatalf("protocol = %q, want %q", analysis.Protection.Protocol, expected.Protocol)
			}
			if string(analysis.Protection.Kind) != expected.Kind {
				t.Fatalf("kind = %q, want %q", analysis.Protection.Kind, expected.Kind)
			}
			if expected.Encrypted && analysis.Protection.EncryptedPart == nil && !analysis.Protection.HasEncryptedAttachment {
				t.Fatalf("encrypted fixture has no encrypted part: %#v", analysis.Protection)
			}
			if expected.Signed && analysis.Protection.SignedContent == nil {
				t.Fatalf("signed fixture has no signed content: %#v", analysis.Protection)
			}
		})
	}
}

func TestFixtureSignedEntitiesPreserveBytes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "pgp-mime-signed.eml"))
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.SignedContent == nil || analysis.Protection.Signature == nil {
		t.Fatal("signed parts missing")
	}
	if !strings.Contains(string(analysis.Protection.SignedContent.Raw), "Alt8Mail Phase 0 interop fixture.") {
		t.Fatalf("signed content = %q", analysis.Protection.SignedContent.Raw)
	}
	if !strings.Contains(string(analysis.Protection.Signature.Raw), "BEGIN PGP SIGNATURE") {
		t.Fatalf("signature = %q", analysis.Protection.Signature.Raw)
	}
	signed := SignedEntity(analysis.Protection.SignedContent)
	if !strings.HasSuffix(string(signed), "\r\n") {
		t.Fatal("signed entity does not end in CRLF")
	}
	if strings.Contains(string(signed), "\n") && strings.Contains(string(signed), "\r\n\r\n") == false {
		t.Fatal("signed entity was not canonicalized to CRLF")
	}
}

func TestFixtureSMIMESignedContentHasNoHeaderBlock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "smime-signed.eml"))
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Protection.SignedContent == nil {
		t.Fatal("signed content missing")
	}
	if len(analysis.Protection.SignedContent.RawHeaders) != 0 {
		t.Fatalf("expected a headerless OpenSSL signed entity, got %q", analysis.Protection.SignedContent.RawHeaders)
	}
	if !strings.Contains(string(analysis.Protection.SignedContent.RawBody), "Alt8Mail Phase 0 interop fixture.") {
		t.Fatalf("signed body = %q", analysis.Protection.SignedContent.RawBody)
	}
}
