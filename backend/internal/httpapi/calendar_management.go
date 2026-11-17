package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type calendarBody struct {
	Name       *string `json:"name"`
	Color      *string `json:"color"`
	CalendarID string  `json:"calendarId"`
	Country    string  `json:"country"`
	Year       int     `json:"year"`
	Visible    *bool   `json:"visible"`
}

type calendarShareBody struct {
	Email  string `json:"email"`
	Access string `json:"access"`
}

func (s *Server) createCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body calendarBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	name := "New calendar"
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	if name == "" || len([]rune(name)) > 80 {
		writeError(w, http.StatusBadRequest, "CALENDAR_NAME", "Give the calendar a name of 1–80 characters")
		return
	}
	color := "#0f6cbd"
	if body.Color != nil && strings.TrimSpace(*body.Color) != "" {
		if !calendarHexPattern.MatchString(strings.TrimSpace(*body.Color)) {
			writeError(w, http.StatusBadRequest, "CALENDAR_COLOR", "Choose a six-digit hex colour")
			return
		}
		color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	id := newID("calendar")
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind)
		VALUES ($1,$2,$3,$4,$5,'personal')
	`, id, sess.User.TenantID, sess.User.ID, name, color); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the calendar")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.calendar_created", name, "Created a calendar")
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) updateCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body calendarBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	var name, color string
	var kind string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, color, kind FROM calendars WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id).Scan(&name, &color, &kind); err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_NOT_FOUND", "Calendar not found")
		return
	}
	if body.Name != nil {
		value := strings.TrimSpace(*body.Name)
		if value == "" || len([]rune(value)) > 80 {
			writeError(w, http.StatusBadRequest, "CALENDAR_NAME", "Give the calendar a name of 1–80 characters")
			return
		}
		name = value
	}
	if body.Color != nil && strings.TrimSpace(*body.Color) != "" {
		if !calendarHexPattern.MatchString(strings.TrimSpace(*body.Color)) {
			writeError(w, http.StatusBadRequest, "CALENDAR_COLOR", "Choose a six-digit hex colour")
			return
		}
		color = strings.ToLower(strings.TrimSpace(*body.Color))
	}
	visible := true
	visibilitySet := body.Visible != nil
	if visibilitySet {
		visible = *body.Visible
	}
	if _, err := s.store.DB.Exec(r.Context(), `
		UPDATE calendars SET name=$3, color=$4, visible=CASE WHEN $5 THEN $6 ELSE visible END, updated_at=now()
		WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id, name, color, visibilitySet, visible); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the calendar")
		return
	}
	// Keep event colours in sync when the whole calendar is recoloured.
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE calendar_events SET updated_at=now() WHERE user_id=$1 AND calendar_id=$2
	`, sess.User.ID, id)
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) deleteCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if id == "default" {
		writeError(w, http.StatusBadRequest, "CALENDAR_DEFAULT", "The default calendar cannot be deleted")
		return
	}
	var name, kind string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name, kind FROM calendars WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id).Scan(&name, &kind); err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_NOT_FOUND", "Calendar not found")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not remove the calendar")
		return
	}
	defer tx.Rollback(r.Context())
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_publications WHERE calendar_id=$1 AND owner_user_id=$2`, id, sess.User.ID)
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_shares WHERE calendar_id=$1 AND owner_user_id=$2`, id, sess.User.ID)
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_subscriptions WHERE user_id=$1 AND calendar_id=$2`, sess.User.ID, id)
	if _, err := tx.Exec(r.Context(), `DELETE FROM calendar_events WHERE user_id=$1 AND calendar_id=$2`, sess.User.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not delete the calendar events")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM calendars WHERE user_id=$1 AND id=$2`, sess.User.ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not delete the calendar")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the calendar removal")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.calendar_deleted", name, "Deleted a "+kind+" calendar")
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) leaveSharedCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM calendar_shares WHERE calendar_id=$1 AND grantee_user_id=$2
	`, id, sess.User.ID)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "SHARE_NOT_FOUND", "Shared calendar not found")
		return
	}
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) shareCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body calendarShareBody
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if !calendarEmailPattern.MatchString(email) {
		writeError(w, http.StatusBadRequest, "CALENDAR_SHARE_EMAIL", "Enter the email address of an Alt8Mail user")
		return
	}
	if email == strings.ToLower(sess.User.Email) {
		writeError(w, http.StatusBadRequest, "CALENDAR_SHARE_SELF", "That is your own calendar")
		return
	}
	access := "view"
	if strings.EqualFold(strings.TrimSpace(body.Access), "edit") {
		access = "edit"
	}
	var calendarName string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT name FROM calendars WHERE user_id=$1 AND id=$2
	`, sess.User.ID, id).Scan(&calendarName); err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_NOT_FOUND", "Calendar not found")
		return
	}
	var granteeID, granteeName string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT id::text, name FROM users
		WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL AND status='active'
	`, sess.User.TenantID, email).Scan(&granteeID, &granteeName); err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_SHAREE", "No active Alt8Mail user has that address")
		return
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO calendar_shares (calendar_id, owner_user_id, grantee_user_id, grantee_email, access, created_by)
		VALUES ($1,$2,$3,$4,$5,$2)
		ON CONFLICT (calendar_id, owner_user_id, grantee_user_id)
		DO UPDATE SET access=EXCLUDED.access
	`, id, sess.User.ID, granteeID, email, access)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR_SHARE", "Could not share the calendar")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.shared", calendarName, "Shared with "+email+" as "+access)
	s.publishRealtimeTo(granteeID, "calendar")
	s.writeCalendars(w, r)
}

