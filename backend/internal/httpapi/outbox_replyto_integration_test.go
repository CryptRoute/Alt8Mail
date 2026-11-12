package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOutboxReplyToIntegration requires A8M_SCIM_TEST_DSN with all migrations
// applied (including 0087_outbox_reply_to). It proves the delivery worker,
// which rebuilds the queue from the normalized outbox table, keeps a stored
// identity reply-to and writes it into the submitted MIME.
func TestOutboxReplyToIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the outbox reply-to integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4071-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4072-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "replyto-" + suffix + "@outbox.example.test"
	slug := "outbox-replyto-" + suffix
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()

	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Outbox reply-to')`, tenant, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, tenant_id, email, name, role, status) VALUES ($1,$2,$3,'Reply To Owner','owner','active')
	`, userID, tenant, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_aliases (tenant_id, user_id, email, label, kind, can_send)
		VALUES ($1,$2,$3,'Reply To Owner','primary',true)
	`, tenant, userID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mail_identity_signatures (tenant_id, user_id, email, body_text, body_html, display_name, reply_to)
		VALUES ($1,$2,$3,'','','Reply To Owner','replies@partner.example')
	`, tenant, userID, email); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: &store.Store{DB: pool}}
	sender := userRecord{ID: userID, TenantID: tenant, Email: email, Name: "Reply To Owner"}
	if got := server.identityReplyTo(ctx, userID, email); got != "replies@partner.example" {
		t.Fatalf("identity reply-to = %q", got)
	}
	now := time.Now().UTC()
	item := map[string]any{
		"id": "send_replyto_" + suffix, "draftId": "", "accountId": "",
		"from": map[string]any{"name": "Reply To Owner", "email": email},
		"replyTo": "replies@partner.example",
		"to": []any{map[string]any{"name": "Nora", "email": "nora@example.test"}},
		"cc": []any{}, "bcc": []any{},
		"subject": "Reply-to round trip", "bodyText": "Hello", "bodyHtml": "<p>Hello</p>",
		"attachments": []any{}, "threadId": "thread_replyto_" + suffix, "priority": "normal",
		"receipt": false, "delivery": false, "saveCopy": true, "scheduled": false,
		"sendAt": now.Format(time.RFC3339), "queuedAt": now.Format(time.RFC3339),
		"holdMs": 0, "status": "queued", "deliveryState": "", "attempts": 0,
		"classification": "Internal", "dlpWarnings": []any{},
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := server.persistOutboxItemTx(ctx, tx, sender, item); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := server.loadOutboxRows(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	var loaded map[string]any
	for _, row := range rows {
		if stateString(row, "id") == item["id"] {
			loaded = row
			break
		}
	}
	if loaded == nil {
		t.Fatal("persisted outbox row was not loaded")
	}
	if got := stateString(loaded, "replyTo"); got != "replies@partner.example" {
		t.Fatalf("outbox row lost reply-to: %q", got)
	}
	raw, err := buildMIMEMessage(loaded, "<replyto-"+suffix+"@a8m.test>", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Reply-To: <replies@partner.example>") {
		t.Fatalf("submitted MIME is missing Reply-To:\n%s", raw)
	}
}
