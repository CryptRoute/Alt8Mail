package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Server) settingRaw(ctx context.Context, key string) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM control.settings WHERE key=$1`, key).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (s *Server) settingString(ctx context.Context, key, fallback string) string {
	raw, err := s.settingRaw(ctx, key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var value string
	if json.Unmarshal(raw, &value) == nil && value != "" {
		return value
	}
	return fallback
}

func (s *Server) settingInt(ctx context.Context, key string, fallback int) int {
	raw, err := s.settingRaw(ctx, key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var value int
	if json.Unmarshal(raw, &value) == nil && value != 0 {
		return value
	}
	return fallback
}

func (s *Server) settingBool(ctx context.Context, key string, fallback bool) bool {
	raw, err := s.settingRaw(ctx, key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return fallback
}

func (s *Server) setSetting(ctx context.Context, key string, value any, userID string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var user any
	if userID != "" {
		user = userID
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO control.settings (key, value, updated_by, updated_at)
		VALUES ($1,$2::jsonb,$3,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=now()
	`, key, raw, user)
	return err
}

func (s *Server) allSettings(ctx context.Context) (map[string]any, error) {
	rows, err := s.db.Query(ctx, `SELECT key, value FROM control.settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]any{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			result[key] = value
		}
	}
	return result, rows.Err()
}

func (s *Server) serverHostname(ctx context.Context) string {
	value := s.settingString(ctx, "server.hostname", "")
	if value != "" {
		return value
	}
	return s.mailHostname(ctx)
}

func requiredString(body map[string]any, key string) (string, error) {
	value, ok := body[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return strings.TrimSpace(text), nil
}
