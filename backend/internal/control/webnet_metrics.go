package control

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var _ = strconv.IntSize

var webNetLogLine = regexp.MustCompile(`^(\S+) \S+ \S+ \[([^\]]+)\] "([^"]*)" (\d{3}) (\d+) "([^"]*)" "([^"]*)" (\S+) (\S+)\s*$`)

type webNetLogStats struct {
	Requests     int64            `json:"requests"`
	ServerErrors int64            `json:"serverErrors"`
	ClientErrors int64            `json:"clientErrors"`
	Bytes        int64            `json:"bytes"`
	Clients      map[string]bool  `json:"-"`
	Paths        map[string]int64 `json:"-"`
	Durations    []float64        `json:"-"`
	LastAt       time.Time        `json:"-"`
}

func newWebNetLogStats() *webNetLogStats {
	return &webNetLogStats{Clients: map[string]bool{}, Paths: map[string]int64{}}
}

func (s *webNetLogStats) add(remote, request, status, bytesText, seconds string, at time.Time) {
	code, _ := strconv.Atoi(status)
	size, _ := strconv.ParseInt(bytesText, 10, 64)
	s.Requests++
	s.Bytes += size
	if code >= 500 {
		s.ServerErrors++
	} else if code >= 400 {
		s.ClientErrors++
	}
	s.Clients[remote] = true
	parts := strings.Fields(request)
	if len(parts) >= 2 {
		path := redactWebNetPath(strings.SplitN(parts[1], "?", 2)[0])
		if path != "" {
			s.Paths[path]++
		}
	}
	if seconds != "" && seconds != "-" {
		if value, err := strconv.ParseFloat(seconds, 64); err == nil && value >= 0 {
			s.Durations = append(s.Durations, value*1000)
		}
	}
	if at.After(s.LastAt) {
		s.LastAt = at
	}
}

func (s *webNetLogStats) summary() map[string]any {
	unique := len(s.Clients)
	average := 0.0
	p95 := 0.0
	if len(s.Durations) > 0 {
		total := 0.0
		for _, value := range s.Durations {
			total += value
		}
		average = total / float64(len(s.Durations))
		sorted := append([]float64{}, s.Durations...)
		sort.Float64s(sorted)
		index := int(float64(len(sorted)-1) * 0.95)
		p95 = sorted[index]
	}
	type pathCount struct {
		Path  string `json:"path"`
		Count int64  `json:"count"`
	}
	paths := make([]pathCount, 0, len(s.Paths))
	for path, count := range s.Paths {
		paths = append(paths, pathCount{Path: path, Count: count})
	}
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Count != paths[j].Count {
			return paths[i].Count > paths[j].Count
		}
		return paths[i].Path < paths[j].Path
	})
	if len(paths) > 6 {
		paths = paths[:6]
	}
	topPaths := make([]map[string]any, 0, len(paths))
	for _, item := range paths {
		topPaths = append(topPaths, map[string]any{"path": item.Path, "count": item.Count})
	}
	lastAt := ""
	if !s.LastAt.IsZero() {
		lastAt = s.LastAt.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"requests": s.Requests, "serverErrors": s.ServerErrors, "clientErrors": s.ClientErrors,
		"bytes": s.Bytes, "uniqueClients": unique, "avgMs": average, "p95Ms": p95,
		"topPaths": topPaths, "lastAt": lastAt,
	}
}

type webNetLogSample struct {
	Remote  string
	Request string
	Status  string
	Bytes   string
	Seconds string
	At      time.Time
}

func redactWebNetPath(path string) string {
	path = truncateText(path, 300)
	parts := strings.Split(path, "/")
	for index, part := range parts {
		if len(part) < 16 {
			continue
		}
		opaque := true
		for _, character := range part {
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '=' {
				opaque = false
				break
			}
		}
		if opaque {
			parts[index] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

func readWebNetLog(path string, maxBytes int64) ([]webNetLogSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	samples := make([]webNetLogSample, 0, len(lines))
	for _, line := range lines {
		match := webNetLogLine.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 10 {
			continue
		}
		at, parseErr := time.Parse("02/Jan/2006:15:04:05 -0700", match[2])
		if parseErr != nil {
			continue
		}
		samples = append(samples, webNetLogSample{
			Remote: match[1], Request: match[3], Status: match[4], Bytes: match[5],
			Seconds: match[8], At: at,
		})
	}
	return samples, nil
}

func (s *Server) webNetServiceMetrics(path string, now time.Time) map[string]any {
	samples, err := readWebNetLog(path, 4<<20)
	if err != nil {
		return map[string]any{
			"available": false, "requests": 0, "serverErrors": 0, "clientErrors": 0,
			"bytes": 0, "uniqueClients": 0, "avgMs": 0, "p95Ms": 0, "topPaths": []any{}, "lastAt": "",
		}
	}
	hour := newWebNetLogStats()
	quarter := newWebNetLogStats()
	for _, sample := range samples {
		if sample.At.Before(now.Add(-time.Hour)) {
			continue
		}
		hour.add(sample.Remote, sample.Request, sample.Status, sample.Bytes, sample.Seconds, sample.At)
		if !sample.At.Before(now.Add(-15 * time.Minute)) {
			quarter.add(sample.Remote, sample.Request, sample.Status, sample.Bytes, sample.Seconds, sample.At)
		}
	}
	payload := hour.summary()
	payload["available"] = true
	payload["last15Minutes"] = quarter.summary()
	return payload
}

func (s *Server) handleWebNetMetrics(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	now := time.Now()
	type service struct {
		key, label string
		file       string
	}
	services := []map[string]any{}
	for _, item := range []service{
		{"webmail", "Webmail", "a8m-webmail.access.log"},
		{"admin", "Administration", "a8m-admin.access.log"},
		{"blocked", "Blocked & unmapped hosts", "a8m-blocked.access.log"},
	} {
		metrics := s.webNetServiceMetrics(filepath.Join(s.cfg.NginxLogDir, item.file), now)
		metrics["key"] = item.key
		metrics["label"] = item.label
		services = append(services, metrics)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": now.UTC().Format(time.RFC3339), "windowMinutes": 60,
		"logDirectory": s.cfg.NginxLogDir, "services": services,
	})
}
