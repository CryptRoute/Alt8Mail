package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/officeagent"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	port := 8082
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("OFFICE_AGENT_HTTP_PORT"))); err == nil && value > 0 && value < 65536 {
		port = value
	}
	server, err := officeagent.New(officeagent.Config{
		Listen:         os.Getenv("OFFICE_AGENT_LISTEN"),
		TokenFile:      os.Getenv("OFFICE_AGENT_TOKEN_FILE"),
		ComposeFile:    os.Getenv("OFFICE_AGENT_COMPOSE_FILE"),
		SecretFile:     os.Getenv("OFFICE_AGENT_SECRET_FILE"),
		StateFile:      os.Getenv("OFFICE_AGENT_STATE_FILE"),
		DefaultImage:   os.Getenv("OFFICE_AGENT_IMAGE"),
		ContainerName:  os.Getenv("OFFICE_AGENT_CONTAINER"),
		HTTPPort:       port,
		NginxSite:      os.Getenv("OFFICE_AGENT_NGINX_SITE"),
		NginxEnabled:   os.Getenv("OFFICE_AGENT_NGINX_ENABLED"),
		CertbotBin:     os.Getenv("OFFICE_AGENT_CERTBOT"),
		CertbotDir:     os.Getenv("OFFICE_AGENT_CERTBOT_DIR"),
		CertbotWebroot: os.Getenv("OFFICE_AGENT_CERTBOT_WEBROOT"),
		DockerBin:      os.Getenv("OFFICE_AGENT_DOCKER"),
	})
	if err != nil {
		logger.Error("office agent configuration", "err", err)
		os.Exit(1)
	}
	listen := strings.TrimSpace(os.Getenv("OFFICE_AGENT_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8098"
	}
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		logger.Info("Alt8Mail office agent listening", "addr", listen, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("office agent", "err", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}
