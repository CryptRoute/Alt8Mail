// Command soakload drives a staged, read-only webmail soak with distinct
// authenticated users. Credentials arrive only via the SOAK_PASSWORD
// environment variable; the output records status/latency only.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type sample struct {
	at       time.Time
	email    string
	endpoint string
	status   int
	latency  time.Duration
	err      string
	phase    string
}

type config struct {
	base     string
	prefix   string
	users    int
	password string
	ramp     time.Duration
	hold     time.Duration
	interval time.Duration
	timeout  time.Duration
	out      string
}

var (
	activeVUs   atomic.Int64
	loginsOK    atomic.Int64
	loginsFail  atomic.Int64
	totalReqs   atomic.Int64
	totalErrors atomic.Int64
)

func endpointFor(index int) string {
	switch index % 10 {
	case 0, 1, 2, 3:
		return "/api/messages?folder=inbox"
	case 4:
		return "/api/folders"
	case 5:
		return "/api/quota"
	case 6:
		return "/api/search?q=welcome"
	case 7:
		return "/api/tasks"
	case 8:
		return "/api/calendar/events"
	default:
		return "/api/updates"
	}
}

// /api/updates is not a browser endpoint. Replace it with a lightweight real
// endpoint so every request exercises the application.
func realEndpoint(index int) string {
	value := endpointFor(index)
	if value == "/api/updates" {
		return "/api/preferences"
	}
	return value
}

func userEmail(prefix string, index int) string {
	return fmt.Sprintf("%s-%04d@meridian.test", prefix, index)
}

func login(ctx context.Context, client *http.Client, base, email, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/session", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login HTTP %d", resp.StatusCode)
	}
	var result struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.CSRF, nil
}

func vuser(ctx context.Context, cfg config, index int, start time.Time, samples chan<- sample, wg *sync.WaitGroup) {
	defer wg.Done()
	delay := time.Until(start)
	if delay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	email := userEmail(cfg.prefix, index)
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.timeout,
	}
	client := &http.Client{Transport: transport, Jar: jar, Timeout: cfg.timeout}
	loginCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	csrf, err := login(loginCtx, client, cfg.base, email, cfg.password)
	cancel()
	if err != nil {
		loginsFail.Add(1)
		samples <- sample{at: time.Now(), email: email, endpoint: "login", latency: 0, err: err.Error(), phase: "login"}
		transport.CloseIdleConnections()
		return
	}
	loginsOK.Add(1)
	activeVUs.Add(1)
	defer activeVUs.Add(-1)
	defer transport.CloseIdleConnections()
	_ = csrf

	// A real browser opens the workspace once after login.
	started := time.Now()
	bootstrapReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.base+"/api/bootstrap", nil)
	bootstrapReq.Header.Set("Accept", "application/json")
	if resp, err := client.Do(bootstrapReq); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		totalReqs.Add(1)
		samples <- sample{at: started, email: email, endpoint: "bootstrap", status: resp.StatusCode,
			latency: time.Since(started), phase: "ramp"}
		if resp.StatusCode/100 != 2 {
			totalErrors.Add(1)
		}
	} else {
		totalErrors.Add(1)
		samples <- sample{at: started, email: email, endpoint: "bootstrap", latency: time.Since(started), err: err.Error(), phase: "ramp"}
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(index)))
	iteration := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		path := realEndpoint(iteration + index)
		start := time.Now()
		reqCtx, reqCancel := context.WithTimeout(ctx, cfg.timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.base+path, nil)
		if err != nil {
			reqCancel()
			totalErrors.Add(1)
			samples <- sample{at: start, email: email, endpoint: path, latency: time.Since(start), err: err.Error(), phase: phaseAt(cfg, time.Since(start))}
		} else {
			req.Header.Set("Accept", "application/json")
			resp, requestErr := client.Do(req)
			if requestErr != nil {
				totalErrors.Add(1)
				samples <- sample{at: start, email: email, endpoint: path, latency: time.Since(start), err: requestErr.Error(), phase: phaseAt(cfg, time.Since(start))}
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
				resp.Body.Close()
				totalReqs.Add(1)
				if resp.StatusCode/100 != 2 {
					totalErrors.Add(1)
				}
				samples <- sample{at: start, email: email, endpoint: path, status: resp.StatusCode,
					latency: time.Since(start), phase: phaseAt(cfg, time.Since(start))}
			}
			reqCancel()
		}
		iteration++
		jitter := time.Duration(rng.Int63n(int64(cfg.interval / 5)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.interval + jitter):
		}
	}
}

var runStarted time.Time

