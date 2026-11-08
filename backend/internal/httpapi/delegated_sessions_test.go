package httpapi

import (
	"net/http"
	"testing"
)

func TestDelegatedSessionAllowedPaths(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/bootstrap", true},
		{http.MethodPost, "/api/send", true},
		{http.MethodPost, "/api/messages/read", true},
		{http.MethodDelete, "/api/delegation/session", true},
		{http.MethodPost, "/api/delegation/launch", false},
		{http.MethodPost, "/api/delegation/redeem", false},
		{http.MethodPost, "/api/password/change", false},
		{http.MethodPost, "/api/security/2fa/totp/begin", false},
		{http.MethodGet, "/api/security/sessions", false},
		{http.MethodPatch, "/api/preferences", false},
		{http.MethodGet, "/api/preferences", true},
		{http.MethodPost, "/api/push/subscribe", false},
		{http.MethodGet, "/api/push/config", true},
		{http.MethodPut, "/api/storage/credentials/dropbox", false},
		{http.MethodGet, "/api/accounts", false},
		{http.MethodPost, "/api/accounts", false},
		{http.MethodGet, "/api/mail/oauth/gmail/start", false},
		{http.MethodPut, "/api/storage/default", false},
		{http.MethodGet, "/api/storage/quota", true},
		{http.MethodPost, "/api/files/file-1/share", false},
		{http.MethodDelete, "/api/file-shares/share-1", false},
		{http.MethodPost, "/api/calendar/calendars/cal-1/share", false},
		{http.MethodDelete, "/api/calendar/calendars/cal-1/share/user-1", false},
		{http.MethodPost, "/api/calendar/calendars/cal-1/publish", false},
		{http.MethodPost, "/api/mail/sync", false},
		{http.MethodGet, "/api/mail/sync", true},
		{http.MethodGet, "/api/addons", false},
		{http.MethodGet, "/api/office/config", false},
		{http.MethodGet, "/api/audit", false},
		{http.MethodGet, "/api/audit.csv", false},
		{http.MethodPost, "/api/dev/reset", false},
	}
	for _, tc := range cases {
		if got := delegatedSessionAllowed(tc.method, tc.path); got != tc.want {
			t.Errorf("delegatedSessionAllowed(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestDelegatedReadOnlyBlocksSending(t *testing.T) {
	sendPaths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/send"},
		{http.MethodPost, "/api/outbox/abc/send-now"},
		{http.MethodPatch, "/api/outbox/abc"},
		{http.MethodPost, "/api/reactions"},
		{http.MethodPost, "/api/messages/abc/reaction"},
		{http.MethodPost, "/api/reaction-rules"},
		{http.MethodPut, "/api/sieve"},
		{http.MethodPost, "/api/calendar/events"},
		{http.MethodPatch, "/api/calendar/events/abc"},
		{http.MethodPost, "/api/calendar/events/abc/rsvp"},
	}
	for _, tc := range sendPaths {
		if !delegatedSendPath(tc.method, tc.path) {
			t.Errorf("delegatedSendPath(%s %s) = false, want true", tc.method, tc.path)
		}
	}
	if delegatedSendPath(http.MethodGet, "/api/calendar/events") {
		t.Error("reading calendar events must remain available to read-only delegates")
	}
	if delegatedSendPath(http.MethodPost, "/api/messages/read") {
		t.Error("marking a message read must not be treated as sending")
	}
}
