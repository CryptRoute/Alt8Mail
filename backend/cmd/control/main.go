package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/control"
)

var version = "dev"

func main() {
	command := "serve"
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		command = os.Args[1]
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}
	switch command {
	case "serve":
		serve()
	case "admin":
		adminCommand()
	case "cron":
		cronCommand()
	case "doctor":
		doctorCommand()
	case "notify":
		notifyCommand()
	case "version", "-version", "--version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "usage: alt8mail-control [serve|admin|cron run <id>|doctor|notify <event> [json]]")
		os.Exit(2)
	}
}

func loadServer() (*control.Server, context.CancelFunc, error) {
	cfg, err := control.LoadConfig()
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	pool, err := control.OpenDatabase(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	server, err := control.New(control.Options{Config: cfg, DB: pool, Logger: logger, Version: version})
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return server, func() { server.Close() }, nil
}

func serve() {
	cfg, err := control.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	pool, err := control.OpenDatabase(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		logger.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	server, err := control.New(control.Options{Config: cfg, DB: pool, Logger: logger, Version: version})
	if err != nil {
		logger.Error("control initialization failed", "err", err)
		os.Exit(1)
	}
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server.Start(runCtx)

	// The interactive SSH terminal uses a WebSocket. Go's HTTP/2 server does
	// not implement http.Hijacker, so keep the panel on HTTP/1.1; this also
	// makes long-lived SSE and terminal connections deterministic.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		TLSConfig:         tlsConfig,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      0,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	go func() {
		<-runCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("system control listening", "addr", cfg.Listen, "version", version, "env", cfg.Env)
	if err := httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
		logger.Error("control listener failed", "err", err)
		os.Exit(1)
	}
}

func adminCommand() {
	flags := flag.NewFlagSet("admin", flag.ExitOnError)
	email := flags.String("email", "", "panel account email")
	name := flags.String("name", "", "display name")
	role := flags.String("role", "superadmin", "superadmin, admin, operator or readonly")
	passwordStdin := flags.Bool("password-stdin", false, "read password from stdin")
	update := flags.Bool("update", false, "update an existing account")
	list := flags.Bool("list", false, "list panel accounts")
	flags.Parse(os.Args[1:])
	server, closeFn, err := loadServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeFn()
	ctx := context.Background()
	if *list {
		rows, err := server.DB().Query(ctx, `SELECT email::text,role,status,created_at FROM control.admin_users ORDER BY email`)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer rows.Close()
		for rows.Next() {
			var mail, accountRole, status string
			var created time.Time
			if rows.Scan(&mail, &accountRole, &status, &created) == nil {
				fmt.Printf("%s\t%s\t%s\t%s\n", mail, accountRole, status, created.UTC().Format(time.RFC3339))
			}
		}
		return
	}
	if *email == "" {
		fmt.Fprintln(os.Stderr, "-email is required")
		os.Exit(2)
	}
	*email = strings.ToLower(strings.TrimSpace(*email))
	if *role != "superadmin" && *role != "admin" && *role != "operator" && *role != "readonly" {
		fmt.Fprintln(os.Stderr, "invalid role")
		os.Exit(2)
	}
	password := ""
	if *passwordStdin {
		raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		password = strings.TrimSpace(string(raw))
	}
	if len(password) < 12 {
		fmt.Fprintln(os.Stderr, "a password of at least 12 characters is required on stdin")
		os.Exit(2)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *name == "" {
		*name = "System Administrator"
	}
	if *update {
		_, err = server.DB().Exec(ctx, `
			UPDATE control.admin_users SET email=$1,name=$2,password_hash=$3,role=$4,status='active',
			  failed_attempts=0,failed_window_start=NULL,locked_until=NULL,updated_at=now()
			WHERE email=$1
		`, *email, *name, hash, *role)
	} else {
		_, err = server.DB().Exec(ctx, `
			INSERT INTO control.admin_users (email,name,password_hash,role,status)
			VALUES ($1,$2,$3,$4,'active')
		`, *email, *name, hash, *role)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not save panel account:", err)
		os.Exit(1)
	}
	fmt.Println("panel account saved:", *email)
}

func cronCommand() {
	flags := flag.NewFlagSet("cron", flag.ExitOnError)
	flags.Parse(os.Args[1:])
	args := flags.Args()
	if len(args) < 2 || args[0] != "run" {
		fmt.Fprintln(os.Stderr, "usage: alt8mail-control cron run <job-id>")
		os.Exit(2)
	}
	server, closeFn, err := loadServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeFn()
	if err := server.RunCronFromCLI(args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "cron job failed:", err)
		os.Exit(1)
	}
}

func doctorCommand() {
	server, closeFn, err := loadServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeFn()
	checks := server.RunDoctor(context.Background())
	raw, _ := json.MarshalIndent(map[string]any{"version": version, "checks": checks}, "", "  ")
	fmt.Println(string(raw))
	for _, check := range checks {
		if check.Status == "fail" {
			os.Exit(1)
		}
	}
}

func notifyCommand() {
	server, closeFn, err := loadServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeFn()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: alt8mail-control notify <event> [json-data]")
		os.Exit(2)
	}
	event := os.Args[1]
	data := map[string]any{}
	if len(os.Args) > 2 {
		_ = json.Unmarshal([]byte(os.Args[2]), &data)
	}
	server.DispatchEventNow(event, data)
	time.Sleep(3 * time.Second)
}
