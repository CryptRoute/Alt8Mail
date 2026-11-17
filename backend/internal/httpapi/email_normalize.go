package httpapi

import (
	"fmt"
	"net/mail"
	"strings"
)

// normalizeEmailAddress lower-cases and validates a single email address.
func normalizeEmailAddress(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if email == "" {
		return "", fmt.Errorf("email is required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return "", fmt.Errorf("invalid email address")
	}
	return email, nil
}
