package httpapi

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// hashMailPassword creates the SHA512-CRYPT hash format read by the deployed
// Dovecot SQL passdb. doveadm is the authoritative local implementation, so the
// admin backend does not maintain a second crypt implementation. The password
// never touches a shell or the process table; it is written to stdin twice as
// doveadm pw expects.
func hashMailPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("mailbox password is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "doveadm", "pw", "-s", "SHA512-CRYPT")
	command.Stdin = strings.NewReader(password + "\n" + password + "\n")
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("doveadm timed out")
		}
		return "", fmt.Errorf("doveadm pw failed: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{SHA512-CRYPT}") {
			return line, nil
		}
	}
	return "", fmt.Errorf("doveadm returned no SHA512-CRYPT hash")
}
