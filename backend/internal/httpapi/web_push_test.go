package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidPushEndpoint(t *testing.T) {
	valid := []string{
		"https://fcm.googleapis.com/fcm/send/abc123",
		"https://updates.push.services.mozilla.com/wpush/v2/token",
		"https://example.com:443/push/token",
	}
	for _, value := range valid {
		if _, err := validPushEndpoint(value); err != nil {
			t.Fatalf("valid endpoint %q rejected: %v", value, err)
		}
	}
	invalid := []string{
		"http://example.com/push",
		"https://example.com:8443/push",
		"https://localhost/push",
		"https://127.0.0.1/push",
		"https://10.0.0.1/push",
		"https://100.64.0.1/push",
		"https://user:pass@example.com/push",
		"not a url",
	}
	for _, value := range invalid {
		if _, err := validPushEndpoint(value); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", value)
		}
	}
}

func TestPushSubscriptionJSONAcceptsExpirationTime(t *testing.T) {
	payload := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc123","expirationTime":null,"keys":{"p256dh":"cF9rZXk=","auth":"YXV0aA=="}}`
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	var input webPushSubscriptionInput
	if err := decoder.Decode(&input); err != nil {
		t.Fatalf("browser subscription payload rejected: %v", err)
	}
	if input.Endpoint == "" || input.Keys.P256dh == "" || input.Keys.Auth == "" {
		t.Fatalf("browser subscription fields were not decoded: %+v", input)
	}
	if input.ExpirationTime != nil {
		t.Fatalf("null expirationTime should decode as nil, got %v", *input.ExpirationTime)
	}

	payload = `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc123","expirationTime":1767225600000,"keys":{"p256dh":"cF9rZXk=","auth":"YXV0aA=="}}`
	decoder = json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		t.Fatalf("browser subscription with an expiration rejected: %v", err)
	}
	if input.ExpirationTime == nil || *input.ExpirationTime != 1767225600000 {
		t.Fatalf("expirationTime was not decoded: %+v", input.ExpirationTime)
	}
}

func TestValidPushKey(t *testing.T) {
	p256dh := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("p", 65)))
	auth := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 16)))
	if !validPushKey(p256dh) {
		t.Fatal("valid p256dh key rejected")
	}
	if !validPushKey(auth) {
		t.Fatal("valid auth key rejected")
	}
	if validPushKey("not-base64!") {
		t.Fatal("invalid key accepted")
	}
	if validPushKey(base64.RawURLEncoding.EncodeToString([]byte("short"))) {
		t.Fatal("short key accepted")
	}
}

func TestQuietHourNow(t *testing.T) {
	settings := webPushSettings{QuietHoursEnabled: true, QuietStart: 22, QuietEnd: 7}
	cases := []struct {
		hour int
		want bool
	}{
		{21, false}, {22, true}, {23, true}, {0, true}, {6, true}, {7, false}, {12, false},
	}
	for _, item := range cases {
		now := time.Date(2026, 11, 2, item.hour, 30, 0, 0, time.UTC)
		if got := quietHourNow(settings, now); got != item.want {
			t.Fatalf("hour %d: got %v want %v", item.hour, got, item.want)
		}
	}
	if quietHourNow(webPushSettings{QuietHoursEnabled: true, QuietStart: 8, QuietEnd: 17}, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)) != true {
		t.Fatal("same-day quiet window was not respected")
	}
	if quietHourNow(webPushSettings{QuietHoursEnabled: true, QuietStart: 22, QuietEnd: 22}, time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)) {
		t.Fatal("empty quiet window should not suppress notifications")
	}
}
