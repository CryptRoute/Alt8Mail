package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	davCalendarCollection    = "default"
	davAddressbookCollection = "default"
	maxDAVBodyBytes          = 2 * 1024 * 1024
	davContentTypeCalendar   = "text/calendar; charset=utf-8"
	davContentTypeVCard      = "text/vcard; charset=utf-8"
)

type davAttendee struct {
	Email  string `json:"email"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type davCalendarEvent struct {
	ID             string
	UID            string
	Title          string
	Description    string
	Location       string
	Timezone       string
	Status         string
	Visibility     string
	RecurrenceRule string
	Organizer      string
	ETag           string
	ResourceName   string
	StartsAt       time.Time
	EndsAt         time.Time
	AllDay         bool
	UpdatedAt      time.Time
	Attendees      []davAttendee
}

type davContact struct {
	ID           string
	UID          string
	Email        string
	Name         string
	Company      string
	Title        string
	Phone        string
	Location     string
	Notes        string
	ETag         string
	ResourceName string
	UpdatedAt    time.Time
}

type davItem struct {
	Href        string
	Kind        string // root, principal, home, calendar, addressbook, event, contact
	DisplayName string
	ETag        string
	ContentType string
	Data        []byte
	UpdatedAt   time.Time
	SyncToken   string
}

func (s *Server) davWellKnown(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dav/", http.StatusMovedPermanently)
}

func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND, PROPPATCH, REPORT, PUT, DELETE")
		w.Header().Set("DAV", "1, 2, 3, calendar-access, addressbook, extended-mkcol")
		w.Header().Set("MS-Author-Via", "DAV")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	user, ok := s.davRequireAuth(w, r)
	if !ok {
		return
	}
	segments, valid := davSegments(r.URL.Path)
	if !valid {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PROPFIND":
		s.davPropfind(w, r, user, segments)
	case "REPORT":
		s.davReport(w, r, user, segments)
	case http.MethodGet, http.MethodHead:
		s.davGet(w, r, user, segments)
	case http.MethodPut:
		s.davPut(w, r, user, segments)
	case http.MethodDelete:
		s.davDelete(w, r, user, segments)
	case "PROPPATCH":
		s.davProppatch(w, r, user, segments)
	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PROPFIND, PROPPATCH, REPORT, PUT, DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func davSegments(path string) ([]string, bool) {
	if path == "/dav" || path == "/dav/" {
		return []string{}, true
	}
	if !strings.HasPrefix(path, "/dav/") {
		return nil, false
	}
	rest := strings.Trim(strings.TrimPrefix(path, "/dav"), "/")
	if rest == "" {
		return []string{}, true
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") || len(part) > 240 {
			return nil, false
		}
	}
	return parts, true
}

func davUserMatches(user userRecord, pathUser string) bool {
	return strings.EqualFold(user.ID, pathUser)
}

func davPrincipalHref(user userRecord) string {
	return "/dav/principals/" + user.ID + "/"
}

func davCalendarHome(user userRecord) string {
	return "/dav/calendars/" + user.ID + "/"
}

func davAddressbookHome(user userRecord) string {
	return "/dav/addressbooks/" + user.ID + "/"
}

func davCalendarCollectionHref(user userRecord) string {
	return davCalendarHome(user) + davCalendarCollection + "/"
}

func davAddressbookCollectionHref(user userRecord) string {
	return davAddressbookHome(user) + davAddressbookCollection + "/"
}

func davHrefEscape(value string) string {
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func davDepthOne(r *http.Request) bool {
	depth := strings.ToLower(strings.TrimSpace(r.Header.Get("Depth")))
	return depth != "0"
}

func (s *Server) davPropfind(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxDAVBodyBytes+1))
	wantData := bytes.Contains(body, []byte("calendar-data")) || bytes.Contains(body, []byte("address-data"))
	items := []davItem{}
	depthOne := davDepthOne(r)

	switch {
	case len(segments) == 0:
		items = append(items, davItem{
			Href: "/dav/", Kind: "root", DisplayName: "Meridian Mail",
		})
	case len(segments) == 2 && segments[0] == "principals":
		if !davUserMatches(user, segments[1]) {
			http.NotFound(w, r)
			return
		}
		items = append(items, davItem{
			Href: davPrincipalHref(user), Kind: "principal", DisplayName: user.Name,
		})
	case len(segments) == 2 && (segments[0] == "calendars" || segments[0] == "addressbooks"):
		if !davUserMatches(user, segments[1]) {
			http.NotFound(w, r)
			return
		}
		home := davCalendarHome(user)
		if segments[0] == "addressbooks" {
			home = davAddressbookHome(user)
		}
		items = append(items, davItem{Href: home, Kind: "home", DisplayName: "Meridian Mail"})
		if depthOne {
			items = append(items, s.davCollectionItem(r.Context(), user, segments[0]))
		}
	case len(segments) == 3 && (segments[0] == "calendars" || segments[0] == "addressbooks"):
		if !davUserMatches(user, segments[1]) || segments[2] != "default" {
			http.NotFound(w, r)
			return
		}
		collection := s.davCollectionItem(r.Context(), user, segments[0])
		items = append(items, collection)
		if depthOne {
			if segments[0] == "calendars" {
				events, err := s.loadDAVCalendarEvents(r.Context(), user.ID)
				if err != nil {
					writeDAVError(w, http.StatusInternalServerError, "calendar data could not be loaded")
					return
				}
				for _, event := range events {
					items = append(items, davEventItem(user, event, wantData))
				}
			} else {
				contacts, err := s.loadDAVContacts(r.Context(), user.ID)
				if err != nil {
					writeDAVError(w, http.StatusInternalServerError, "address book data could not be loaded")
					return
				}
				for _, contact := range contacts {
					items = append(items, davContactItem(user, contact, wantData))
				}
			}
		}
	case len(segments) == 4 && (segments[0] == "calendars" || segments[0] == "addressbooks"):
		if !davUserMatches(user, segments[1]) || segments[2] != "default" {
			http.NotFound(w, r)
			return
		}
		if segments[0] == "calendars" {
			event, err := s.loadDAVCalendarEventByResource(r.Context(), user.ID, segments[3])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			items = append(items, davEventItem(user, event, true))
		} else {
			contact, err := s.loadDAVContactByResource(r.Context(), user.ID, segments[3])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			items = append(items, davContactItem(user, contact, true))
		}
	default:
		http.NotFound(w, r)
		return
	}
	writeDAVMultistatus(w, items, user)
}

func (s *Server) davCollectionItem(ctx context.Context, user userRecord, kind string) davItem {
	if kind == "calendars" || kind == "calendar" {
		token := s.davSyncToken(ctx, user.ID, "calendar")
		return davItem{
			Href: davCalendarCollectionHref(user), Kind: "calendar", DisplayName: "Personal calendar",
			SyncToken: token,
		}
	}
	token := s.davSyncToken(ctx, user.ID, "addressbook")
	return davItem{
		Href: davAddressbookCollectionHref(user), Kind: "addressbook", DisplayName: "Personal address book",
		SyncToken: token,
	}
}

func (s *Server) davSyncToken(ctx context.Context, userID, kind string) string {
	var stamp string
	var count int64
	query := `
		SELECT COALESCE(to_char(max(updated_at) AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISSUS'), '0'), count(*)
		FROM calendar_events WHERE user_id = $1`
	if kind == "addressbook" {
		query = `
			SELECT COALESCE(to_char(max(updated_at) AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISSUS'), '0'), count(*)
			FROM contacts WHERE user_id = $1`
	}
	_ = s.store.DB.QueryRow(ctx, query, userID).Scan(&stamp, &count)
	return "urn:meridian:sync:" + kind + ":" + stamp + ":" + strconv.FormatInt(count, 10)
}

func (s *Server) davReport(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	if len(segments) < 3 || len(segments) > 4 || (segments[0] != "calendars" && segments[0] != "addressbooks") {
		http.NotFound(w, r)
		return
	}
	if !davUserMatches(user, segments[1]) || segments[2] != "default" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDAVBodyBytes+1))
	if err != nil || len(body) > maxDAVBodyBytes {
		writeDAVError(w, http.StatusBadRequest, "report body is too large")
		return
	}
	reportKind := strings.ToLower(segments[0])
	items := []davItem{}
	var syncToken string
	if reportKind == "calendars" {
		events, err := s.loadDAVCalendarEvents(r.Context(), user.ID)
		if err != nil {
			writeDAVError(w, http.StatusInternalServerError, "calendar data could not be loaded")
			return
		}
		hrefs := davReportHrefs(body)
		start, end, hasRange := davReportTimeRange(body)
		for _, event := range events {
			if len(hrefs) > 0 && !davHrefInSet(hrefs, davCalendarCollectionHref(user)+davResourceName(event.ResourceName, event.ID, ".ics")) {
				continue
			}
			if hasRange && event.RecurrenceRule == "" && !davEventOverlaps(event, start, end) {
				continue
			}
			items = append(items, davEventItem(user, event, true))
		}
		syncToken = s.davSyncToken(r.Context(), user.ID, "calendar")
	} else {
		contacts, err := s.loadDAVContacts(r.Context(), user.ID)
		if err != nil {
			writeDAVError(w, http.StatusInternalServerError, "address book data could not be loaded")
			return
		}
		hrefs := davReportHrefs(body)
		for _, contact := range contacts {
			if len(hrefs) > 0 && !davHrefInSet(hrefs, davAddressbookCollectionHref(user)+davResourceName(contact.ResourceName, contact.ID, ".vcf")) {
				continue
			}
			items = append(items, davContactItem(user, contact, true))
		}
		syncToken = s.davSyncToken(r.Context(), user.ID, "addressbook")
	}
	if bytes.Contains(bytes.ToLower(body), []byte("sync-collection")) {
		requested := davSyncTokenFromBody(body)
		if requested != "" && requested == syncToken {
			items = nil
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Href < items[j].Href })
	writeDAVMultistatusWithToken(w, items, user, syncToken)
}

func davResourceName(name, id, ext string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return id + ext
}

var davHrefPattern = regexp.MustCompile(`(?is)<[^>]*href[^>]*>([^<]+)</[^>]*href>`)

func davReportHrefs(body []byte) map[string]bool {
	hrefs := map[string]bool{}
	for _, match := range davHrefPattern.FindAllSubmatch(body, -1) {
		value := strings.TrimSpace(string(match[1]))
		if parsed, err := url.Parse(value); err == nil {
			value = parsed.Path
		}
		hrefs[strings.TrimSuffix(value, "/")] = true
	}
	return hrefs
}

func davHrefInSet(hrefs map[string]bool, href string) bool {
	return hrefs[strings.TrimSuffix(href, "/")]
}

func davReportTimeRange(body []byte) (time.Time, time.Time, bool) {
	lower := strings.ToLower(string(body))
	index := strings.Index(lower, "<c:time-range")
	closeIndex := -1
	if index >= 0 {
		closeIndex = strings.Index(lower[index:], "/>")
		if closeIndex < 0 {
			closeIndex = strings.Index(lower[index:], "</c:time-range>")
		}
	}
	if index < 0 || closeIndex < 0 {
		// Accept any namespace prefix.
		re := regexp.MustCompile(`(?is)<[a-z0-9]+:time-range\s+([^>]+?)/?>`)
		match := re.FindSubmatch(body)
		if match == nil {
			return time.Time{}, time.Time{}, false
		}
		return parseDAVTimeRange(string(match[1]))
	}
	segment := string(body[index : index+closeIndex])
	start := regexp.MustCompile(`(?i)start="([^"]+)"`).FindStringSubmatch(segment)
	end := regexp.MustCompile(`(?i)end="([^"]+)"`).FindStringSubmatch(segment)
	attrs := ""
	if len(start) > 1 {
		attrs += ` start="` + start[1] + `"`
	}
	if len(end) > 1 {
		attrs += ` end="` + end[1] + `"`
	}
	return parseDAVTimeRange(attrs)
}

func parseDAVTimeRange(attrs string) (time.Time, time.Time, bool) {
	read := func(name string) time.Time {
		match := regexp.MustCompile(`(?i)` + name + `="([^"]+)"`).FindStringSubmatch(attrs)
		if len(match) < 2 {
			return time.Time{}
		}
		if parsed, err := time.Parse("20060102T150405Z", match[1]); err == nil {
			return parsed
		}
		if parsed, err := time.Parse("20060102", match[1]); err == nil {
			return parsed
		}
		return time.Time{}
	}
	start, end := read("start"), read("end")
	if start.IsZero() && end.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

func davEventOverlaps(event davCalendarEvent, start, end time.Time) bool {
	if !start.IsZero() && !event.EndsAt.After(start) {
		return false
	}
	if !end.IsZero() && !event.StartsAt.Before(end) {
		return false
	}
	return true
}

func (s *Server) davProppatch(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	// Collection display names are fixed; acknowledge the request so clients
	// such as Thunderbird can finish collection setup without an error loop.
	items := []davItem{{Href: requestPath(r), Kind: "empty"}}
	writeDAVMultistatus(w, items, user)
}

func requestPath(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = r.URL.Path
	}
	return path
}

func (s *Server) davGet(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	if len(segments) != 4 || !davUserMatches(user, segments[1]) || segments[2] != "default" {
		http.NotFound(w, r)
		return
	}
	if segments[0] == "calendars" {
		event, err := s.loadDAVCalendarEventByResource(r.Context(), user.ID, segments[3])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := calendarToICS(event, user)
		etag := davETag(event.ETag, event.ID, event.UpdatedAt)
		writeDAVResource(w, r, data, davContentTypeCalendar, etag, event.UpdatedAt)
		return
	}
	if segments[0] == "addressbooks" {
		contact, err := s.loadDAVContactByResource(r.Context(), user.ID, segments[3])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := contactToVCard(contact)
		etag := davETag(contact.ETag, contact.ID, contact.UpdatedAt)
		writeDAVResource(w, r, data, davContentTypeVCard, etag, contact.UpdatedAt)
		return
	}
	http.NotFound(w, r)
}

func davETag(value, id string, updated time.Time) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return hashShort(id + ":" + updated.UTC().Format(time.RFC3339Nano))
}

func writeDAVResource(w http.ResponseWriter, r *http.Request, data []byte, contentType, etag string, updated time.Time) {
	quoted := `"` + etag + `"`
	if davIfNoneMatch(r, quoted) {
		w.Header().Set("ETag", quoted)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", quoted)
	w.Header().Set("Cache-Control", "no-cache")
	if !updated.IsZero() {
		w.Header().Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) davPut(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	if len(segments) != 4 || !davUserMatches(user, segments[1]) || segments[2] != "default" {
		http.NotFound(w, r)
		return
	}
	resource := segments[3]
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDAVBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxDAVBodyBytes {
		writeDAVError(w, http.StatusBadRequest, "request body is empty or too large")
		return
	}
	if segments[0] == "calendars" {
		if !strings.HasSuffix(strings.ToLower(resource), ".ics") {
			writeDAVError(w, http.StatusForbidden, "calendar resources must use the .ics extension")
			return
		}
		event, err := parseCalendarICS(body, user, s.tenantPolicyFor(r.Context(), user.TenantID).Branding.PrimaryColor)
		if err != nil {
			writeDAVError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing, _ := s.loadDAVCalendarEventByResource(r.Context(), user.ID, resource)
		if existing.ID == "" {
			existing, _ = s.loadDAVCalendarEventByUID(r.Context(), user.ID, event.UID)
		}
		if !davPreconditionsPass(r, existing.ETag, existing.ID != "", event.UID) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		created := existing.ID == ""
		id, etag := existing.ID, newID("etag")
		if created {
			id = newID("cal")
		}
		err = s.saveDAVCalendarEvent(r.Context(), user, id, resource, event, etag, created)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				writeDAVError(w, http.StatusConflict, "another event already uses that UID")
			} else {
				writeDAVError(w, http.StatusInternalServerError, "the event could not be saved")
			}
			return
		}
		w.Header().Set("ETag", `"`+etag+`"`)
		if created {
			w.Header().Set("Location", davHrefEscape(davCalendarCollectionHref(user)+resource))
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	if segments[0] == "addressbooks" {
		if !strings.HasSuffix(strings.ToLower(resource), ".vcf") {
			writeDAVError(w, http.StatusForbidden, "address book resources must use the .vcf extension")
			return
		}
		contact, err := parseVCard(body)
		if err != nil {
			writeDAVError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing, _ := s.loadDAVContactByResource(r.Context(), user.ID, resource)
		if existing.ID == "" {
			existing, _ = s.loadDAVContactByUID(r.Context(), user.ID, contact.UID)
		}
		if !davPreconditionsPass(r, existing.ETag, existing.ID != "", contact.UID) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		created := existing.ID == ""
		id, etag := existing.ID, newID("etag")
		if created {
			id = newID("contact")
		}
		err = s.saveDAVContact(r.Context(), user, id, resource, contact, etag, created)
		if err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique") {
				writeDAVError(w, http.StatusConflict, "another contact already uses that email address")
			} else {
				writeDAVError(w, http.StatusInternalServerError, "the contact could not be saved")
			}
			return
		}
		w.Header().Set("ETag", `"`+etag+`"`)
		if created {
			w.Header().Set("Location", davHrefEscape(davAddressbookCollectionHref(user)+resource))
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	http.NotFound(w, r)
}

func davPreconditionsPass(r *http.Request, existingETag string, exists bool, uid string) bool {
	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if ifNoneMatch != "" {
		if ifNoneMatch == "*" && exists {
			return false
		}
		if exists && davETagListMatches(ifNoneMatch, existingETag) {
			return false
		}
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch != "" {
		if !exists {
			return false
		}
		if ifMatch != "*" && !davETagListMatches(ifMatch, existingETag) {
			return false
		}
	}
	return true
}

func davETagListMatches(header, etag string) bool {
	quoted := `"` + etag + `"`
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == quoted || candidate == etag || strings.Trim(candidate, `"`) == etag {
			return true
		}
	}
	return false
}

func davIfNoneMatch(r *http.Request, quotedETag string) bool {
	header := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == quotedETag || strings.Trim(candidate, `"`) == strings.Trim(quotedETag, `"`) {
			return true
		}
	}
	return false
}

func (s *Server) davDelete(w http.ResponseWriter, r *http.Request, user userRecord, segments []string) {
	if len(segments) != 4 || !davUserMatches(user, segments[1]) || segments[2] != "default" {
		http.NotFound(w, r)
		return
	}
	var etag string
	var err error
	if segments[0] == "calendars" {
		existing, loadErr := s.loadDAVCalendarEventByResource(r.Context(), user.ID, segments[3])
		if loadErr != nil {
			http.NotFound(w, r)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
		if ifMatch != "" && ifMatch != "*" && !davETagListMatches(ifMatch, davETag(existing.ETag, existing.ID, existing.UpdatedAt)) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		etag = existing.ETag
		_, err = s.store.DB.Exec(r.Context(), `DELETE FROM calendar_events WHERE id = $1 AND user_id = $2`, existing.ID, user.ID)
	} else if segments[0] == "addressbooks" {
		existing, loadErr := s.loadDAVContactByResource(r.Context(), user.ID, segments[3])
		if loadErr != nil {
			http.NotFound(w, r)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
		if ifMatch != "" && ifMatch != "*" && !davETagListMatches(ifMatch, davETag(existing.ETag, existing.ID, existing.UpdatedAt)) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		etag = existing.ETag
		_, err = s.store.DB.Exec(r.Context(), `DELETE FROM contacts WHERE id = $1 AND user_id = $2`, existing.ID, user.ID)
	} else {
		http.NotFound(w, r)
		return
	}
	_ = etag
	if err != nil {
		writeDAVError(w, http.StatusInternalServerError, "the resource could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) loadDAVCalendarEvents(ctx context.Context, userID string) ([]davCalendarEvent, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT e.id, e.uid, e.title, e.description, e.location, e.timezone, e.status, e.visibility,
		       e.recurrence_rule, e.organizer_email::text, e.etag, e.resource_name, e.starts_at, e.ends_at,
		       e.all_day, e.updated_at,
		       COALESCE(json_agg(json_build_object('email', a.email::text, 'name', a.name, 'status', a.status))
		                FILTER (WHERE a.email IS NOT NULL), '[]'::json)
		FROM calendar_events e
		LEFT JOIN calendar_attendees a ON a.event_id = e.id
		WHERE e.user_id = $1
		GROUP BY e.id
		ORDER BY e.starts_at
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []davCalendarEvent{}
	for rows.Next() {
		var event davCalendarEvent
		var attendees []byte
		if err := rows.Scan(&event.ID, &event.UID, &event.Title, &event.Description, &event.Location,
			&event.Timezone, &event.Status, &event.Visibility, &event.RecurrenceRule, &event.Organizer,
			&event.ETag, &event.ResourceName, &event.StartsAt, &event.EndsAt, &event.AllDay, &event.UpdatedAt,
			&attendees); err == nil {
			_ = json.Unmarshal(attendees, &event.Attendees)
			events = append(events, event)
		}
	}
	return events, rows.Err()
}

func (s *Server) loadDAVCalendarEventByResource(ctx context.Context, userID, resource string) (davCalendarEvent, error) {
	events, err := s.loadDAVCalendarEvents(ctx, userID)
	if err != nil {
		return davCalendarEvent{}, err
	}
	for _, event := range events {
		if event.ResourceName == resource || (event.ResourceName == "" && event.ID+".ics" == resource) {
			return event, nil
		}
	}
	return davCalendarEvent{}, fmt.Errorf("event not found")
}

func (s *Server) loadDAVCalendarEventByUID(ctx context.Context, userID, uid string) (davCalendarEvent, error) {
	events, err := s.loadDAVCalendarEvents(ctx, userID)
	if err != nil {
		return davCalendarEvent{}, err
	}
	for _, event := range events {
		if event.UID == uid {
			return event, nil
		}
	}
	return davCalendarEvent{}, fmt.Errorf("event not found")
}

func (s *Server) loadDAVContacts(ctx context.Context, userID string) ([]davContact, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, uid, email::text, name, company, title, phone, location, notes, etag, resource_name, updated_at
		FROM contacts WHERE user_id = $1 ORDER BY name, email
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	contacts := []davContact{}
	for rows.Next() {
		var contact davContact
		if err := rows.Scan(&contact.ID, &contact.UID, &contact.Email, &contact.Name, &contact.Company,
			&contact.Title, &contact.Phone, &contact.Location, &contact.Notes, &contact.ETag,
			&contact.ResourceName, &contact.UpdatedAt); err == nil {
			contacts = append(contacts, contact)
		}
	}
	return contacts, rows.Err()
}

func (s *Server) loadDAVContactByResource(ctx context.Context, userID, resource string) (davContact, error) {
	contacts, err := s.loadDAVContacts(ctx, userID)
	if err != nil {
		return davContact{}, err
	}
	for _, contact := range contacts {
		if contact.ResourceName == resource || (contact.ResourceName == "" && contact.ID+".vcf" == resource) {
			return contact, nil
		}
	}
	return davContact{}, fmt.Errorf("contact not found")
}

func (s *Server) loadDAVContactByUID(ctx context.Context, userID, uid string) (davContact, error) {
	contacts, err := s.loadDAVContacts(ctx, userID)
	if err != nil {
		return davContact{}, err
	}
	for _, contact := range contacts {
		if contact.UID == uid {
			return contact, nil
		}
	}
	return davContact{}, fmt.Errorf("contact not found")
}

func (s *Server) saveDAVCalendarEvent(ctx context.Context, user userRecord, id, resource string,
	event davCalendarEvent, etag string, created bool) error {
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if created {
		_, err = tx.Exec(ctx, `
			INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
			                            starts_at, ends_at, all_day, timezone, status, visibility, recurrence_rule,
			                            organizer_email, etag, resource_name, metadata)
			VALUES ($1,$2,$3,'default',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'{}')
		`, id, user.TenantID, user.ID, event.UID, event.Title, event.Description, event.Location,
			event.StartsAt, event.EndsAt, event.AllDay, event.Timezone, event.Status, event.Visibility,
			event.RecurrenceRule, event.Organizer, etag, resource)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE calendar_events SET uid=$3, title=$4, description=$5, location=$6, starts_at=$7, ends_at=$8,
			       all_day=$9, timezone=$10, status=$11, visibility=$12, recurrence_rule=$13,
			       organizer_email=$14, etag=$15, resource_name=$16, updated_at=now()
			WHERE id=$1 AND user_id=$2
		`, id, user.ID, event.UID, event.Title, event.Description, event.Location, event.StartsAt,
			event.EndsAt, event.AllDay, event.Timezone, event.Status, event.Visibility,
			event.RecurrenceRule, event.Organizer, etag, resource)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM calendar_attendees WHERE event_id = $1`, id); err != nil {
		return err
	}
	for _, attendee := range event.Attendees {
		_, err = tx.Exec(ctx, `
			INSERT INTO calendar_attendees (event_id, email, name, status)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (event_id, email) DO UPDATE SET name=EXCLUDED.name, status=EXCLUDED.status
		`, id, attendee.Email, attendee.Name, firstNonEmpty(attendee.Status, "needs-action"))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Server) saveDAVContact(ctx context.Context, user userRecord, id, resource string,
	contact davContact, etag string, created bool) error {
	if created {
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO contacts (id, tenant_id, user_id, email, name, company, title, phone, location, notes,
			                      source, uid, resource_name, etag)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'carddav',$11,$12,$13)
		`, id, user.TenantID, user.ID, contact.Email, contact.Name, contact.Company, contact.Title,
			contact.Phone, contact.Location, contact.Notes, contact.UID, resource, etag)
		return err
	}
	_, err := s.store.DB.Exec(ctx, `
		UPDATE contacts SET email=$3, name=$4, company=$5, title=$6, phone=$7, location=$8, notes=$9,
		       uid=$10, resource_name=$11, etag=$12, updated_at=now()
		WHERE id=$1 AND user_id=$2
	`, id, user.ID, contact.Email, contact.Name, contact.Company, contact.Title, contact.Phone,
		contact.Location, contact.Notes, contact.UID, resource, etag)
	return err
}

func davEventItem(user userRecord, event davCalendarEvent, includeData bool) davItem {
	href := davCalendarCollectionHref(user) + davResourceName(event.ResourceName, event.ID, ".ics")
	item := davItem{
		Href: href, Kind: "event", DisplayName: event.Title,
		ETag: davETag(event.ETag, event.ID, event.UpdatedAt), ContentType: davContentTypeCalendar,
		UpdatedAt: event.UpdatedAt,
	}
	if includeData {
		item.Data = calendarToICS(event, user)
	}
	return item
}

func davContactItem(user userRecord, contact davContact, includeData bool) davItem {
	href := davAddressbookCollectionHref(user) + davResourceName(contact.ResourceName, contact.ID, ".vcf")
	item := davItem{
		Href: href, Kind: "contact", DisplayName: contact.Name,
		ETag: davETag(contact.ETag, contact.ID, contact.UpdatedAt), ContentType: davContentTypeVCard,
		UpdatedAt: contact.UpdatedAt,
	}
	if includeData {
		item.Data = contactToVCard(contact)
	}
	return item
}

func writeDAVError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><D:error xmlns:D="DAV:"><D:responsedescription>` +
		xmlEscape(message) + `</D:responsedescription></D:error>`))
}

func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func davPropfindStatus() string {
	return "HTTP/1.1 200 OK"
}

func writeDAVMultistatus(w http.ResponseWriter, items []davItem, user userRecord) {
	writeDAVMultistatusWithToken(w, items, user, "")
}

func writeDAVMultistatusWithToken(w http.ResponseWriter, items []davItem, user userRecord, syncToken string) {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	builder.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/">`)
	for _, item := range items {
		builder.WriteString(`<D:response><D:href>`)
		builder.WriteString(xmlEscape(davHrefEscape(item.Href)))
		builder.WriteString(`</D:href><D:propstat><D:prop>`)
		builder.WriteString(davProps(item, user))
		builder.WriteString(`</D:prop><D:status>`)
		builder.WriteString(davPropfindStatus())
		builder.WriteString(`</D:status></D:propstat></D:response>`)
	}
	if syncToken != "" {
		builder.WriteString(`<D:sync-token>`)
		builder.WriteString(xmlEscape(syncToken))
		builder.WriteString(`</D:sync-token>`)
	}
	builder.WriteString(`</D:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access, addressbook, extended-mkcol")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(builder.String()))
}

func davProps(item davItem, user userRecord) string {
	var props strings.Builder
	switch item.Kind {
	case "root":
		props.WriteString(`<D:resourcetype><D:collection/></D:resourcetype><D:displayname>Meridian Mail</D:displayname><D:current-user-principal><D:href>` +
			xmlEscape(davPrincipalHref(user)) + `</D:href></D:current-user-principal>`)
	case "principal":
		props.WriteString(`<D:resourcetype><D:principal/></D:resourcetype><D:displayname>`)
		props.WriteString(xmlEscape(item.DisplayName))
		props.WriteString(`</D:displayname><D:current-user-principal><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:current-user-principal><D:principal-URL><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:principal-URL><C:calendar-home-set><D:href>`)
		props.WriteString(xmlEscape(davCalendarHome(user)))
		props.WriteString(`</D:href></C:calendar-home-set><A:addressbook-home-set><D:href>`)
		props.WriteString(xmlEscape(davAddressbookHome(user)))
		props.WriteString(`</D:href></A:addressbook-home-set>`)
	case "home":
		props.WriteString(`<D:resourcetype><D:collection/></D:resourcetype><D:displayname>`)
		props.WriteString(xmlEscape(item.DisplayName))
		props.WriteString(`</D:displayname><D:current-user-principal><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:current-user-principal>`)
	case "calendar":
		props.WriteString(`<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>`)
		props.WriteString(`<D:displayname>Personal calendar</D:displayname><D:current-user-principal><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:current-user-principal><D:owner><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:owner><CS:getctag>`)
		props.WriteString(xmlEscape(firstNonEmpty(item.SyncToken, "ctag")))
		props.WriteString(`</CS:getctag><D:sync-token>`)
		props.WriteString(xmlEscape(item.SyncToken))
		props.WriteString(`</D:sync-token><C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set><D:current-user-privilege-set><D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege><D:privilege><D:write-content/></D:privilege></D:current-user-privilege-set>`)
	case "addressbook":
		props.WriteString(`<D:resourcetype><D:collection/><A:addressbook/></D:resourcetype>`)
		props.WriteString(`<D:displayname>Personal address book</D:displayname><D:current-user-principal><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:current-user-principal><D:owner><D:href>`)
		props.WriteString(xmlEscape(davPrincipalHref(user)))
		props.WriteString(`</D:href></D:owner><CS:getctag>`)
		props.WriteString(xmlEscape(firstNonEmpty(item.SyncToken, "ctag")))
		props.WriteString(`</CS:getctag><D:sync-token>`)
		props.WriteString(xmlEscape(item.SyncToken))
		props.WriteString(`</D:sync-token><A:supported-address-data><A:address-data-type content-type="text/vcard" version="3.0"/></A:supported-address-data><D:current-user-privilege-set><D:privilege><D:read/></D:privilege><D:privilege><D:write/></D:privilege><D:privilege><D:write-content/></D:privilege></D:current-user-privilege-set>`)
	case "event":
		props.WriteString(`<D:resourcetype/><D:getetag>"` + xmlEscape(item.ETag) + `"</D:getetag><D:getcontenttype>`)
		props.WriteString(davContentTypeCalendar)
		props.WriteString(`</D:getcontenttype><D:getlastmodified>`)
		props.WriteString(item.UpdatedAt.UTC().Format(http.TimeFormat))
		props.WriteString(`</D:getlastmodified><D:displayname>`)
		props.WriteString(xmlEscape(item.DisplayName))
		props.WriteString(`</D:displayname>`)
		if len(item.Data) > 0 {
			props.WriteString(`<C:calendar-data>`)
			props.WriteString(xmlEscape(string(item.Data)))
			props.WriteString(`</C:calendar-data>`)
		}
	case "contact":
		props.WriteString(`<D:resourcetype/><D:getetag>"` + xmlEscape(item.ETag) + `"</D:getetag><D:getcontenttype>`)
		props.WriteString(davContentTypeVCard)
		props.WriteString(`</D:getcontenttype><D:getlastmodified>`)
		props.WriteString(item.UpdatedAt.UTC().Format(http.TimeFormat))
		props.WriteString(`</D:getlastmodified><D:displayname>`)
		props.WriteString(xmlEscape(item.DisplayName))
		props.WriteString(`</D:displayname>`)
		if len(item.Data) > 0 {
			props.WriteString(`<A:address-data>`)
			props.WriteString(xmlEscape(string(item.Data)))
			props.WriteString(`</A:address-data>`)
		}
	default:
		props.WriteString(`<D:resourcetype/>`)
	}
	return props.String()
}

func davSyncTokenFromBody(body []byte) string {
	re := regexp.MustCompile(`(?is)<[^>]*sync-token[^>]*>([^<]+)</[^>]*sync-token>`)
	match := re.FindSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}
