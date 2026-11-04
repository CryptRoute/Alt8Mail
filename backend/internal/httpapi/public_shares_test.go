package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestShareRecipientCookieRequiresMatchingEmailOnDevice(t *testing.T) {
	server := &Server{cfg: config.Config{SessionSecret: "session-secret", CSRFSecret: "csrf-secret"}}
	share := publicShare{ShareID: "share-1", Recipient: "person@example.com"}
	token := "share-token"
	value := server.shareAccessValue(share, share.Recipient)
	request := httptest.NewRequest("GET", "/files/s/"+token, nil)
	request.AddCookie(&http.Cookie{Name: server.shareAccessCookieName(token), Value: value})
	email, ok := server.shareAccessEmail(request, token, share)
	if !ok || email != share.Recipient {
		t.Fatalf("verified email = %q, ok=%v", email, ok)
	}
	if !server.shareVisibleToRequester(request, token, share) {
		t.Fatal("verified recipient was not allowed to view the share")
	}

	wrong := publicShare{ShareID: "share-1", Recipient: "other@example.com"}
	if _, ok := server.shareAccessEmail(request, token, wrong); ok {
		t.Fatal("a cookie for one recipient unlocked another recipient's link")
	}
	tampered := httptest.NewRequest("GET", "/files/s/"+token, nil)
	tampered.AddCookie(&http.Cookie{Name: server.shareAccessCookieName(token), Value: value + "tampered"})
	if _, ok := server.shareAccessEmail(tampered, token, share); ok {
		t.Fatal("a tampered access cookie was accepted")
	}

	locked := share
	locked.Locked = true
	if server.shareVisibleToRequester(request, token, locked) {
		t.Fatal("a locked share exposed its file")
	}
}
