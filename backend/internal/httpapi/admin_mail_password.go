package httpapi

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// hashMailPassword creates the SHA512-CRYPT hash format read by the deployed
// Dovecot SQL passdb. OpenSSL is used instead of doveadm because the webmail
// service runs as the unprivileged a8m user and cannot read the root-only
// Dovecot drop-in configuration that doveadm parses. The password never touches
// a shell or the process table; it is written to stdin.
func hashMailPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("mailbox password is empty")
	}
	if strings.ContainsAny(password, "\r\n\x00") {
		return "", fmt.Errorf("mailbox password must not contain line breaks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "openssl", "passwd", "-6", "-stdin")
	command.Stdin = strings.NewReader(password + "\n")
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("password hashing timed out")
		}
		return "", fmt.Errorf("openssl password hashing failed: %w", err)
	}
	hash := strings.TrimSpace(string(output))
	if !strings.HasPrefix(hash, "$6$") {
		return "", fmt.Errorf("openssl returned no SHA512-CRYPT hash")
	}
	return "{SHA512-CRYPT}" + hash, nil
}
