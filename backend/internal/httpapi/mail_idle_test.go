package httpapi

import (
	"testing"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

func TestIgnoreIDLEUpdate(t *testing.T) {
	cases := []struct {
		name   string
		update imapclient.Update
		ignore bool
	}{
		{"keepalive ok", &imapclient.StatusUpdate{Status: &imap.StatusResp{Type: imap.StatusRespOk}}, true},
		{"warning no", &imapclient.StatusUpdate{Status: &imap.StatusResp{Type: imap.StatusRespNo}}, true},
		{"warning bad", &imapclient.StatusUpdate{Status: &imap.StatusResp{Type: imap.StatusRespBad}}, true},
		{"bye", &imapclient.StatusUpdate{Status: &imap.StatusResp{Type: imap.StatusRespBye}}, false},
		{"mailbox update", &imapclient.MailboxUpdate{Mailbox: &imap.MailboxStatus{}}, false},
		{"expunge", &imapclient.ExpungeUpdate{SeqNum: 3}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ignoreIDLEUpdate(tc.update); got != tc.ignore {
				t.Fatalf("ignoreIDLEUpdate(%T) = %v, want %v", tc.update, got, tc.ignore)
			}
		})
	}
}

func TestIdleEventForUpdate(t *testing.T) {
	cases := []struct {
		name   string
		update imapclient.Update
		event  idleEvent
	}{
		{"exists means new mail", &imapclient.MailboxUpdate{Mailbox: &imap.MailboxStatus{Messages: 9}}, idleEventNewMail},
		{"expunge", &imapclient.ExpungeUpdate{SeqNum: 3}, idleEventFlags},
		{"fetch", &imapclient.MessageUpdate{Message: &imap.Message{SeqNum: 2}}, idleEventFlags},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idleEventForUpdate(tc.update); got != tc.event {
				t.Fatalf("idleEventForUpdate(%T) = %v, want %v", tc.update, got, tc.event)
			}
		})
	}
}
