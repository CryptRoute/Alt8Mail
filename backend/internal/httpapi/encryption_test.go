package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestMailCryptoVaultRoundTripAndAAD(t *testing.T) {
	vault := newMailCryptoVault(strings.Repeat("crypto-secret-", 3), strings.Repeat("previous-secret-", 3))
	if vault == nil {
		t.Fatal("vault was not created")
	}
	sealed, err := vault.seal("tenant-a", "user-a", "identity-a", "-----BEGIN PGP PRIVATE KEY BLOCK-----")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "PGP PRIVATE KEY") {
		t.Fatal("wrapped key contains plaintext key material")
	}
	opened, err := vault.open("tenant-a", "user-a", "identity-a", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(opened, "-----BEGIN PGP PRIVATE KEY BLOCK-----") {
		t.Fatalf("opened = %q", opened)
	}
	if _, err := vault.open("tenant-b", "user-a", "identity-a", sealed); err == nil {
		t.Fatal("the tenant AAD must not open under another tenant")
	}
	if _, err := newMailCryptoVault(strings.Repeat("different-secret-", 3), "").open("tenant-a", "user-a", "identity-a", sealed); err == nil {
		t.Fatal("a different vault key must not open the wrapped key")
	}
}

func TestTenantEncryptionPolicyValidation(t *testing.T) {
	policy := defaultTenantPolicy()
	if policy.Encryption.Mode != "disabled" || policy.Encryption.Protocol != "openpgp" {
		t.Fatalf("default encryption policy = %#v", policy.Encryption)
	}
	policy.Encryption.Mode = "server_managed"
	updated, errors := validateTenantPolicy(policy)
	if len(errors) != 0 {
		t.Fatalf("valid encryption policy rejected: %v", errors)
	}
	if updated.Encryption.AllowDelegatedDecrypt {
		t.Fatal("delegated decryption must default to disabled")
	}
	policy.Encryption.AllowDelegatedDecrypt = true
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("delegated decryption setting rejected: %v", errors)
	}
	if updated.Encryption.Mode != "server_managed" {
		t.Fatalf("mode = %q", updated.Encryption.Mode)
	}
	policy.Encryption.Mode = "client_side"
	if _, errors := validateTenantPolicy(policy); len(errors) == 0 {
		t.Fatal("unsupported encryption mode was accepted")
	}
	policy.Encryption.Mode = "server_managed"
	policy.Encryption.Protocol = "smime"
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("S/MIME protocol rejected: %v", errors)
	}
	policy.Encryption.Protocol = "both"
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("dual protocol rejected: %v", errors)
	}
	if got := encryptionProtocols(policy.Encryption); len(got) != 2 {
		t.Fatalf("dual protocol set = %#v", got)
	}
	policy.Encryption.Protocol = "pem"
	if _, errors := validateTenantPolicy(policy); len(errors) == 0 {
		t.Fatal("unsupported protocol was accepted")
	}
	policy.Encryption.Mode = "client_side"
	policy.Encryption.Protocol = "openpgp"
	policy.Encryption.AllowDelegatedDecrypt = false
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("client-side policy rejected: %v", errors)
	}
	if got := encryptionProtocols(policy.Encryption); len(got) != 1 || got[0] != "openpgp" {
		t.Fatalf("client-side protocols = %#v", got)
	}
	policy.Encryption.Protocol = "smime"
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("client-side S/MIME protocol rejected: %v", errors)
	}
	policy.Encryption.Protocol = "both"
	if _, errors := validateTenantPolicy(policy); len(errors) != 0 {
		t.Fatalf("client-side dual protocol rejected: %v", errors)
	}
	if got := encryptionProtocols(policy.Encryption); len(got) != 2 {
		t.Fatalf("client-side dual protocols = %#v", got)
	}
	policy.Encryption.Protocol = "openpgp"
	policy.Encryption.AllowDelegatedDecrypt = true
	if _, errors := validateTenantPolicy(policy); len(errors) == 0 {
		t.Fatal("client-side delegated decryption was accepted")
	}
}

func TestSelectRecipientKeyTrustAndRotation(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	key := func(trust string, changed, notAfter *time.Time) storedRecipientKey {
		return storedRecipientKey{
			publicKey: "key-" + trust, keyID: trust, fingerprint: trust,
			trust: trust, keyChangedAt: changed, notAfter: notAfter,
		}
	}
	selected, err := selectRecipientKey("alice@example.test", []storedRecipientKey{
		key("verified", nil, nil), key("unknown", &past, nil),
	}, now)
	if err != nil || selected.trust != "verified" {
		t.Fatalf("verified key selection = %#v, %v", selected, err)
	}
	if _, err := selectRecipientKey("alice@example.test", []storedRecipientKey{
		key("tofu", nil, nil), key("unknown", &past, nil),
	}, now); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("unverified changed key was accepted: %v", err)
	}
	if _, err := selectRecipientKey("alice@example.test", []storedRecipientKey{
		key("unknown", &past, nil),
	}, now); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("only changed key was accepted: %v", err)
	}
	selected, err = selectRecipientKey("alice@example.test", []storedRecipientKey{
		key("verified", nil, &past), key("tofu", nil, &future),
	}, now)
	if err != nil || selected.trust != "tofu" {
		t.Fatalf("expired key fallback = %#v, %v", selected, err)
	}
	if _, err := selectRecipientKey("alice@example.test", []storedRecipientKey{
		key("verified", nil, &past),
	}, now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired key was accepted: %v", err)
	}
	if _, err := selectRecipientKey("alice@example.test", nil, now); err == nil || !strings.Contains(err.Error(), "no encryption key") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestEncryptionVaultRequiresProductionSecret(t *testing.T) {
	api := &Server{cfg: config.Config{Env: "production", SessionSecret: strings.Repeat("session-", 3)}}
	if _, err := api.encryptionVault(); err == nil {
		t.Fatal("production accepted a session-secret fallback for private mail keys")
	}
	api.cfg.CryptoSecret = strings.Repeat("crypto-", 3)
	if _, err := api.encryptionVault(); err != nil {
		t.Fatalf("production crypto secret rejected: %v", err)
	}
}

func TestPublicOutboxItemMarksClientSideCiphertext(t *testing.T) {
	item := map[string]any{
		"id": "send_client", "subject": "Encrypted", "sendAt": time.Now().Add(time.Minute).Format(time.RFC3339),
		"clientMime":    "MIME-Version: 1.0\r\nContent-Type: multipart/encrypted\r\n\r\n",
		"cryptoEncrypt": true, "cryptoProtocol": "openpgp",
	}
	payload := publicOutboxItem(item)
	if payload["clientSide"] != true {
		t.Fatalf("clientSide = %#v; a queued item with ciphertext must never be offered as restorable", payload["clientSide"])
	}
	if strings.Contains(payload["bodyText"].(string), "plaintext") {
		t.Fatal("client-side queued payload leaked a body")
	}
}

func TestEncryptionIdentityMatchesMode(t *testing.T) {
	cases := []struct {
		source, mode string
		want         bool
	}{
		{"client", "client_side", true},
		{"generated", "client_side", false},
		{"imported", "client_side", false},
		{"client", "server_managed", false},
		{"generated", "server_managed", true},
		{"imported", "server_managed", true},
		{"generated", "disabled", false},
	}
	for _, test := range cases {
		if got := encryptionIdentityMatchesMode(test.source, test.mode); got != test.want {
			t.Fatalf("(%s, %s) = %v, want %v", test.source, test.mode, got, test.want)
		}
	}
}
