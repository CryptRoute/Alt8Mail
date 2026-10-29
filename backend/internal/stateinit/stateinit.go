// Package stateinit builds the non-demo starting state for a real mailbox.
// It deliberately does not read seed/state.json: mail and workspace content
// start empty and are populated by Dovecot IMAP sync and real user actions.
package stateinit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type userRow struct {
	ID          string
	TenantID    string
	Email       string
	Name        string
	Initials    string
	Color       string
	Role        string
	Preferences json.RawMessage
	QuotaBytes  int64
}

func Build(ctx context.Context, db *pgxpool.Pool, email string) (map[string]any, error) {
	var user userRow
	var tenantName string
	err := db.QueryRow(ctx, `
		SELECT u.id::text, u.tenant_id::text, u.email::text, u.name, u.initials, u.color,
		       u.role, u.preferences, u.quota_bytes, t.name
		FROM users u JOIN tenants t ON t.id = u.tenant_id
		WHERE u.email = $1 AND u.status = 'active'
	`, strings.ToLower(strings.TrimSpace(email))).Scan(
		&user.ID, &user.TenantID, &user.Email, &user.Name, &user.Initials,
		&user.Color, &user.Role, &user.Preferences, &user.QuotaBytes, &tenantName,
	)
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	preferences := map[string]any{}
	_ = json.Unmarshal(user.Preferences, &preferences)
	userMap := map[string]any{
		"name": user.Name, "email": user.Email, "initials": user.Initials,
		"color": user.Color, "role": user.Role, "company": tenantName,
		"team": "", "phone": "", "location": "", "reportsTo": "",
		"timezone": firstString(preferences["timeZone"], "UTC"),
		"quota":    map[string]any{"limitBytes": user.QuotaBytes, "baselineUsedBytes": 0},
		"outbound": map[string]any{
			"limit": 200, "windowHours": 24, "baselineUsed": 0, "events": []any{},
		},
		"preferences": preferences,
	}

	aliases := []any{}
	rows, err := db.Query(ctx, `
		SELECT email::text, label, kind, can_send
		FROM user_aliases WHERE user_id = $1 AND active = true
		ORDER BY (kind = 'primary') DESC, created_at
	`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load aliases: %w", err)
	}
	for rows.Next() {
		var address, label, kind string
		var canSend bool
		if err := rows.Scan(&address, &label, &kind, &canSend); err != nil {
			rows.Close()
			return nil, err
		}
		reason := ""
		if !canSend {
			reason = "Delegated mailbox · read only"
		}
		aliases = append(aliases, map[string]any{
			"id":    kind + "_" + strings.ReplaceAll(address, "@", "_"),
			"email": address, "label": label, "kind": kind,
			"canSend": canSend, "reason": reason,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	userMap["aliases"] = aliases

	trustedSenders := []any{}
	trustedRows, err := db.Query(ctx, `SELECT sender::text FROM trusted_senders WHERE user_id = $1 ORDER BY sender`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load trusted senders: %w", err)
	}
	for trustedRows.Next() {
		var sender string
		if err := trustedRows.Scan(&sender); err == nil {
			trustedSenders = append(trustedSenders, sender)
		}
	}
	trustedRows.Close()
	userMap["trustedSenders"] = trustedSenders

	people := []any{}
	peopleRows, err := db.Query(ctx, `
		SELECT name, email::text, role, initials, color
		FROM users WHERE tenant_id = $1 AND id <> $2 AND status = 'active'
		ORDER BY name
	`, user.TenantID, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load directory: %w", err)
	}
	for peopleRows.Next() {
		var name, address, role, initials, color string
		if err := peopleRows.Scan(&name, &address, &role, &initials, &color); err != nil {
			peopleRows.Close()
			return nil, err
		}
		people = append(people, map[string]any{
			"name": name, "email": address, "role": role, "company": tenantName,
			"team": "", "phone": "", "location": "", "reportsTo": "",
			"initials": initials, "color": color,
		})
	}
	peopleRows.Close()
	if err := peopleRows.Err(); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	state := map[string]any{
		"version":         1,
		"createdAt":       now,
		"mailMode":        "imap",
		"user":            userMap,
		"folders":         standardFolders(),
		"people":          people,
		"sieve":           defaultSieve(),
		"audit":           []any{},
		"pendingExpunges": []any{},
	}
	return state, nil
}

func standardFolders() []any {
	return []any{
		map[string]any{"id": "inbox", "label": "Inbox", "icon": "i-inbox", "section": "favorites"},
		map[string]any{"id": "starred", "label": "Starred", "icon": "i-star", "section": "favorites", "virtual": true},
		map[string]any{"id": "sent", "label": "Sent", "icon": "i-send", "section": "folders"},
		map[string]any{"id": "drafts", "label": "Drafts", "icon": "i-file", "section": "folders"},
		map[string]any{"id": "archive", "label": "Archive", "icon": "i-archive", "section": "folders"},
		map[string]any{"id": "spam", "label": "Spam", "icon": "i-alert", "section": "folders"},
		map[string]any{"id": "trash", "label": "Trash", "icon": "i-trash", "section": "folders"},
	}
}

func defaultSieve() map[string]any {
	return map[string]any{
		"revision": 0,
		"rules":    []any{},
		"vacation": map[string]any{
			"enabled": false, "start": "", "end": "", "days": 7,
			"internal": map[string]any{"subject": "Out of office", "message": ""},
			"external": map[string]any{"enabled": false, "subject": "Away from the office", "message": ""},
		},
		"script": "",
	}
}

func firstString(value any, fallback string) string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}
