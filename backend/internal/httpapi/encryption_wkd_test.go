package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

func TestFetchWKDKeyValidatesRecipient(t *testing.T) {
	key, err := mailcrypto.GenerateOpenPGPKey("Alice", "alice@example.test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pgp-keys")
		_, _ = w.Write([]byte(key.ArmoredPublic))
	}))
	defer server.Close()
	api := &Server{wkdHTTPClient: server.Client(), wkdAllowPrivate: true}
	info, _, err := api.fetchWKDKey(context.Background(), "alice@example.test", []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != key.Fingerprint || info.KeyID != key.KeyID {
		t.Fatalf("WKD info = %#v, want %#v", info, key)
	}
	if _, _, err := api.fetchWKDKey(context.Background(), "mallory@example.test", []string{server.URL}); err == nil {
		t.Fatal("WKD accepted a key for a different recipient")
	}
}

func TestFetchWKDKeyRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxWKDResponseBytes+1))
	}))
	defer server.Close()
	api := &Server{wkdHTTPClient: server.Client(), wkdAllowPrivate: true}
	if _, _, err := api.fetchWKDKey(context.Background(), "alice@example.test", []string{server.URL}); err == nil {
		t.Fatal("oversized WKD response was accepted")
	}
}

func TestWKDHostRejectsPrivateAddresses(t *testing.T) {
	api := &Server{}
	if api.wkdHostAllowed(context.Background(), "localhost") {
		t.Fatal("localhost was accepted")
	}
	if api.wkdHostAllowed(context.Background(), "127.0.0.1") {
		t.Fatal("loopback was accepted")
	}
}
