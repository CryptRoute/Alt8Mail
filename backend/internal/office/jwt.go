package office

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SignJWT signs an OnlyOffice editor, callback or conversion payload with
// HS256. The shared secret is the one written by the Office agent.
func SignJWT(secret string, claims map[string]any, ttl time.Duration) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("office JWT secret is unavailable")
	}
	payload := jwt.MapClaims{}
	for key, value := range claims {
		payload[key] = value
	}
	now := time.Now()
	payload["iat"] = now.Unix()
	payload["exp"] = now.Add(ttl).Unix()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, payload).SignedString([]byte(secret))
}

// VerifyJWT checks the signature and, when present, the expiration claim.
// OnlyOffice does not always add exp to callback tokens, so its absence is
// accepted; the signed callback URL still carries its own expiry.
func VerifyJWT(secret, raw string) (map[string]any, error) {
	if secret == "" || raw == "" {
		return nil, fmt.Errorf("office JWT token is missing")
	}
	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithLeeway(30*time.Second),
	)
	token, err := parser.ParseWithClaims(raw, claims, func(parsed *jwt.Token) (any, error) {
		if parsed.Method.Alg() != "HS256" {
			return nil, fmt.Errorf("unexpected office JWT algorithm")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("office JWT verification failed: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("office JWT is not valid")
	}
	return claims, nil
}

// ClaimInt reads a numeric JWT claim that may arrive as float64 or json.Number.
func ClaimInt(claims map[string]any, key string) (int, bool) {
	value, ok := claims[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	default:
		return 0, false
	}
}

// ClaimString reads a string JWT claim.
func ClaimString(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}
