package control

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed web/*
var webFiles embed.FS

type Server struct {
	cfg          Config
	db           *pgxpool.Pool
	logger       *slog.Logger
	version      string
	secrets      *secretBox
	started      time.Time
	dummyHash    string
	hub          *eventHub
	probe        *systemProbe
	importMu     sync.Mutex
	importCancel map[string]context.CancelFunc
	importWake   chan struct{}
	updateMu     sync.Mutex
	updateJobs   map[string]*updateJob
	termMu       sync.Mutex
	termSessions map[string]*terminalSession
}

type Options struct {
	Config  Config
	DB      *pgxpool.Pool
	Logger  *slog.Logger
	Version string
}

func New(opts Options) (*Server, error) {
	secrets, err := newSecretBox(opts.Config.SecretKey)
	if err != nil {
		return nil, err
	}
	dummyHash, err := auth.HashPassword("not-a-real-control-password-7f3d")
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: opts.Config, db: opts.DB, logger: opts.Logger, version: opts.Version,
		secrets: secrets, started: time.Now(), dummyHash: dummyHash,
		hub: newEventHub(), probe: newSystemProbe(opts.Config, opts.Logger),
		importCancel: map[string]context.CancelFunc{}, importWake: make(chan struct{}, 1),
		updateJobs: map[string]*updateJob{}, termSessions: map[string]*terminalSession{},
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(webFiles, "web")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /", s.serveIndex)

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /healthz/stacks", s.requireAny([]string{"services.read", "settings.write"}, s.handleStacks))
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /events", s.require("dashboard.read", s.handleSSE))
	mux.HandleFunc("GET /control/fragments/services", s.require("dashboard.read", s.handleServiceFragment))

	mux.HandleFunc("GET /control/api/session", s.handleSession)
	mux.HandleFunc("POST /control/api/login", s.handleLogin)
	mux.HandleFunc("POST /control/api/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /control/api/logout", s.require("", s.handleLogout))

	mux.HandleFunc("GET /control/api/me", s.require("", s.handleMe))
	mux.HandleFunc("GET /control/api/me/sessions", s.require("", s.handleSessionList))
	mux.HandleFunc("DELETE /control/api/me/sessions/{id}", s.require("", s.handleSessionRevoke))
	mux.HandleFunc("POST /control/api/me/sessions/revoke-others", s.require("", s.handleSessionRevokeOthers))
	mux.HandleFunc("POST /control/api/me/totp/begin", s.require("", s.handleTOTPBegin))
	mux.HandleFunc("POST /control/api/me/totp/finish", s.require("", s.handleTOTPFinish))
	mux.HandleFunc("POST /control/api/me/totp/disable", s.require("", s.handleTOTPDisable))
	mux.HandleFunc("POST /control/api/me/password", s.require("", s.handleChangeOwnPassword))

	s.registerDashboardRoutes(mux)
	s.registerDomainRoutes(mux)
	s.registerMailboxRoutes(mux)
	s.registerAliasRoutes(mux)
	s.registerTLSRoutes(mux)
	s.registerDeliveryRoutes(mux)
	s.registerServiceRoutes(mux)
	s.registerLogRoutes(mux)
	s.registerImportRoutes(mux)
	s.registerBackupRoutes(mux)
	s.registerCronRoutes(mux)
	s.registerServerRoutes(mux)
	s.registerDatabaseRoutes(mux)
	s.registerAccessRoutes(mux)
	s.registerAPIRoutes(mux)
	s.registerNotificationRoutes(mux)
	s.registerSecurityReportRoutes(mux)
	s.registerUpdateRoutes(mux)
	s.registerTerminalRoutes(mux)

	return s.securityHeaders(s.logRequests(mux))
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	raw, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STATIC", "Control UI is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		connectSrc := "'self'"
		if host := strings.TrimSpace(r.Host); host != "" && !strings.ContainsAny(host, " ;,'\"\\") {
			connectSrc += " ws://" + host + " wss://" + host
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src "+connectSrc+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if r.URL.Path != "/metrics" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			s.logger.Debug("control request", "method", r.Method, "path", r.URL.Path, "ip", s.requestIP(r).String())
		}
		next.ServeHTTP(w, r)
		if time.Since(start) > 3*time.Second && !strings.HasPrefix(r.URL.Path, "/events") {
			s.logger.Warn("slow control request", "path", r.URL.Path, "duration", time.Since(start))
		}
	})
}

func (s *Server) Close() {
	if s.hub != nil {
		s.hub.Close()
	}
	if s.db != nil {
		s.db.Close()
	}
}

// DB exposes the panel pool to the small CLI bootstrap commands.
func (s *Server) DB() *pgxpool.Pool { return s.db }

// RunCronFromCLI is invoked from /etc/cron.d. It runs exactly the same stored
// job definition as the panel's run-now action.
func (s *Server) RunCronFromCLI(jobID string) error {
	return s.runCronFromCLI(jobID)
}

// DispatchEventNow is used by the CLI notification wrapper (ssl renewal cron).
func (s *Server) DispatchEventNow(event string, data map[string]any) {
	s.dispatchEvent(event, data)
}
