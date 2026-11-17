package httpapi

import "testing"

type oidcTestAudit struct {
	userID string
	action string
	target string
	detail string
}

func stabilizeConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ALT8MAIL_ENV", "development")
	t.Setenv("ALT8MAIL_COOKIE_SECURE", "false")
	t.Setenv("ALT8MAIL_DB_MAX_CONNS", "20")
	t.Setenv("ALT8MAIL_DEMO_MODE", "false")
	t.Setenv("ALT8MAIL_MEMORY_LIMIT_MB", "512")
	t.Setenv("ALT8MAIL_SESSION_SECRET", "test-session-secret")
	t.Setenv("ALT8MAIL_CSRF_SECRET", "test-csrf-secret")
}
