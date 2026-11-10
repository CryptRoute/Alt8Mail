package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMailImportOAuthTokenAndProfile(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, "bad refresh", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"refreshed-token"}`)
		case "/gmail/v1/users/me/profile":
			if r.Header.Get("Authorization") != "Bearer refreshed-token" {
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"emailAddress":"Migrated@Example.test"}`)
		case "/v1.0/me":
			if r.Header.Get("Authorization") != "Bearer graph-token" {
				http.Error(w, "bad auth", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"userPrincipalName":"graph@example.test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	server := &Server{importHTTPClient: fake.Client(), gmailBaseURL: fake.URL, gmailTokenURL: fake.URL + "/token", graphBaseURL: fake.URL + "/v1.0"}
	token, err := server.importOAuthAccessToken(context.Background(), "gmail", `{"refreshToken":"refresh-1","clientId":"client","clientSecret":"secret"}`)
	if err != nil || token != "refreshed-token" {
		t.Fatalf("refresh token=%q err=%v", token, err)
	}
	profile, err := server.importOAuthProfile(context.Background(), "gmail", token)
	if err != nil || profile != "migrated@example.test" {
		t.Fatalf("gmail profile=%q err=%v", profile, err)
	}
	profile, err = server.importOAuthProfile(context.Background(), "graph", "graph-token")
	if err != nil || profile != "graph@example.test" {
		t.Fatalf("graph profile=%q err=%v", profile, err)
	}
	if raw, err := server.importOAuthAccessToken(context.Background(), "graph", "graph-token"); err != nil || raw != "graph-token" {
		t.Fatalf("raw token=%q err=%v", raw, err)
	}
	if _, err := server.importOAuthAccessToken(context.Background(), "gmail", `{"refreshToken":"wrong","clientId":"client","clientSecret":"secret"}`); err == nil {
		t.Fatal("expected a rejected refresh token")
	}
}

func TestMailImportProviderFolderMapping(t *testing.T) {
	cases := map[string]struct {
		folder string
		flags  []string
	}{
		"INBOX":   {"INBOX", []string{`\Seen`}},
		"SENT":    {"Sent", []string{`\Seen`}},
		"DRAFT":   {"Drafts", []string{`\Seen`}},
		"TRASH":   {"Trash", []string{`\Seen`}},
		"SPAM":    {"Junk", []string{`\Seen`}},
		"UNREAD":  {"INBOX", nil},
		"STARRED": {"INBOX", []string{`\Flagged`, `\Seen`}},
	}
	for label, want := range cases {
		folder, flags := gmailImportFolder([]string{label})
		if folder != want.folder || strings.Join(flags, ",") != strings.Join(want.flags, ",") {
			t.Fatalf("%s -> %s %v, want %s %v", label, folder, flags, want.folder, want.flags)
		}
	}
	if name := graphFolderName("sentitems", "Sent Items"); name != "Sent" {
		t.Fatalf("sentitems -> %q", name)
	}
	if name := graphFolderName("", "Projects"); name != "Projects" {
		t.Fatalf("custom folder -> %q", name)
	}
	folder, flags := gmailImportFolder([]string{"INBOX", "UNREAD"})
	if folder != "INBOX" || len(flags) != 0 {
		t.Fatalf("unread INBOX -> %s %v", folder, flags)
	}
}

func TestGraphNextLinkValidation(t *testing.T) {
	server := &Server{graphBaseURL: "https://graph.example.test/v1.0"}
	if !server.graphNextLinkAllowed("https://graph.example.test/v1.0/me/messages?$skiptoken=1") {
		t.Fatal("expected the configured Graph host to be allowed")
	}
	if server.graphNextLinkAllowed("https://evil.example.test/v1.0/me/messages") {
		t.Fatal("expected an unrelated host to be rejected")
	}
}
