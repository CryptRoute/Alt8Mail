package control

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var coreServiceUnits = []string{
	"postfix@-.service",
	"dovecot.service",
	"rspamd.service",
	"clamav-daemon.service",
	"clamav-freshclam.service",
	"nginx.service",
	"postgresql@15-main.service",
	"redis-server.service",
	"fail2ban.service",
	"cron.service",
	"chrony.service",
	"alt8mail-control.service",
	"a8m-server.service",
	"a8m-admin.service",
}

type commandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

func runCommand(ctx context.Context, timeout time.Duration, name string, args ...string) (commandResult, error) {
	return runCommandEnv(ctx, timeout, nil, name, args...)
}

// runCommandEnv is the env-aware form used for short-lived credentials such as
// REDISCLI_AUTH. The environment is never logged or returned to the browser.
func runCommandEnv(ctx context.Context, timeout time.Duration, environment []string, name string, args ...string) (commandResult, error) {
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	if environment != nil {
		cmd.Env = environment
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &limitedWriter{builder: &stdout, limit: 4 << 20}
	cmd.Stderr = &limitedWriter{builder: &stderr, limit: 1 << 20}
	err := cmd.Run()
	result := commandResult{Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(started)}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("%s timed out after %s", name, timeout)
	}
	return result, err
}

type limitedWriter struct {
	builder *strings.Builder
	limit   int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.builder.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.builder.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = w.builder.Write(p)
	return len(p), nil
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

type hostMetrics struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
	MemoryPercent    float64 `json:"memoryPercent"`
	DiskUsedBytes    uint64  `json:"diskUsedBytes"`
	DiskTotalBytes   uint64  `json:"diskTotalBytes"`
	DiskPercent      float64 `json:"diskPercent"`
	UptimeSeconds    int64   `json:"uptimeSeconds"`
	Load1            float64 `json:"load1"`
	Load5            float64 `json:"load5"`
	Load15           float64 `json:"load15"`
	PackageUpdates   int     `json:"packageUpdates"`
	UpdatedAt        string  `json:"updatedAt"`
}

type hostPoint struct {
	At   string  `json:"at"`
	CPU  float64 `json:"cpu"`
	Mem  float64 `json:"memory"`
	Disk float64 `json:"disk"`
}

