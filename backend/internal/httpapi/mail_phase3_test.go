package httpapi

import "testing"

func TestWebPushNotificationURL(t *testing.T) {
	cases := map[string]string{
		webPushNotificationURL("https://mail.example.test/", "macct_1"): "https://mail.example.test/?account=macct_1#/Inbox",
		webPushNotificationURL("https://mail.example.test", ""):         "https://mail.example.test/#/Inbox",
		webPushNotificationURL("https://mail.example.test/", "macct/1"): "https://mail.example.test/?account=macct%2F1#/Inbox",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("push URL = %q, want %q", got, want)
		}
	}
}
