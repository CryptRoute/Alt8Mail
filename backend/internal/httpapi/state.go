package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

func newID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + "_" + strings.TrimRight(base64URL(buf), "=")
}

func base64URL(value []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out strings.Builder
	for i := 0; i < len(value); i += 3 {
		var chunk uint32
		remain := len(value) - i
		chunk = uint32(value[i]) << 16
		if remain > 1 {
			chunk |= uint32(value[i+1]) << 8
		}
		if remain > 2 {
			chunk |= uint32(value[i+2])
		}
		out.WriteByte(alphabet[(chunk>>18)&63])
		out.WriteByte(alphabet[(chunk>>12)&63])
		if remain > 1 {
			out.WriteByte(alphabet[(chunk>>6)&63])
		}
		if remain > 2 {
			out.WriteByte(alphabet[chunk&63])
		}
	}
	return out.String()
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Server) loadState(ctx context.Context, userID string) (map[string]any, error) {
	var raw []byte
	err := s.store.DB.QueryRow(ctx, `SELECT state FROM app_states WHERE user_id = $1`, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decode app state: %w", err)
	}
	return state, nil
}

func (s *Server) saveState(ctx context.Context, userID, tenantID string, state map[string]any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO app_states (user_id, tenant_id, state, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()
	`, userID, tenantID, raw)
	return err
}

func (s *Server) userStateLock(userID string) *sync.Mutex {
	s.stateLocksMu.Lock()
	defer s.stateLocksMu.Unlock()
	lock := s.stateLocks[userID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.stateLocks[userID] = lock
	}
	return lock
}

func (s *Server) withState(userID, tenantID string, fn func(map[string]any) error) error {
	return s.withStateTimeout(userID, tenantID, 15*time.Second, fn)
}

// withStateTimeout is used by large mailbox imports, where serializing a
// multi-megabyte state document can legitimately take longer than a normal
// interactive request. The lock is per user: a slow sync for one mailbox must
// not block interactive reads or writes for every other user.
func (s *Server) withStateTimeout(userID, tenantID string, timeout time.Duration, fn func(map[string]any) error) error {
	lock := s.userStateLock(userID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	state, err := s.loadState(ctx, userID)
	if err != nil {
		return err
	}
	if err := fn(state); err != nil {
		return err
	}
	return s.saveState(ctx, userID, tenantID, state)
}

func stateMap(state map[string]any, key string) map[string]any {
	value, _ := state[key].(map[string]any)
	if value == nil {
		value = map[string]any{}
	}
	return value
}

func stateSlice(state map[string]any, key string) []any {
	value, _ := state[key].([]any)
	if value == nil {
		value = []any{}
	}
	return value
}

func stateString(m map[string]any, key string) string {
	if value, ok := m[key]; ok && value != nil {
		return fmt.Sprint(value)
	}
	return ""
}

func stateBool(m map[string]any, key string) bool {
	value, _ := m[key].(bool)
	return value
}

func stateInt(m map[string]any, key string) int64 {
	switch value := m[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case uint32:
		return int64(value)
	case int32:
		return int64(value)
	case uint:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	}
	return 0
}

func stateObjectByID(items []any, id string) map[string]any {
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj != nil && stateString(obj, "id") == id {
			return obj
		}
	}
	return nil
}

func stateSliceByID(items []any, id string) []any {
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj != nil && stateString(obj, "id") == id {
			return []any{obj}
		}
	}
	return nil
}

func stateRemoveByID(items []any, id string) []any {
	result := make([]any, 0, len(items))
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj != nil && stateString(obj, "id") == id {
			continue
		}
		result = append(result, item)
	}
	return result
}

func stateAdd(items []any, value any) []any {
	return append(items, value)
}

func statePerson(value any) map[string]any {
	if person, ok := value.(map[string]any); ok {
		return person
	}
	return map[string]any{"name": fmt.Sprint(value), "email": fmt.Sprint(value)}
}

func stateRecipientText(value any) string {
	items, _ := value.([]any)
	parts := []string{}
	for _, item := range items {
		person, _ := item.(map[string]any)
		if person == nil {
			continue
		}
		parts = append(parts, stateString(person, "email"))
	}
	return strings.Join(parts, ", ")
}

func parsePeople(value any) ([]any, error) {
	raw := []string{}
	switch input := value.(type) {
	case string:
		for _, chunk := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ';' }) {
			if text := strings.TrimSpace(chunk); text != "" {
				raw = append(raw, text)
			}
		}
	case []any:
		for _, item := range input {
			switch typed := item.(type) {
			case string:
				raw = append(raw, typed)
			case map[string]any:
				raw = append(raw, stateString(typed, "email"))
			}
		}
	}
	people := []any{}
	seen := map[string]bool{}
	for _, text := range raw {
		name := ""
		email := text
		if open := strings.LastIndex(text, "<"); open >= 0 && strings.HasSuffix(strings.TrimSpace(text), ">") {
			name = strings.TrimSpace(text[:open])
			email = strings.TrimSpace(strings.TrimSuffix(text[open+1:], ">"))
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
			return nil, fmt.Errorf("invalid recipient: %s", text)
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		if name == "" {
			name = strings.Split(email, "@")[0]
			name = strings.ReplaceAll(name, ".", " ")
			parts := strings.Fields(name)
			for i, part := range parts {
				if part != "" {
					parts[i] = strings.ToUpper(part[:1]) + part[1:]
				}
			}
			name = strings.Join(parts, " ")
		}
		people = append(people, map[string]any{
			"name": name, "email": email,
			"initials": initialsFor(name, email), "color": colorFor(email),
		})
	}
	if len(people) == 0 {
		return nil, errors.New("add at least one recipient")
	}
	return people, nil
}

func initialsFor(name, email string) string {
	parts := strings.Fields(name)
	if len(parts) >= 2 {
		return strings.ToUpper(parts[0][:1] + parts[len(parts)-1][:1])
	}
	if len(name) >= 2 {
		return strings.ToUpper(name[:2])
	}
	if len(email) >= 1 {
		return strings.ToUpper(email[:1])
	}
	return "?"
}

func colorFor(value string) string {
	palette := []string{"#34557a", "#3d6b62", "#5b4a7e", "#7a4a5c", "#7a5a3a", "#3f6570", "#476b4f", "#4f5673"}
	var hash uint32
	for _, char := range value {
		hash = hash*31 + uint32(char)
	}
	return palette[hash%uint32(len(palette))]
}

func snippet(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180])
	}
	return text
}

func textToHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	paragraphs := strings.Split(text, "\n\n")
	for i, paragraph := range paragraphs {
		paragraphs[i] = "<p>" + strings.ReplaceAll(paragraph, "\n", "<br>") + "</p>"
	}
	return strings.Join(paragraphs, "\n")
}

func sortStateByDate(items []any) {
	sort.SliceStable(items, func(i, j int) bool {
		left, _ := items[i].(map[string]any)
		right, _ := items[j].(map[string]any)
		return stateString(left, "date") > stateString(right, "date")
	})
}

func stateMessageIDs(threads []any) []string {
	ids := []string{}
	for _, item := range threads {
		thread, _ := item.(map[string]any)
		for _, raw := range stateSlice(thread, "messages") {
			message, _ := raw.(map[string]any)
			if message != nil {
				ids = append(ids, stateString(message, "id"))
			}
		}
	}
	return ids
}
