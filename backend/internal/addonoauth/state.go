// Package addonoauth contains the signed state shared by the webmail OAuth
// gateway and the managed add-on relay. The state is deliberately small and
// contains no provider tokens.
package addonoauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type State struct {
	TenantID  string `json:"tenantId"`
	AddonID   string `json:"addonId"`
	UserID    string `json:"userId"`
	Scope     string `json:"scope"`
	Mode      string `json:"mode"`
	Callback  string `json:"callback,omitempty"`
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expiresAt"`
}

var (
	ErrInvalidState = errors.New("invalid add-on OAuth state")
	ErrExpiredState = errors.New("expired add-on OAuth state")
)

func Sign(secret string, state State) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("add-on OAuth secret is not configured")
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + signature(secret, encoded), nil
}

func Parse(secret, raw string) (State, error) {
	if strings.TrimSpace(secret) == "" {
		return State{}, ErrInvalidState
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return State{}, ErrInvalidState
	}
	expected := signature(secret, parts[0])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return State{}, ErrInvalidState
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return State{}, ErrInvalidState
	}
	var state State
	if err := json.Unmarshal(payload, &state); err != nil {
		return State{}, ErrInvalidState
	}
	if state.TenantID == "" || state.AddonID == "" || state.UserID == "" || state.Nonce == "" {
		return State{}, ErrInvalidState
	}
	if state.ExpiresAt <= time.Now().Unix() {
		return State{}, ErrExpiredState
	}
	return state, nil
}

func signature(secret, encoded string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
