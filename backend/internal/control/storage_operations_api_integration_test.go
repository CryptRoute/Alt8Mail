package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStorageOperationsAPIIntegration verifies the paging and sort contract
// used by the operations table. It requires A8M_STORAGE_TEST_DSN.
func TestStorageOperationsAPIIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_STORAGE_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_STORAGE_TEST_DSN to run the storage operations integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	fixtures := []struct {
		mailboxes int
		status    string
		target    string
	}{
		{mailboxes: 905, status: "failed", target: "ops-api-a-" + suffix},
		{mailboxes: 901, status: "grace", target: "ops-api-b-" + suffix},
		{mailboxes: 903, status: "planned", target: "ops-api-c-" + suffix},
	}
	ids := make([]string, 0, len(fixtures))
	for index, fixture := range fixtures {
		items := make([]map[string]any, fixture.mailboxes)
		for item := range items {
			items[item] = map[string]any{"email": fmt.Sprintf("ops-%d-%d@example.test", index, item)}
		}
		plan, _ := json.Marshal(map[string]any{
			"scope": "mailbox", "targetVolumeName": fixture.target, "items": items,
		})
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO control.storage_operations
			  (kind, status, plan, plan_hash, step, actor_email, created_at, updated_at)
			VALUES ('move_mailboxes', $1, $2::jsonb, 'test', 'Queued', $3, now() - ($4 * interval '1 minute'), now())
			RETURNING id::text
		`, fixture.status, string(plan), "ops-api-"+suffix, len(fixtures)-index).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM control.storage_operations WHERE id = ANY($1::uuid[])`, ids)
	}()

	server := &Server{db: pool}
	request := func(query string) map[string]any {
		recorder := httptest.NewRecorder()
		server.handleStorageOperations(recorder, httptest.NewRequest(http.MethodGet, "/control/api/storage/operations"+query, nil), nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return payload
	}
	orderOfFixtures := func(payload map[string]any) []string {
		out := []string{}
		for _, raw := range payload["operations"].([]any) {
			operation := raw.(map[string]any)
			id := operation["id"].(string)
			for _, fixtureID := range ids {
				if fixtureID == id {
					out = append(out, fixtureID)
				}
			}
		}
		return out
	}

	payload := request("?sort=mailboxes&order=desc&limit=100&page=1")
	if got := int(payload["total"].(float64)); got < len(fixtures) {
		t.Fatalf("total = %d, want at least %d", got, len(fixtures))
	}
	if rows := len(payload["operations"].([]any)); rows > 100 {
		t.Fatalf("returned %d rows for limit 100", rows)
	}
	ordered := orderOfFixtures(payload)
	if len(ordered) != len(fixtures) || ordered[0] != ids[0] || ordered[1] != ids[2] || ordered[2] != ids[1] {
		t.Fatalf("mailbox-count order = %v, want %v", ordered, []string{ids[0], ids[2], ids[1]})
	}
	for _, raw := range payload["operations"].([]any) {
		operation := raw.(map[string]any)
		if operation["id"] == ids[0] {
			if int(operation["mailboxes"].(float64)) != 905 || operation["targetVolume"] != fixtures[0].target {
				t.Fatalf("summary = %#v", operation)
			}
		}
	}

	statusPayload := request("?sort=status&order=asc&limit=100&page=1")
	statusOrdered := orderOfFixtures(statusPayload)
	if len(statusOrdered) != len(fixtures) || statusOrdered[0] != ids[0] || statusOrdered[1] != ids[1] || statusOrdered[2] != ids[2] {
		t.Fatalf("status order = %v, want %v", statusOrdered, ids)
	}

	pageTwo := request("?sort=mailboxes&order=desc&limit=2&page=2")
	if got := int(pageTwo["page"].(float64)); got != 2 {
		t.Fatalf("page = %d, want 2", got)
	}
	if got := int(pageTwo["pages"].(float64)); got < 2 {
		t.Fatalf("pages = %d, want at least 2", got)
	}
	if got := len(pageTwo["operations"].([]any)); got < 1 || got > 2 {
		t.Fatalf("page two returned %d rows for limit 2", got)
	}
}
