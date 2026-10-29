package httpapi

import (
	"testing"

	"github.com/meridian-mail/meridian/backend/internal/auth"
	"github.com/meridian-mail/meridian/backend/internal/config"
)

func TestVerifyAccountPasswordDemoFallback(t *testing.T) {
	hash, err := auth.HashPassword("account-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	server := &Server{cfg: config.Config{
		DemoMode:      true,
		DemoEmail:     "avery.chen@meridian.example",
		DemoPassword:  "demo-password",
		DemoUserEmail: "avery.chen@meridian.test",
	}}
	sess := &sessionRecord{User: userRecord{Email: "avery.chen@meridian.test", PasswordHash: hash}}
	if !server.verifyAccountPassword(sess, "account-password") {
		t.Fatal("real account password was rejected")
	}
	if !server.verifyAccountPassword(sess, "demo-password") {
		t.Fatal("demo password should be accepted for the mapped demo account")
	}
	if server.verifyAccountPassword(sess, "wrong-password") {
		t.Fatal("wrong password was accepted")
	}
	other := &sessionRecord{User: userRecord{Email: "jordan.lee@meridian.test", PasswordHash: hash}}
	if server.verifyAccountPassword(other, "demo-password") {
		t.Fatal("demo password was accepted for a non-demo account")
	}
	server.cfg.DemoMode = false
	if server.verifyAccountPassword(sess, "demo-password") {
		t.Fatal("demo password was accepted while demo mode is disabled")
	}
}