func phaseAt(cfg config, _ time.Duration) string {
	if time.Since(runStarted) < cfg.ramp {
		return "ramp"
	}
	return "hold"
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.base, "base", "https://130-94-46-22.sslip.io", "webmail base URL")
	flag.StringVar(&cfg.prefix, "prefix", "soak-20261029", "soak account prefix")
	flag.IntVar(&cfg.users, "users", 1000, "distinct users")
	flag.DurationVar(&cfg.ramp, "ramp", 10*time.Minute, "ramp duration")
	flag.DurationVar(&cfg.hold, "hold", 20*time.Minute, "hold duration at full concurrency")
	flag.DurationVar(&cfg.interval, "interval", 15*time.Second, "per-user request interval")
	flag.DurationVar(&cfg.timeout, "timeout", 20*time.Second, "per-request timeout")
	flag.StringVar(&cfg.out, "out", "artifacts/soak", "artifact directory")
	flag.Parse()
	cfg.password = os.Getenv("SOAK_PASSWORD")
	if cfg.password == "" {
		fmt.Fprintln(os.Stderr, "SOAK_PASSWORD is required")
		os.Exit(2)
	}
	if cfg.users < 1 || cfg.users > 5000 {
		fmt.Fprintln(os.Stderr, "users must be between 1 and 5000")
		os.Exit(2)
	}
	if cfg.interval < time.Second {
		fmt.Fprintln(os.Stderr, "interval must be at least 1s")
		os.Exit(2)
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runStarted = time.Now()
	end := runStarted.Add(cfg.ramp + cfg.hold)
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	samples := make(chan sample, 200000)
	var wg sync.WaitGroup
	for i := 1; i <= cfg.users; i++ {
		offset := time.Duration(0)
		if cfg.users > 1 {
			offset = time.Duration(float64(cfg.ramp) * float64(i-1) / float64(cfg.users-1))
		}
		wg.Add(1)
		go vuser(ctx, cfg, i, runStarted.Add(offset), samples, &wg)
	}
	writerDone := make(chan struct{})
	var all []sample
	go func() {
		defer close(writerDone)
		for item := range samples {
			all = append(all, item)
		}
	}()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	stopProgress := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				req := totalReqs.Load()
				errs := totalErrors.Load()
				rate := float64(0)
				if elapsed := time.Since(runStarted).Seconds(); elapsed > 0 {
					rate = float64(req) / elapsed
				}
				fmt.Printf("progress elapsed=%s active=%d logins_ok=%d logins_fail=%d requests=%d errors=%d rate=%.1f/s\n",
					time.Since(runStarted).Round(time.Second), activeVUs.Load(), loginsOK.Load(), loginsFail.Load(), req, errs, rate)
			}
		}
	}()
	wg.Wait()
	close(samples)
	<-writerDone
	close(stopProgress)
	summary := summarise(all, cfg, time.Since(runStarted))
	raw, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(cfg.out+"/soak-summary.json", raw, 0o644)
	fmt.Println(string(raw))
}

func summarise(items []sample, cfg config, elapsed time.Duration) map[string]any {
	latencies := make([]float64, 0, len(items))
	byEndpoint := map[string]int64{}
	byStatus := map[string]int64{}
	errors := int64(0)
	success := int64(0)
	loginOK := loginsOK.Load()
	loginFail := loginsFail.Load()
	for _, item := range items {
		if item.endpoint == "login" {
			continue
		}
		latencies = append(latencies, float64(item.latency.Microseconds())/1000.0)
		byEndpoint[item.endpoint]++
		if item.err != "" {
			errors++
			byStatus["transport_error"]++
			continue
		}
		byStatus[fmt.Sprintf("%d", item.status)]++
		if item.status/100 == 2 {
			success++
		} else {
			errors++
		}
	}
	sort.Float64s(latencies)
	percentile := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		index := int(float64(len(latencies)-1) * p)
		return latencies[index]
	}
	return map[string]any{
		"base": cfg.base, "users": cfg.users, "rampSeconds": cfg.ramp.Seconds(),
		"holdSeconds": cfg.hold.Seconds(), "intervalSeconds": cfg.interval.Seconds(),
		"elapsedSeconds": round(elapsed.Seconds()), "loginOK": loginOK, "loginFail": loginFail,
		"requests": len(latencies), "success": success, "errors": errors,
		"errorRate":  ratio(errors, int64(len(latencies))),
		"latencyMs":  map[string]float64{"p50": percentile(0.50), "p95": percentile(0.95), "p99": percentile(0.99), "max": percentile(1)},
		"byEndpoint": byEndpoint, "byStatus": byStatus,
	}
}

func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func round(value float64) float64 {
	return float64(int64(value*10)) / 10
}

var _ = strings.TrimSpace