func (s *Server) unshareCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM calendar_shares WHERE calendar_id=$1 AND owner_user_id=$2 AND grantee_user_id=$3
	`, r.PathValue("id"), sess.User.ID, r.PathValue("userId"))
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "CALENDAR_SHARE", "That share no longer exists")
		return
	}
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) loadCalendarShares(ctx context.Context, ownerID, calendarID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT s.grantee_user_id::text, s.grantee_email::text, u.name, s.access, s.created_at
		FROM calendar_shares s
		JOIN users u ON u.id = s.grantee_user_id
		WHERE s.owner_user_id=$1 AND s.calendar_id=$2
		ORDER BY lower(s.grantee_email)
	`, ownerID, calendarID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shares := []map[string]any{}
	for rows.Next() {
		var userID, email, name, access string
		var createdAt time.Time
		if rows.Scan(&userID, &email, &name, &access, &createdAt) == nil {
			shares = append(shares, map[string]any{
				"userId": userID, "email": email, "name": name, "access": access,
				"createdAt": createdAt.UTC().Format(time.RFC3339),
			})
		}
	}
	return shares, rows.Err()
}

func (s *Server) writeCalendars(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	calendars, err := s.loadCalendars(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not reload calendars")
		return
	}
	subscriptions, _ := s.loadCalendarSubscriptions(r.Context(), sess.User.ID)
	events, _ := s.loadCalendarEvents(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "calendars": calendars, "subscriptions": subscriptions,
		"events": events, "calendarEvents": events, "count": len(events),
	})
}

// ---------------------------------------------------------------------------
// Publishing (Google Calendar / Apple Calendar subscriptions)
// ---------------------------------------------------------------------------

