package control

import "testing"

func TestValidWebPushEndpoint(t *testing.T) {
	for _, value := range []string{
		"https://fcm.googleapis.com/fcm/send/token",
		"https://updates.push.services.mozilla.com:443/wpush/v2/token",
	} {
		if !validWebPushEndpoint(value) {
			t.Fatalf("valid endpoint rejected: %s", value)
		}
	}
	for _, value := range []string{
		"http://example.com/push",
		"https://localhost/push",
		"https://127.0.0.1/push",
		"https://10.0.0.2/push",
		"https://example.com:8443/push",
		"https://user:pass@example.com/push",
		"",
	} {
		if validWebPushEndpoint(value) {
			t.Fatalf("unsafe endpoint accepted: %s", value)
		}
	}
}
