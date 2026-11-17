package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
)

var mcpSupportedScopes = []string{"mail.read", "mail.search", "mail.draft", "calendar.read", "tasks.read", "calendar.write", "tasks.write", "labels.write", "draft.send", "crm.read"}

func mcpScopeSupported(scope string) bool {
	for _, supported := range mcpSupportedScopes {
		if scope == supported {
			return true
		}
	}
	return false
}

func mcpWriteTools() []string {
	return []string{"calendar_write", "tasks_write", "labels_write", "draft_send"}
}

func mcpHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