func (s *Server) calendarPublicationURL(ctx context.Context, ownerID, calendarID string) (string, error) {
	var tokenEnc string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT token_enc FROM calendar_publications WHERE calendar_id=$1 AND owner_user_id=$2
	`, calendarID, ownerID).Scan(&tokenEnc); err != nil {
		return "", err
	}
	token, err := s.openWorkspaceToken(tokenEnc)
	if err != nil || token == "" {
		return "", fmt.Errorf("published token is unreadable")
	}
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/calendar/feed/" + token + ".ics", nil
}

func (s *Server) getCalendarPublication(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	url, err := s.calendarPublicationURL(r.Context(), sess.User.ID, id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"published": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"published": true, "url": url, "webcal": strings.Replace(url, "https://", "webcal://", 1),
		"googleHint": "In Google Calendar choose Other calendars → From URL and paste this address.",
		"appleHint":  "On iPhone, iPad or Mac choose Add Calendar → Add Subscription Calendar and paste this address.",
	})
}

func (s *Server) publishCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	rotate := r.URL.Query().Get("rotate") == "1"
	var name string
	if err := s.store.DB.QueryRow(r.Context(), `SELECT name FROM calendars WHERE user_id=$1 AND id=$2`, sess.User.ID, id).Scan(&name); err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_NOT_FOUND", "Calendar not found")
		return
	}
	var existing string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT token_enc FROM calendar_publications WHERE calendar_id=$1 AND owner_user_id=$2
	`, id, sess.User.ID).Scan(&existing)
	if existing != "" && !rotate {
		if url, err := s.calendarPublicationURL(r.Context(), sess.User.ID, id); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"published": true, "url": url, "webcal": strings.Replace(url, "https://", "webcal://", 1),
			})
			return
		}
	}
	token := randomToken(32)
	sealed, err := s.sealWorkspaceToken(token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR_PUBLISH", "Could not protect the calendar token")
		return
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO calendar_publications (calendar_id, owner_user_id, token_hash, token_enc, created_by)
		VALUES ($1,$2,$3,$4,$2)
		ON CONFLICT (calendar_id, owner_user_id)
		DO UPDATE SET token_hash=EXCLUDED.token_hash, token_enc=EXCLUDED.token_enc, updated_at=now()
	`, id, sess.User.ID, calendarFeedTokenHash(token), sealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR_PUBLISH", "Could not publish the calendar")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.published", name, "Published an iCalendar feed")
	url := strings.TrimRight(s.cfg.PublicURL, "/") + "/calendar/feed/" + token + ".ics"
	writeJSON(w, http.StatusOK, map[string]any{
		"published": true, "url": url, "webcal": strings.Replace(url, "https://", "webcal://", 1),
		"googleHint": "In Google Calendar choose Other calendars → From URL and paste this address.",
		"appleHint":  "On iPhone, iPad or Mac choose Add Calendar → Add Subscription Calendar and paste this address.",
	})
}

func (s *Server) unpublishCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	_, _ = s.store.DB.Exec(r.Context(), `
		DELETE FROM calendar_publications WHERE calendar_id=$1 AND owner_user_id=$2
	`, id, sess.User.ID)
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.unpublished", id, "Removed the public iCalendar feed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "published": false})
}

func (s *Server) publicCalendarFeed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(r.PathValue("token"), ".ics")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	var calendarID, calendarName, ownerID, ownerEmail string
	var color string
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT p.calendar_id, c.name, c.color, c.user_id::text, u.email::text
		FROM calendar_publications p
		JOIN calendars c ON c.id=p.calendar_id AND c.user_id=p.owner_user_id
		JOIN users u ON u.id=p.owner_user_id
		WHERE p.token_hash=$1
	`, calendarFeedTokenHash(token)).Scan(&calendarID, &calendarName, &color, &ownerID, &ownerEmail)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	owner, err := s.userByID(r.Context(), ownerID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	events, err := s.loadCalendarEvents(r.Context(), owner)
	if err != nil {
		http.Error(w, "Calendar feed unavailable", http.StatusInternalServerError)
		return
	}
	filtered := []map[string]any{}
	for _, event := range events {
		if stateString(event, "calendarId") != calendarID || !stateBool(event, "owned") {
			continue
		}
		if stateString(event, "status") == "cancelled" {
			continue
		}
		filtered = append(filtered, event)
	}
	body := buildCalendarICS(firstNonEmpty(calendarName, "Alt8Mail calendar"), filtered, "", s.cfg.PublicURL)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="calendar.ics"`)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(body))
	_ = ownerEmail
	_ = color
}

// ---------------------------------------------------------------------------
// External iCalendar subscriptions and imports
// ---------------------------------------------------------------------------

func (s *Server) loadCalendarSubscriptions(ctx context.Context, userID string) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, calendar_id, url, name, color, last_sync_at, last_error, enabled, created_at
		FROM calendar_subscriptions WHERE user_id=$1 ORDER BY created_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, calendarID, rawURL, name, color, lastError string
		var lastSync *time.Time
		var enabled bool
		var createdAt time.Time
		if rows.Scan(&id, &calendarID, &rawURL, &name, &color, &lastSync, &lastError, &enabled, &createdAt) == nil {
			entry := map[string]any{
				"id": id, "calendarId": calendarID, "url": rawURL, "name": name,
				"color": color, "lastError": lastError, "enabled": enabled,
				"createdAt": createdAt.UTC().Format(time.RFC3339),
			}
			if lastSync != nil {
				entry["lastSyncAt"] = lastSync.UTC().Format(time.RFC3339)
			}
			out = append(out, entry)
		}
	}
	return out, rows.Err()
}

func (s *Server) subscribeCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		URL   string `json:"url"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	rawURL = strings.Replace(rawURL, "webcal://", "https://", 1)
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		writeError(w, http.StatusBadRequest, "CALENDAR_URL", "Paste an https:// or webcal:// calendar address")
		return
	}
	data, host, err := safeFetchCalendarURL(r.Context(), rawURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "CALENDAR_FETCH", err.Error())
		return
	}
	events, err := parseICS(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CALENDAR_PARSE", err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = host
	}
	color := "#7a4a5c"
	if calendarHexPattern.MatchString(strings.TrimSpace(body.Color)) {
		color = strings.ToLower(strings.TrimSpace(body.Color))
	}
	calendarID := newID("calendar")
	subscriptionID := newID("sub")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not subscribe to the calendar")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, source_url, source_key)
		VALUES ($1,$2,$3,$4,$5,'subscription',$6,$7)
	`, calendarID, sess.User.TenantID, sess.User.ID, truncate(name, 80), color, truncate(rawURL, 1000), subscriptionID); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the subscription calendar")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendar_subscriptions (id, tenant_id, user_id, calendar_id, url, name, color, last_sync_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
	`, subscriptionID, sess.User.TenantID, sess.User.ID, calendarID, truncate(rawURL, 1000), truncate(name, 80), color); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not save the subscription")
		return
	}
	if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, subscriptionID, events); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the calendar events")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the subscription")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.subscribed", name, "Subscribed to "+rawURL)
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) syncCalendarSubscription(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var calendarID, rawURL, name string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT calendar_id, url, name FROM calendar_subscriptions WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&calendarID, &rawURL, &name); err != nil {
		writeError(w, http.StatusNotFound, "SUBSCRIPTION_NOT_FOUND", "Calendar subscription not found")
		return
	}
	data, _, err := safeFetchCalendarURL(r.Context(), rawURL)
	if err != nil {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE calendar_subscriptions SET last_error=$2, updated_at=now() WHERE id=$1 AND user_id=$3`, id, truncate(err.Error(), 500), sess.User.ID)
		writeError(w, http.StatusBadGateway, "CALENDAR_FETCH", err.Error())
		return
	}
	events, err := parseICS(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CALENDAR_PARSE", err.Error())
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not sync the calendar")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `DELETE FROM calendar_events WHERE user_id=$1 AND source_id=$2 AND calendar_id=$3`, sess.User.ID, id, calendarID); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not replace the subscribed events")
		return
	}
	if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, id, events); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the calendar events")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE calendar_subscriptions SET last_sync_at=now(), last_error='', updated_at=now() WHERE id=$1 AND user_id=$2`, id, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not update the subscription")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the calendar sync")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.subscription_synced", name, fmt.Sprintf("Imported %d events", len(events)))
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) deleteCalendarSubscription(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var calendarID string
	if err := s.store.DB.QueryRow(r.Context(), `
		SELECT calendar_id FROM calendar_subscriptions WHERE id=$1 AND user_id=$2
	`, id, sess.User.ID).Scan(&calendarID); err != nil {
		writeError(w, http.StatusNotFound, "SUBSCRIPTION_NOT_FOUND", "Calendar subscription not found")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not remove the subscription")
		return
	}
	defer tx.Rollback(r.Context())
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_events WHERE user_id=$1 AND calendar_id=$2`, sess.User.ID, calendarID)
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendar_subscriptions WHERE id=$1 AND user_id=$2`, id, sess.User.ID)
	_, _ = tx.Exec(r.Context(), `DELETE FROM calendars WHERE user_id=$1 AND id=$2`, sess.User.ID, calendarID)
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the subscription removal")
		return
	}
	s.publishRealtime("calendar")
	s.writeCalendars(w, r)
}

