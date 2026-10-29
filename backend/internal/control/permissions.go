package control

import "strings"

var allPermissions = []string{
	"dashboard.read",
	"domains.read", "mailboxes.read", "aliases.read", "tls.read", "delivery.read",
	"services.read", "logs.read", "imports.read", "backups.read", "cron.read",
	"server.read", "database.read", "api.read", "notifications.read", "access.read",
	"audit.read",
	"admin.manage", "group.manage", "sessions.manage", "settings.write",
	"mail.manage", "cron.manage", "tls.manage", "delivery.manage", "services.manage",
	"imports.manage", "backups.manage", "server.manage", "database.manage",
	"api.manage", "notifications.manage",
}

var readPermissions = []string{
	"dashboard.read",
	"domains.read", "mailboxes.read", "aliases.read", "tls.read", "delivery.read",
	"services.read", "logs.read", "imports.read", "backups.read", "cron.read",
	"server.read", "database.read", "api.read", "notifications.read", "access.read",
	"audit.read",
}

func rolePermissions(role string) []string {
	switch role {
	case "superadmin":
		return []string{"*"}
	case "admin":
		out := append([]string{}, readPermissions...)
		out = append(out,
			"group.manage", "sessions.manage", "settings.write", "mail.manage",
			"cron.manage", "tls.manage", "delivery.manage", "services.manage",
			"imports.manage", "backups.manage", "server.manage", "database.manage",
			"api.manage", "notifications.manage",
		)
		return uniqueStrings(out)
	case "operator":
		out := append([]string{}, readPermissions...)
		out = append(out, "mail.manage", "delivery.manage", "imports.manage", "services.manage")
		return uniqueStrings(out)
	default:
		return append([]string{}, readPermissions...)
	}
}

func permissionMap(values []string) permissionSet {
	set := permissionSet{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func validPermission(value string) bool {
	for _, permission := range allPermissions {
		if permission == value {
			return true
		}
	}
	return false
}
