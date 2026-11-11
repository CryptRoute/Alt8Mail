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

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBookingHelpers(t *testing.T) {
	body, err := sanitizeBookingBody(bookingPageBody{Title: "Meet Avery", Timezone: "America/New_York", Duration: 30, StartHour: 9, EndHour: 17})
	if err != nil || body.Slug != "meet-avery" || body.Duration != 30 {
		t.Fatalf("sanitized = %#v err=%v", body, err)
	}
	if _, err := sanitizeBookingBody(bookingPageBody{Slug: "UP", Title: "X", Timezone: "UTC", StartHour: 9, EndHour: 17}); err == nil {
		t.Fatal("invalid slug accepted")
	}
	if _, err := sanitizeBookingBody(bookingPageBody{Slug: "ok-slug", Title: "X", Timezone: "Not/AZone", StartHour: 9, EndHour: 17}); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}

// TestBookingIntegration requires A8M_SCIM_TEST_DSN with all migrations,
// including 0072_booking_pages.
func TestBookingIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the booking integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4041-8000-%012d", time.Now().UnixNano()%1000000000000)
	user := fmt.Sprintf("00000000-0000-4042-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	email := "booking-" + suffix + "@booking.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO public.tenants (id, slug, name) VALUES ($1,$2,'Booking Test')`, tenant, "booking-"+suffix)
	mustExec(`INSERT INTO public.users (id, tenant_id, email, name, role) VALUES ($1,$2,$3,'Booking Host','member')`, user, tenant, email)
	pageID := "page_" + suffix
	slug := "meet-" + suffix
	mustExec(`INSERT INTO public.booking_pages
		(id, tenant_id, user_id, slug, title, description, duration_minutes, working_start_hour, working_end_hour, timezone, weekdays)
		VALUES ($1,$2,$3,$4,'Meet the team','Choose a time',30,9,17,'America/New_York','{0,1,2,3,4,5,6}')`, pageID, tenant, user, slug)
	mustExec(`INSERT INTO public.booking_page_members (page_id, user_id) VALUES ($1,$2)`, pageID, user)
	server := &Server{
		store: &store.Store{DB: pool}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{},
		license: &licensing.License{Version: licensing.Version, ID: "booking-test", Edition: "enterprise", Seats: 10,
			Features: []string{"booking_pages"}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	page, err := server.bookingPageBySlug(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	location := mustBookingLocation(page.Timezone)
	// Crossing the US spring DST boundary keeps 09:00 local and shifts UTC.
	dstNow := time.Date(2027, 3, 12, 12, 0, 0, 0, time.UTC)
	dstSlots := server.bookingSlotsAt(ctx, page, dstNow, 5)
	dstFound := false
	for _, slot := range dstSlots {
		localSlot := slot.StartAt.In(location)
		if localSlot.Day() == 15 && localSlot.Month() == time.March && localSlot.Hour() == 9 {
			dstFound = true
			if slot.StartAt.UTC().Hour() != 13 {
				t.Fatalf("DST slot UTC hour = %d, want 13", slot.StartAt.UTC().Hour())
			}
		}
	}
	if !dstFound {
		t.Fatalf("no DST slot found: %#v", dstSlots)
	}
	slots := server.bookingSlots(ctx, page, 14)
	if len(slots) == 0 {
		t.Fatal("no booking slots generated")
	}
	local := slots[0].StartAt.In(location)
	if local.Hour() < 9 || local.Hour() >= 17 || local.Minute()%30 != 0 {
		t.Fatalf("slot outside working hours: %s", local)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(`{"startAt":"`+slots[0].StartAt.Format(time.RFC3339)+`","name":"Casey Client","email":"casey@example.test","note":"Demo"}`), &payload)
	book := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/public/booking/"+slug+"/book", bytes.NewReader(mustJSON(payload)))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("slug", slug)
		recorder := httptest.NewRecorder()
		server.bookSlot(recorder, request)
		return recorder
	}
	first := book()
	if first.Code != http.StatusCreated {
		t.Fatalf("first booking = %d %s", first.Code, first.Body.String())
	}
	second := book()
	if second.Code != http.StatusConflict {
		t.Fatalf("double booking = %d %s", second.Code, second.Body.String())
	}
	var bookings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.bookings WHERE page_id=$1 AND status='confirmed'`, pageID).Scan(&bookings); err != nil || bookings != 1 {
		t.Fatalf("bookings = %d err=%v", bookings, err)
	}
	// Public slots never expose calendar event details.
	slotsRequest := httptest.NewRequest(http.MethodGet, "/api/public/booking/"+slug+"/slots", nil)
	slotsRequest.SetPathValue("slug", slug)
	recorder := httptest.NewRecorder()
	server.publicBookingSlots(recorder, slotsRequest)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "Demo") || strings.Contains(recorder.Body.String(), "Booking Host") {
		t.Fatalf("privacy leak in slots: %s", recorder.Body.String())
	}
	pageRequest := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	pageRequest.SetPathValue("slug", slug)
	pageRecorder := httptest.NewRecorder()
	server.publicBookingPage(pageRecorder, pageRequest)
	if pageRecorder.Code != http.StatusOK {
		t.Fatalf("public booking page = %d %s", pageRecorder.Code, pageRecorder.Body.String())
	}
	pageHTML := pageRecorder.Body.String()
	for _, marker := range []string{"Choose a time", "Your details", "Confirm booking", "bookingSuccess", "America/New_York"} {
		if !strings.Contains(pageHTML, marker) {
			t.Fatalf("public booking page missing %q", marker)
		}
	}
}
