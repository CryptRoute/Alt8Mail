package office

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	TokenKindContent  = "content"
	TokenKindCallback = "callback"
)

// URLToken is a short-lived, HMAC-signed capability used for the URLs that the
// OnlyOffice Document Server fetches or calls without a browser session.
type URLToken struct {
	Kind         string `json:"k"`
	DocumentID   string `json:"d,omitempty"`
	FileID       string `json:"f,omitempty"`
	UserID       string `json:"u"`
	TenantID     string `json:"t"`
	Key          string `json:"key,omitempty"`
	ShareID      string `json:"s,omitempty"`
	GuestEmail   string `json:"ge,omitempty"`
	EditorUserID string `json:"eu,omitempty"`
	Actor        string `json:"a,omitempty"`
	ExpiresAt    int64  `json:"exp"`
}

func signingKey(secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("a8m-office-signed-url-v1"))
	return mac.Sum(nil)
}

// SignURLToken creates a URL-safe token in the form payload.signature.
func SignURLToken(secret string, token URLToken) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("office signing secret is unavailable")
	}
	if token.Kind != TokenKindContent && token.Kind != TokenKindCallback {
		return "", fmt.Errorf("invalid token kind")
	}
	if token.UserID == "" || token.TenantID == "" || token.ExpiresAt == 0 {
		return "", fmt.Errorf("incomplete token")
	}
	payload, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, signingKey(secret))
	mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

// VerifyURLToken validates the signature and expiry before returning the
// capability. The comparison is constant time.
func VerifyURLToken(secret, raw string, now time.Time) (URLToken, error) {
	var token URLToken
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return token, fmt.Errorf("invalid signed URL")
	}
	mac := hmac.New(sha256.New, signingKey(secret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return token, fmt.Errorf("invalid signed URL")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return token, fmt.Errorf("invalid signed URL")
	}
	if err := json.Unmarshal(payload, &token); err != nil {
		return token, fmt.Errorf("invalid signed URL")
	}
	if token.Kind != TokenKindContent && token.Kind != TokenKindCallback {
		return token, fmt.Errorf("invalid signed URL")
	}
	if token.ExpiresAt < now.Unix() {
		return token, fmt.Errorf("signed URL has expired")
	}
	if token.UserID == "" || token.TenantID == "" {
		return token, fmt.Errorf("invalid signed URL")
	}
	return token, nil
}