func (s *Server) importCalendarFile(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		Name       string `json:"name"`
		DataBase64 string `json:"dataBase64"`
	}
	if !s.decodeBody(w, r, &body, 12*1024*1024) {
		return
	}
	raw := body.DataBase64
	if comma := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && comma >= 0 {
		raw = raw[comma+1:]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || len(data) == 0 {
		writeError(w, http.StatusBadRequest, "CALENDAR_IMPORT", "Choose an .ics file to import")
		return
	}
	if len(data) > 8*1024*1024 {
		writeError(w, http.StatusRequestEntityTooLarge, "CALENDAR_IMPORT", "Calendar files are limited to 8 MB")
		return
	}
	events, err := parseICS(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CALENDAR_PARSE", err.Error())
		return
	}
	name := safeFileName(body.Name)
	name = strings.TrimSuffix(name, ".ics")
	if name == "" {
		name = "Imported calendar"
	}
	calendarID := newID("calendar")
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the calendar")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, source_key)
		VALUES ($1,$2,$3,$4,'#476b4f','personal','import')
	`, calendarID, sess.User.TenantID, sess.User.ID, truncate(name, 80)); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not create the imported calendar")
		return
	}
	if err := s.insertParsedCalendarEvents(r.Context(), tx, sess.User, calendarID, "", events); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not import the events")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not commit the import")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.imported", name, fmt.Sprintf("Imported %d events", len(events)))
	s.publishRealtime("calendar")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "imported": len(events), "calendarId": calendarID, "name": name})
}

func (s *Server) insertParsedCalendarEvents(ctx context.Context, tx pgx.Tx, user userRecord, calendarID, sourceID string, events []parsedCalendarEvent) error {
	seen := map[string]bool{}
	for index, event := range events {
		id := newID("cal")
		uid := strings.TrimSpace(event.UID)
		if uid == "" {
			uid = fmt.Sprintf("%s-%d", firstNonEmpty(sourceID, id), index)
		}
		if seen[uid] {
			uid = fmt.Sprintf("%s-%d", uid, index)
		}
		seen[uid] = true
		uid = truncate(uid, 200)
		timezone := strings.TrimSpace(event.Timezone)
		if timezone == "" {
			timezone = "UTC"
		}
		if _, err := time.LoadLocation(timezone); err != nil {
			timezone = "UTC"
		}
		title := strings.TrimSpace(event.Title)
		if title == "" {
			title = "Imported event"
		}
		status := event.Status
		if status != "tentative" && status != "cancelled" && status != "confirmed" {
			status = "confirmed"
		}
		transparency := event.Transparency
		if transparency != "free" {
			transparency = "busy"
		}
		organizer := strings.ToLower(strings.TrimSpace(event.Organizer))
		if organizer == "" {
			organizer = user.Email
		}
		rule := strings.TrimSpace(strings.TrimPrefix(event.RecurrenceRule, "RRULE:"))
		rule = truncate(rule, 500)
		_, err := tx.Exec(ctx, `
			INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
			                            starts_at, ends_at, all_day, timezone, status, visibility, recurrence_rule,
			                            organizer_email, organizer_name, etag, resource_name, metadata,
			                            sequence, transparency, reminder_minutes, attachments, response_requested,
			                            source_id, source_uid)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'default',$14,$15,'',$16,
			        $5 || '.ics', '{}'::jsonb, $17, $18, 0, '[]'::jsonb, false, $19, $5)
			ON CONFLICT (user_id, calendar_id, uid) DO UPDATE
			SET title=EXCLUDED.title, description=EXCLUDED.description, location=EXCLUDED.location,
			    starts_at=EXCLUDED.starts_at, ends_at=EXCLUDED.ends_at, all_day=EXCLUDED.all_day,
			    timezone=EXCLUDED.timezone, status=EXCLUDED.status, recurrence_rule=EXCLUDED.recurrence_rule,
			    organizer_email=EXCLUDED.organizer_email, sequence=EXCLUDED.sequence,
			    transparency=EXCLUDED.transparency, source_id=EXCLUDED.source_id,
			    source_uid=EXCLUDED.source_uid, updated_at=now()
		`, id, user.TenantID, user.ID, calendarID, uid, truncate(title, 240),
			truncate(event.Description, 5000), truncate(event.Location, 240), event.StartsAt,
			event.EndsAt, event.AllDay, timezone, status, rule, organizer, newID("etag"),
			event.Sequence, transparency, sourceID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) exportCalendar(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	calendarID := strings.TrimSpace(r.URL.Query().Get("calendarId"))
	calendars, _ := s.loadCalendars(r.Context(), sess.User)
	name := "Alt8Mail calendar"
	events, err := s.loadCalendarEvents(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALENDAR", "Could not export the calendar")
		return
	}
	filtered := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if calendarID != "" && stateString(event, "calendarId") != calendarID {
			continue
		}
		if stateString(event, "status") == "cancelled" {
			continue
		}
		filtered = append(filtered, event)
	}
	if calendarID != "" {
		for _, calendar := range calendars {
			if stateString(calendar, "id") == calendarID {
				name = stateString(calendar, "name")
				break
			}
		}
	}
	body := buildCalendarICS(name, filtered, "", s.cfg.PublicURL)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="alt8mail-calendar.ics"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(body))
}

func (s *Server) rsvpCalendarEvent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 32*1024) {
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "accepted" && status != "tentative" && status != "declined" && status != "needs-action" {
		writeError(w, http.StatusBadRequest, "RSVP_STATUS", "Choose accepted, tentative or declined")
		return
	}
	var eventUID, title, organizerEmail, tenantID string
	var sequence int
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT e.uid, e.title, e.organizer_email::text, e.tenant_id::text, e.sequence
		FROM calendar_events e
		JOIN calendar_attendees a ON a.event_id=e.id
		WHERE e.id=$1 AND lower(a.email)=lower($2)
	`, id, sess.User.Email).Scan(&eventUID, &title, &organizerEmail, &tenantID, &sequence)
	if err != nil {
		writeError(w, http.StatusNotFound, "RSVP_NOT_FOUND", "You are not invited to this event")
		return
	}
	organizerID := ""
	if organizerEmail != "" {
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT id::text FROM users WHERE tenant_id=$1 AND lower(email)=lower($2) AND deleted_at IS NULL
		`, tenantID, organizerEmail).Scan(&organizerID)
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE calendar_attendees a
		SET status=$3, note=$4, responded_at=now(),
		    last_response_sequence=$5, last_response_at=now()
		FROM calendar_events e
		WHERE a.event_id=e.id AND e.id=$1 AND e.sequence=$5
		  AND lower(a.email)=lower($2) AND a.last_response_sequence <= $5
	`, id, sess.User.Email, status, truncate(strings.TrimSpace(body.Note), 500), sequence)
	if err != nil || result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "RSVP_STALE", "This invitation changed. Open the newest invitation and respond again")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO calendar_invite_replies
		(event_id, tenant_id, event_uid, attendee_email, sequence, partstat, method, dedup_key, source, applied, detail)
		VALUES ($1,$2,$3,$4,$5,$6,'REPLY',$7,'app',true,$8)
		ON CONFLICT (event_id, dedup_key) DO NOTHING
	`, id, tenantID, eventUID, sess.User.Email, sequence, strings.ToUpper(status), newID("rsvp"), "In-app response")
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.rsvp", id, "RSVP "+status)
	if organizerID != "" && organizerID != sess.User.ID {
		if organizer, lookupErr := s.userByID(r.Context(), organizerID); lookupErr == nil {
			s.insertAudit(organizer, "calendar.rsvp-received", title,
				fmt.Sprintf("Guest %s responded %s · sequence %d · in-app", sess.User.Email, status, sequence),
				clientIP(r), sanitizeUserAgent(r.UserAgent()))
		}
	}
	s.publishRealtime("calendar")
	event, _ := s.loadCalendarEvent(r.Context(), sess.User, id)
	if event != nil && status != "needs-action" {
		attendee := sess.User
		go s.sendCalendarReplyEmail(context.Background(), attendee, event, status)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event})
}

func (s *Server) resendCalendarInvites(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	event, err := s.loadCalendarEvent(r.Context(), sess.User, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Event not found")
		return
	}
	access := stateString(event, "access")
	if access != "owner" && access != "edit" {
		writeError(w, http.StatusForbidden, "EVENT_ACCESS", "Only the organizer or a calendar editor can resend invitations")
		return
	}
	if stateString(event, "status") == "cancelled" {
		writeError(w, http.StatusConflict, "EVENT_CANCELLED", "This event has been cancelled")
		return
	}
	filtered := []any{}
	waiting := []string{}
	for _, raw := range stateSlice(event, "attendeeList") {
		entry, ok := raw.(map[string]any)
		if !ok || stateBool(entry, "organizer") {
			continue
		}
		email := stateString(entry, "email")
		if email == "" || stateString(entry, "status") != "needs-action" {
			continue
		}
		filtered = append(filtered, entry)
		waiting = append(waiting, email)
	}
	if len(filtered) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resent": 0, "waiting": []string{}, "event": event})
		return
	}
	copyEvent := deepCopyMap(event)
	copyEvent["attendeeList"] = filtered
	s.sendCalendarInvitePayload(context.Background(), sess.User, copyEvent, "REQUEST")
	s.recordWorkspaceAudit(r.Context(), sess, "calendar.invites.resent", stateString(event, "title"),
		fmt.Sprintf("Resent invitation to %d guest(s) awaiting a response", len(filtered)))
	event, _ = s.loadCalendarEvent(r.Context(), sess.User, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resent": len(filtered), "waiting": waiting, "event": event})
}

func (s *Server) getCalendarShares(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	shares, err := s.loadCalendarShares(r.Context(), sess.User.ID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "CALENDAR_NOT_FOUND", "Calendar not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares, "count": len(shares)})
}
