package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run with:
//
//	A8M_NOTES_TEST_DSN='postgres://...' go test ./internal/httpapi -run NotesDeskIntegration -count=1
//
// The test owns its tenant and users and deletes the tenant afterwards.
func TestNotesDeskIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_NOTES_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_NOTES_TEST_DSN to run the PostgreSQL notes desk integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	var table string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.user_sticky_notes')::text`).Scan(&table); err != nil || table == "" {
		t.Fatalf("user_sticky_notes is missing; apply migration 0040 first: %v", err)
	}

	slug := fmt.Sprintf("notes-test-%d", time.Now().UnixNano())
	var tenantID string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, "Notes desk test").Scan(&tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	defer func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM mail_messages WHERE id='msg-context'`)
		_, _ = pool.Exec(cleanup, `DELETE FROM calendar_events WHERE id='event-context'`)
		_, _ = pool.Exec(cleanup, `DELETE FROM calendars WHERE id='cal-context'`)
		_, _ = pool.Exec(cleanup, `DELETE FROM files WHERE id='file-context'`)
		_, _ = pool.Exec(cleanup, `DELETE FROM tenants WHERE id=$1`, tenantID)
	}()
	createUser := func(label string) userRecord {
		var id, email string
		email = fmt.Sprintf("%s-%s@example.test", label, slug)
		if err := pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, name, role)
			VALUES ($1, $2, $3, 'owner') RETURNING id::text, email::text
		`, tenantID, email, "Notes "+label).Scan(&id, &email); err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		return userRecord{ID: id, TenantID: tenantID, Email: email, Name: "Notes " + label}
	}
	owner := createUser("owner")
	legacyState := `{"user":{"preferences":{"stickyNote":{"content":"Legacy line\n- [ ] Task","color":"blue","enabled":true,"collapsed":false,"links":[],"position":{"x":0.5,"y":0.2}}}}}`
	if _, err := pool.Exec(ctx, `INSERT INTO app_states (user_id, tenant_id, state) VALUES ($1,$2,$3::jsonb)`, owner.ID, tenantID, legacyState); err != nil {
		t.Fatalf("seed legacy preference: %v", err)
	}
	delegate := createUser("delegate")
	if _, err := pool.Exec(ctx, `
		INSERT INTO mailbox_delegates (id, tenant_id, owner_user_id, delegate_user_id, can_read, can_send)
		VALUES ('delegation-context', $1, $2, $3, true, true)
	`, tenantID, owner.ID, delegate.ID); err != nil {
		t.Fatalf("seed delegated mailbox: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO mail_messages (id, tenant_id, user_id, thread_id, subject, from_name, from_email, sent_at)
		VALUES ('msg-context', $1, $2, 'thread-context', 'Context thread', 'Avery Chen', 'avery@example.test', now())
	`, tenantID, owner.ID); err != nil {
		t.Fatalf("seed context thread: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind)
		VALUES ('cal-context', $1, $2, 'Calendar', '#0f6cbd', 'personal')
	`, tenantID, owner.ID); err != nil {
		t.Fatalf("seed context calendar: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, location, starts_at, ends_at, all_day, status)
		VALUES ('event-context', $1, $2, 'cal-context', 'uid-context', 'Context review', 'Room 4', now() + interval '1 hour', now() + interval '2 hours', false, 'confirmed')
	`, tenantID, owner.ID); err != nil {
		t.Fatalf("seed context event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (id, tenant_id, user_id, provider_id, name, mime, size_bytes, updated_at)
		VALUES ('file-context', $1, $2, 'a8m', 'Context brief.pdf', 'application/pdf', 2048, now())
	`, tenantID, owner.ID); err != nil {
		t.Fatalf("seed context file: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Options{Config: config.Config{}, Store: &store.Store{DB: pool}, Logger: logger})

	if err := server.migrateLegacyStickyNote(ctx, owner); err != nil {
		t.Fatalf("migrate legacy note: %v", err)
	}
	var legacyID, legacyTitle, legacyContent, legacyColor string
	var legacyPinned bool
	var legacyPosition []byte
	if err := pool.QueryRow(ctx, `
		SELECT id, title, content, color, inbox_pinned, position
		FROM user_sticky_notes WHERE owner_user_id=$1
	`, owner.ID).Scan(&legacyID, &legacyTitle, &legacyContent, &legacyColor, &legacyPinned, &legacyPosition); err != nil {
		t.Fatalf("read migrated legacy note: %v", err)
	}
	if !legacyPinned || legacyColor != "blue" || legacyTitle != "Legacy line" || !strings.Contains(legacyContent, "- [ ] Task") {
		t.Fatalf("legacy note migrated incorrectly: pinned=%v color=%q title=%q content=%q", legacyPinned, legacyColor, legacyTitle, legacyContent)
	}
	var position map[string]float64
	if err := json.Unmarshal(legacyPosition, &position); err != nil || position["x"] != 0.5 || position["y"] != 0.2 {
		t.Fatalf("legacy position = %s (%v)", legacyPosition, err)
	}

	// A second legacy load must be idempotent.
	if err := server.migrateLegacyStickyNote(ctx, owner); err != nil {
		t.Fatalf("repeat legacy migration: %v", err)
	}
	var count int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM user_sticky_notes WHERE owner_user_id=$1`, owner.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("legacy migration repeated %d notes", count)
	}

	request := func(user userRecord, method, path, body, noteID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if noteID != "" {
			req.SetPathValue("id", noteID)
		}
		req = req.WithContext(context.WithValue(req.Context(), sessionContextKey{}, &sessionRecord{User: user}))
		rec := httptest.NewRecorder()
		switch {
		case method == http.MethodPost:
			server.createStickyNote(rec, req)
		case method == http.MethodPatch:
			server.patchStickyNote(rec, req)
		case strings.HasPrefix(req.URL.Path, "/api/notes/search"):
			server.searchStickyNotes(rec, req)
		case strings.HasPrefix(req.URL.Path, "/api/notes/attach"):
			server.getStickyNoteAttachCandidates(rec, req)
		default:
			server.getStickyNotes(rec, req)
		}
		return rec
	}
	type board struct {
		Notes       []map[string]any `json:"notes"`
		Pinned      map[string]any   `json:"pinned"`
		ActiveCount int              `json:"activeCount"`
		Team        bool             `json:"team"`
		TeamCount   int              `json:"teamCount"`
		CanEdit     bool             `json:"canEdit"`
		Mailboxes   []map[string]any `json:"mailboxes"`
	}
	decodeSearch := func(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) []map[string]any {
		t.Helper()
		if rec.Code != wantStatus {
			t.Fatalf("search status = %d, want %d; body: %s", rec.Code, wantStatus, rec.Body.String())
		}
		var out struct {
			Notes     []map[string]any `json:"notes"`
			Mailboxes []map[string]any `json:"mailboxes"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode search: %v; body: %s", err, rec.Body.String())
		}
		return out.Notes
	}
	decodeBoard := func(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) board {
		t.Helper()
		if rec.Code != wantStatus {
			t.Fatalf("status = %d, want %d; body: %s", rec.Code, wantStatus, rec.Body.String())
		}
		var out board
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode board: %v; body: %s", err, rec.Body.String())
		}
		return out
	}
	findNote := func(out board, needle string) map[string]any {
		for _, note := range out.Notes {
			if strings.Contains(fmt.Sprint(note["content"]), needle) {
				return note
			}
		}
		return nil
	}

	first := decodeBoard(t, request(owner, http.MethodPost, "/api/notes",
		`{"content":"Second note","links":["thread-context",{"type":"event","id":"event-context"},{"type":"file","id":"file-context"}]}`, ""), http.StatusCreated)
	second := findNote(first, "Second note")
	if second == nil {
		t.Fatalf("created note missing from board: %#v", first.Notes)
	}
	if createdLinks, _ := second["links"].([]any); len(createdLinks) != 3 {
		t.Fatalf("create did not persist typed links: %#v", second)
	}
	if second["inboxPinned"] != false {
		t.Fatalf("second note stole the legacy pin: %#v", second)
	}
	secondID := fmt.Sprint(second["id"])

	linked := decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID,
		`{"links":["thread-context",{"type":"event","id":"event-context"},{"type":"file","id":"file-context"}]}`, secondID), http.StatusOK)
	linkedNote := findNote(linked, "Second note")
	if linkedNote == nil {
		t.Fatalf("linked note missing from board: %#v", linked.Notes)
	}
	links, _ := linkedNote["links"].([]any)
	if len(links) != 3 {
		t.Fatalf("typed note links = %#v", linkedNote)
	}
	metadata := map[string]map[string]any{}
	for _, raw := range links {
		link, _ := raw.(map[string]any)
		metadata[fmt.Sprint(link["type"])] = link
	}
	if metadata["thread"]["subject"] != "Context thread" {
		t.Fatalf("thread link metadata = %#v", metadata["thread"])
	}
	if metadata["event"]["title"] != "Context review" || metadata["event"]["location"] != "Room 4" {
		t.Fatalf("event link metadata = %#v", metadata["event"])
	}
	if metadata["file"]["name"] != "Context brief.pdf" || metadata["file"]["mime"] != "application/pdf" {
		t.Fatalf("file link metadata = %#v", metadata["file"])
	}

	pinned := decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"inboxPinned":true}`, secondID), http.StatusOK)
	if pinned.Pinned == nil || fmt.Sprint(pinned.Pinned["id"]) != secondID {
		t.Fatalf("pin swap failed: %#v", pinned.Pinned)
	}
	for _, note := range pinned.Notes {
		if fmt.Sprint(note["id"]) == legacyID && note["inboxPinned"] != false {
			t.Fatalf("old pin was not cleared: %#v", note)
		}
	}

	archived := decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"archived":true}`, secondID), http.StatusOK)
	if archived.Pinned != nil {
		t.Fatalf("archiving left a pin: %#v", archived.Pinned)
	}
	var archivedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT archived_at FROM user_sticky_notes WHERE id=$1`, secondID).Scan(&archivedAt); err != nil {
		t.Fatalf("read archived_at: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"color":"green"}`, secondID), http.StatusOK)
	var archivedAtAgain time.Time
	if err := pool.QueryRow(ctx, `SELECT archived_at FROM user_sticky_notes WHERE id=$1`, secondID).Scan(&archivedAtAgain); err != nil {
		t.Fatalf("reread archived_at: %v", err)
	}
	if !archivedAt.Equal(archivedAtAgain) {
		t.Fatalf("editing an archived note moved it in the archive: %s -> %s", archivedAt, archivedAtAgain)
	}
	if rec := request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"archived":true,"inboxPinned":true}`, secondID); rec.Code != http.StatusBadRequest {
		t.Fatalf("pinning an archived note status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}

	restored := decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"archived":false,"inboxPinned":true}`, secondID), http.StatusOK)
	if restored.Pinned == nil || fmt.Sprint(restored.Pinned["id"]) != secondID {
		t.Fatalf("restore and pin failed: %#v", restored.Pinned)
	}
	decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"content":"Quokka planning"}`, secondID), http.StatusOK)
	var matches int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_sticky_notes WHERE id=$1 AND search_vector @@ to_tsquery('simple','quokka')`, secondID).Scan(&matches); err != nil || matches != 1 {
		t.Fatalf("generated search vector did not follow the edit: matches=%d err=%v", matches, err)
	}

	delegateBoard := decodeBoard(t, request(delegate, http.MethodGet, "/api/notes?archived=1&ownerId="+owner.ID, "", ""), http.StatusOK)
	if !delegateBoard.Team || delegateBoard.TeamCount != 1 || !delegateBoard.CanEdit || len(delegateBoard.Mailboxes) < 2 {
		t.Fatalf("delegated board indicators = team:%v count:%d canEdit:%v mailboxes:%d",
			delegateBoard.Team, delegateBoard.TeamCount, delegateBoard.CanEdit, len(delegateBoard.Mailboxes))
	}
	delegateNote := decodeBoard(t, request(delegate, http.MethodPost, "/api/notes", `{"content":"Delegate private note"}`, ""), http.StatusCreated)
	if findNote(delegateNote, "Delegate private note") == nil {
		t.Fatalf("delegate note missing: %#v", delegateNote.Notes)
	}
	delegateSearch := decodeSearch(t, request(delegate, http.MethodGet, "/api/notes/search?q=Quokka&scope=all", "", ""), http.StatusOK)
	var sharedResult map[string]any
	for _, note := range delegateSearch {
		if fmt.Sprint(note["id"]) == secondID {
			sharedResult = note
			break
		}
	}
	if sharedResult == nil {
		t.Fatalf("delegate global search missed the shared note: %#v", delegateSearch)
	}
	ownerMeta, _ := sharedResult["owner"].(map[string]any)
	if sharedResult["team"] != true || sharedResult["teamCount"] != float64(1) || sharedResult["canEdit"] != true || fmt.Sprint(ownerMeta["id"]) != owner.ID {
		t.Fatalf("shared search result metadata = %#v", sharedResult)
	}
	if leaked := decodeSearch(t, request(owner, http.MethodGet, "/api/notes/search?q=Delegate%20private&scope=all", "", ""), http.StatusOK); len(leaked) != 0 {
		t.Fatalf("owner search saw a delegate's private note: %#v", leaked)
	}

	// Archive search filters on the same endpoint used by the desk.
	decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"archived":true}`, secondID), http.StatusOK)
	archivedResults := decodeSearch(t, request(owner, http.MethodGet, "/api/notes/search?q=Quokka&archived=1&scope=mailbox", "", ""), http.StatusOK)
	foundArchived := false
	for _, note := range archivedResults {
		if fmt.Sprint(note["id"]) == secondID && note["archived"] == true {
			foundArchived = true
		}
	}
	if !foundArchived {
		t.Fatalf("archive search did not return the archived note: %#v", archivedResults)
	}
	activeResults := decodeSearch(t, request(owner, http.MethodGet, "/api/notes/search?q=Quokka&archived=0&scope=mailbox", "", ""), http.StatusOK)
	for _, note := range activeResults {
		if fmt.Sprint(note["id"]) == secondID {
			t.Fatalf("active search returned an archived note: %#v", note)
		}
	}
	decodeBoard(t, request(owner, http.MethodPatch, "/api/notes/"+secondID, `{"archived":false,"inboxPinned":true}`, secondID), http.StatusOK)

	decodeItems := func(path string) []any {
		rec := request(delegate, http.MethodGet, path+"&ownerId="+owner.ID, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("attach candidates status = %d; body: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []any `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode attach candidates: %v", err)
		}
		return out.Items
	}
	for _, check := range []struct{ path, key string }{
		{"/api/notes/attach?type=thread", "thread-context"},
		{"/api/notes/attach?type=event", "event-context"},
		{"/api/notes/attach?type=file", "file-context"},
	} {
		items := decodeItems(check.path)
		found := false
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if fmt.Sprint(item["id"]) == check.key {
				found = true
			}
		}
		if !found {
			t.Fatalf("attach candidates for %s missed %s: %#v", check.path, check.key, items)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE mailbox_delegates SET can_send=false WHERE owner_user_id=$1 AND delegate_user_id=$2`, owner.ID, delegate.ID); err != nil {
		t.Fatalf("make delegation read-only: %v", err)
	}
	readOnlyBoard := decodeBoard(t, request(delegate, http.MethodGet, "/api/notes?archived=1&ownerId="+owner.ID, "", ""), http.StatusOK)
	if readOnlyBoard.CanEdit {
		t.Fatal("read-only delegate was allowed to edit the shared desk")
	}
	readOnlyWrite := request(delegate, http.MethodPatch, "/api/notes/"+secondID+"?ownerId="+owner.ID, `{"color":"blue"}`, secondID)
	if readOnlyWrite.Code != http.StatusForbidden {
		t.Fatalf("read-only delegate write status = %d, want 403; body: %s", readOnlyWrite.Code, readOnlyWrite.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE mailbox_delegates SET can_send=true WHERE owner_user_id=$1 AND delegate_user_id=$2`, owner.ID, delegate.ID); err != nil {
		t.Fatalf("restore delegation write access: %v", err)
	}

	// Concurrent default-pin creates must produce exactly one Inbox pin.
	racer := createUser("racer")
	const racers = 8
	var wg sync.WaitGroup
	statuses := make(chan int, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			rec := request(racer, http.MethodPost, "/api/notes", fmt.Sprintf(`{"content":"Race note %d"}`, index), "")
			statuses <- rec.Code
		}(i)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("concurrent note create status = %d, want 201", status)
		}
	}
	var raceNotes, racePins int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE inbox_pinned)
		FROM user_sticky_notes WHERE owner_user_id=$1 AND archived_at IS NULL
	`, racer.ID).Scan(&raceNotes, &racePins); err != nil {
		t.Fatalf("read concurrent notes: %v", err)
	}
	if raceNotes != racers || racePins != 1 {
		t.Fatalf("concurrent creates produced %d notes and %d pins, want %d and 1", raceNotes, racePins, racers)
	}
}