type serviceStatus struct {
	Unit         string `json:"unit"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	LoadState    string `json:"loadState"`
	ActiveState  string `json:"activeState"`
	SubState     string `json:"subState"`
	EnabledState string `json:"enabledState"`
	MainPID      int    `json:"mainPid"`
	Active       bool   `json:"active"`
}

type systemProbe struct {
	cfg    Config
	logger *slog.Logger

	mu          sync.Mutex
	prevCPU     cpuTimes
	cpuPercent  float64
	host        hostMetrics
	hostAt      time.Time
	packageAt   time.Time
	packages    int
	services    []serviceStatus
	servicesAt  time.Time
	history     []hostPoint
	queueCount  int
	queueAt     time.Time
	queueDetail string
}

func newSystemProbe(cfg Config, logger *slog.Logger) *systemProbe {
	return &systemProbe{cfg: cfg, logger: logger}
}

func readCPUTimes() cpuTimes {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		var times []uint64
		for _, field := range fields[1:] {
			value, _ := strconv.ParseUint(field, 10, 64)
			times = append(times, value)
		}
		if len(times) < 5 {
			return cpuTimes{}
		}
		var total uint64
		for _, value := range times {
			total += value
		}
		idle := times[3] + times[4]
		return cpuTimes{total: total, idle: idle}
	}
	return cpuTimes{}
}

func readMemory() (used, total uint64, percent float64) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(parts[1], 10, 64)
		values[strings.TrimSuffix(parts[0], ":")] = value * 1024
	}
	total = values["MemTotal"]
	available := values["MemAvailable"]
	if available == 0 {
		available = values["MemFree"]
	}
	if total >= available {
		used = total - available
	}
	if total > 0 {
		percent = float64(used) / float64(total) * 100
	}
	return used, total, percent
}

func readUptime() int64 {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return int64(value)
}

func readLoad() (float64, float64, float64) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	one, _ := strconv.ParseFloat(fields[0], 64)
	five, _ := strconv.ParseFloat(fields[1], 64)
	fifteen, _ := strconv.ParseFloat(fields[2], 64)
	return one, five, fifteen
}

func (p *systemProbe) refreshPackageUpdates(ctx context.Context, force bool) {
	p.mu.Lock()
	fresh := !force && time.Since(p.packageAt) < 6*time.Hour
	p.mu.Unlock()
	if fresh {
		return
	}
	result, _ := runCommand(ctx, 45*time.Second, "apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade")
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "Inst ") {
			count++
		}
	}
	p.mu.Lock()
	p.packages = count
	p.packageAt = time.Now()
	p.mu.Unlock()
}

func (p *systemProbe) hostMetrics(ctx context.Context, force bool) hostMetrics {
	p.mu.Lock()
	if !force && time.Since(p.hostAt) < 3*time.Second {
		cached := p.host
		p.mu.Unlock()
		return cached
	}
	p.mu.Unlock()

	current := readCPUTimes()
	p.mu.Lock()
	prev := p.prevCPU
	if prev.total > 0 && current.total > prev.total {
		totalDelta := current.total - prev.total
		idleDelta := current.idle - prev.idle
		if totalDelta > 0 {
			p.cpuPercent = float64(totalDelta-idleDelta) / float64(totalDelta) * 100
		}
	}
	p.prevCPU = current
	cpuPercent := p.cpuPercent
	p.mu.Unlock()

	usedMem, totalMem, memPercent := readMemory()
	usedDisk, totalDisk, diskPercent := filesystemUsage("/")
	load1, load5, load15 := readLoad()
	p.mu.Lock()
	p.host = hostMetrics{
		CPUPercent: cpuPercent, MemoryUsedBytes: usedMem, MemoryTotalBytes: totalMem,
		MemoryPercent: memPercent, DiskUsedBytes: usedDisk, DiskTotalBytes: totalDisk,
		DiskPercent: diskPercent, UptimeSeconds: readUptime(), Load1: load1, Load5: load5,
		Load15: load15, PackageUpdates: p.packages, UpdatedAt: nowRFC3339(),
	}
	p.hostAt = time.Now()
	p.history = append(p.history, hostPoint{
		At: nowRFC3339(), CPU: p.host.CPUPercent, Mem: p.host.MemoryPercent, Disk: p.host.DiskPercent,
	})
	if len(p.history) > 180 {
		p.history = p.history[len(p.history)-180:]
	}
	out := p.host
	p.mu.Unlock()
	return out
}

func (p *systemProbe) hostHistory() []hostPoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hostPoint{}, p.history...)
}

func parseSystemctlShow(output string) serviceStatus {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	pid, _ := strconv.Atoi(values["MainPID"])
	active := values["ActiveState"] == "active"
	return serviceStatus{
		Unit: values["Id"], Description: values["Description"],
		LoadState: values["LoadState"], ActiveState: values["ActiveState"],
		SubState: values["SubState"], EnabledState: values["UnitFileState"],
		MainPID: pid, Active: active,
	}
}

func (p *systemProbe) serviceStatuses(ctx context.Context, force bool) []serviceStatus {
	p.mu.Lock()
	if !force && time.Since(p.servicesAt) < 5*time.Second && len(p.services) > 0 {
		cached := append([]serviceStatus{}, p.services...)
		p.mu.Unlock()
		return cached
	}
	p.mu.Unlock()
	units := make([]string, len(coreServiceUnits))
	copy(units, coreServiceUnits)
	result := make([]serviceStatus, 0, len(units))
	for _, unit := range units {
		show, _ := runCommand(ctx, 5*time.Second, "systemctl", "show", unit, "--no-page",
			"--property=Id,Description,LoadState,ActiveState,SubState,UnitFileState,MainPID")
		status := parseSystemctlShow(show.Stdout)
		if status.Unit == "" {
			status.Unit = unit
		}
		status.Name = strings.TrimSuffix(strings.TrimSuffix(status.Unit, ".service"), "@-")
		if status.Description == "" {
			status.Description = status.Name
		}
		result = append(result, status)
	}
	p.mu.Lock()
	p.services = result
	p.servicesAt = time.Now()
	p.mu.Unlock()
	return result
}

func (p *systemProbe) queue(ctx context.Context, force bool) (int, string) {
	p.mu.Lock()
	if !force && time.Since(p.queueAt) < 10*time.Second {
		count, detail := p.queueCount, p.queueDetail
		p.mu.Unlock()
		return count, detail
	}
	p.mu.Unlock()
	result, err := runCommand(ctx, 8*time.Second, "postqueue", "-p")
	count := 0
	if err == nil {
		for _, line := range strings.Split(result.Stdout, "\n") {
			if strings.HasPrefix(line, "-Queue ID-") || strings.HasPrefix(line, "Mail queue is empty") || strings.TrimSpace(line) == "" {
				continue
			}
			if strings.HasPrefix(line, "-") && strings.Contains(line, "Size") {
				continue
			}
			if strings.HasPrefix(line, "(") || strings.HasPrefix(line, "\t") {
				continue
			}
			if len(strings.Fields(line)) >= 7 {
				count++
			}
		}
	}
	p.mu.Lock()
	p.queueCount, p.queueDetail, p.queueAt = count, result.Stdout, time.Now()
	p.mu.Unlock()
	return count, result.Stdout
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": "alt8mail-control", "version": s.version,
		"uptimeSeconds": int64(time.Since(s.started).Seconds()), "time": nowRFC3339(),
	})
}

func (s *Server) handleStacks(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf[:n])
}

func (s *Server) handleServiceFragment(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	services := s.probe.serviceStatuses(r.Context(), queryBool(r, "refresh", false))
	var b strings.Builder
	b.WriteString(`<div class="service-grid">`)
	for _, service := range services {
		state := "down"
		label := "Down"
		if service.Active {
			state, label = "up", "Up"
		}
		b.WriteString(`<article class="service-card is-` + state + `">`)
		b.WriteString(`<div class="service-card-head"><span class="status-dot ` + state + `"></span><strong>` + html.EscapeString(service.Name) + `</strong><span class="status-pill ` + state + `">` + label + `</span></div>`)
		b.WriteString(`<p>` + html.EscapeString(service.Description) + `</p>`)
		b.WriteString(`<dl><div><dt>Unit</dt><dd>` + html.EscapeString(service.Unit) + `</dd></div><div><dt>State</dt><dd>` + html.EscapeString(service.SubState) + `</dd></div><div><dt>Boot</dt><dd>` + html.EscapeString(service.EnabledState) + `</dd></div><div><dt>PID</dt><dd>` + strconv.Itoa(service.MainPID) + `</dd></div></dl>`)
		b.WriteString(`</article>`)
	}
	b.WriteString(`</div>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	services := s.probe.serviceStatuses(ctx, false)
	host := s.probe.hostMetrics(ctx, false)
	queue, _ := s.probe.queue(ctx, false)
	var domains, users, aliases, suppressions, outboundIPs, apiKeys, webhooks, blocklist24, events5m int64
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains`).Scan(&domains)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.users WHERE deleted_at IS NULL`).Scan(&users)
	_ = s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.user_aliases) + (SELECT count(*) FROM control.mail_aliases WHERE active)`).Scan(&aliases)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.suppressions WHERE expires_at IS NULL OR expires_at > now()`).Scan(&suppressions)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.outbound_ips WHERE active`).Scan(&outboundIPs)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.api_keys WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`).Scan(&apiKeys)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.webhooks WHERE enabled`).Scan(&webhooks)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.blocklist_results WHERE listed AND checked_at > now() - interval '24 hours'`).Scan(&blocklist24)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM control.mail_events WHERE event_at > now() - interval '5 minutes'`).Scan(&events5m)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "# HELP admin_info System Control build information\n# TYPE admin_info gauge\n")
	fmt.Fprintf(w, "admin_info{version=%q,env=%q,service=\"alt8mail-control\"} 1\n", s.version, s.cfg.Env)
	fmt.Fprintf(w, "# HELP admin_uptime_seconds Seconds since the control service started\n# TYPE admin_uptime_seconds gauge\nadmin_uptime_seconds %.0f\n", time.Since(s.started).Seconds())
	fmt.Fprintf(w, "# HELP service_up systemd service active state\n# TYPE service_up gauge\n")
	for _, service := range services {
		value := 0
		if service.Active {
			value = 1
		}
		fmt.Fprintf(w, "service_up{service=%q,unit=%q} %d\n", service.Name, service.Unit, value)
	}
	fmt.Fprintf(w, "domains_total %d\nusers_total %d\naliases_total %d\nmail_events_5m %d\nqueue_messages %d\nsuppressions_total %d\noutbound_ips_active %d\napi_keys_active %d\nwebhooks_active %d\nblocklist_listed_24h %d\n",
		domains, users, aliases, events5m, queue, suppressions, outboundIPs, apiKeys, webhooks, blocklist24)
	fmt.Fprintf(w, "host_cpu_percent %.2f\nhost_memory_percent %.2f\nhost_disk_percent %.2f\nhost_package_updates %d\n",
		host.CPUPercent, host.MemoryPercent, host.DiskPercent, host.PackageUpdates)
	rows, err := s.db.Query(ctx, `SELECT name, not_after FROM control.ssl_certificates`)
	if err == nil {
		defer rows.Close()
		fmt.Fprintf(w, "# HELP ssl_days_left Days until certificate expiry\n# TYPE ssl_days_left gauge\n")
		for rows.Next() {
			var name string
			var notAfter time.Time
			if rows.Scan(&name, &notAfter) == nil {
				fmt.Fprintf(w, "ssl_days_left{certificate=%q} %.0f\n", name, time.Until(notAfter).Hours()/24)
			}
		}
	}
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// redisPing prefers an unauthenticated PING, then retries once using the
// existing deployment secret file. The secret never enters a process argument,
// a log line or an API response.
func (s *Server) redisPing(ctx context.Context) (bool, string) {
	result, err := runCommand(ctx, 5*time.Second, "redis-cli", "ping")
	if err == nil && strings.TrimSpace(result.Stdout) == "PONG" {
		return true, "Redis replies to PING"
	}
	raw, fileErr := os.ReadFile(s.cfg.RedisPasswordFile)
	password := strings.TrimSpace(string(raw))
	if fileErr != nil || password == "" {
		return false, "Redis is reachable but requires authentication; set CONTROL_REDIS_PASSWORD_FILE to the deployment secret"
	}
	environment := append(os.Environ(), "REDISCLI_AUTH="+password)
	result, err = runCommandEnv(ctx, 5*time.Second, environment, "redis-cli", "ping")
	if err == nil && strings.TrimSpace(result.Stdout) == "PONG" {
		return true, "Redis authenticated PING succeeded"
	}
	return false, "Redis PING failed with the configured credentials"
}
