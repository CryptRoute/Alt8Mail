package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/addonrelay"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server, err := addonrelay.New(addonrelay.Config{
		PublicURL:         os.Getenv("ADDON_RELAY_PUBLIC_URL"),
		Secret:            os.Getenv("ADDON_RELAY_SECRET"),
		SlackClientID:     strings.TrimSpace(os.Getenv("ADDON_RELAY_SLACK_CLIENT_ID")),
		SlackClientSecret: strings.TrimSpace(os.Getenv("ADDON_RELAY_SLACK_CLIENT_SECRET")),
		Logger:            logger,
	})
	if err != nil {
		logger.Error("add-on relay configuration", "err", err)
		os.Exit(1)
	}
	listen := strings.TrimSpace(os.Getenv("ADDON_RELAY_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8099"
	}
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		logger.Info("Alt8Mail add-on relay listening", "addr", listen, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("add-on relay", "err", err)
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
