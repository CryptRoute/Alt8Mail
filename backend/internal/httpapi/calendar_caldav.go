package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const calDAVMaxBytes = 8 * 1024 * 1024

func calDAVFreeBusyReport(start, end time.Time) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav">
  <C:time-range start="` + start.UTC().Format("20060102T150405Z") + `" end="` + end.UTC().Format("20060102T150405Z") + `"/>
</C:free-busy-query>`
}

func (s *Server) calDAVClient() *http.Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}
	return &http.Client{
		Timeout:   25 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("CalDAV redirects must stay on https")
			}
			return ensurePublicHost(req.URL.Hostname())
		},
	}
}

func (s *Server) fetchCalDAVFreeBusy(ctx context.Context, rawURL, username, password string, start, end time.Time) ([]map[string]any, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("CalDAV federation requires an https:// calendar collection URL")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("CalDAV credentials must be sent as separate fields")
	}
	if err := ensurePublicHost(parsed.Hostname()); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "REPORT", parsed.String(), strings.NewReader(calDAVFreeBusyReport(start, end)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	request.Header.Set("Accept", "text/calendar, application/xml;q=0.8")
	request.Header.Set("Depth", "0")
	request.Header.Set("User-Agent", "Alt8Mail-CalDAV/1.0")
	if username != "" || password != "" {
		request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	}
	response, err := s.calDAVClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not reach the CalDAV server")
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, calDAVMaxBytes+1))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("the CalDAV server returned HTTP %d", response.StatusCode)
	}
	if len(raw) > calDAVMaxBytes {
		return nil, fmt.Errorf("the CalDAV free/busy response is too large")
	}
	return parseCalDAVFreeBusy(raw, start, end)
}

func parseCalDAVFreeBusy(raw []byte, start, end time.Time) ([]map[string]any, error) {
	lines := unfoldICS(raw)
	busy := []map[string]any{}
	seen := map[string]bool{}
	for _, line := range lines {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(upper, "FREEBUSY") {
			continue
		}
		name, params, value := parseICSProperty(strings.TrimSpace(line))
		if name != "FREEBUSY" {
			continue
		}
		if kind := strings.ToUpper(strings.TrimSpace(params["FBTYPE"])); kind == "FREE" {
			continue
		}
		for _, period := range strings.Split(value, ",") {
			fromRaw, toRaw, ok := strings.Cut(strings.TrimSpace(period), "/")
			if !ok {
				continue
			}
			from, _, err := parseICSTime(strings.TrimSpace(fromRaw), map[string]string{})
			if err != nil {
				continue
			}
			to, _, err := parseICSTime(strings.TrimSpace(toRaw), map[string]string{})
			if err != nil || !to.After(from) || !to.After(start) || !from.Before(end) {
				continue
			}
			if from.Before(start) {
				from = start
			}
			if to.After(end) {
				to = end
			}
			key := from.UTC().Format(time.RFC3339Nano) + "/" + to.UTC().Format(time.RFC3339Nano)
			if seen[key] {
				continue
			}
			seen[key] = true
			busy = append(busy, map[string]any{
				"start": from.UTC().Format(time.RFC3339), "end": to.UTC().Format(time.RFC3339),
			})
		}
	}
	if len(busy) == 0 {
		return []map[string]any{}, nil
	}
	return busy, nil
}

func (s *Server) calendarCalDAVFreeBusy(w http.ResponseWriter, r *http.Request) {
	s.requireSessionFromContext(r)
	var body struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Start    string `json:"start"`
		End      string `json:"end"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	start := time.Now().UTC()
	end := start.Add(24 * time.Hour)
	var err error
	if raw := strings.TrimSpace(body.Start); raw != "" {
		start, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "CALDAV", "start must be RFC3339")
			return
		}
	}
	if raw := strings.TrimSpace(body.End); raw != "" {
		end, err = time.Parse(time.RFC3339, raw)
		if err != nil || !end.After(start) || end.Sub(start) > 31*24*time.Hour {
			writeError(w, http.StatusBadRequest, "CALDAV", "end must be RFC3339, after start and within 31 days")
			return
		}
	}
	busy, err := s.fetchCalDAVFreeBusy(r.Context(), body.URL, body.Username, body.Password, start, end)
	if err != nil {
		writeError(w, http.StatusBadGateway, "CALDAV", err.Error())
		return
	}
	parsed, _ := url.Parse(strings.TrimSpace(body.URL))
	source := ""
	if parsed != nil {
		source = strings.ToLower(parsed.Hostname())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"busy": busy, "source": source,
		"start": start.UTC().Format(time.RFC3339), "end": end.UTC().Format(time.RFC3339),
	})
}

func (s *Server) registerCalDAVRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/calendar/free-busy/caldav", s.requireUser(s.calendarCalDAVFreeBusy))
}
