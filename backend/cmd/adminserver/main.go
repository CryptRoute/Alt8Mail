package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/httpapi"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("storage initialization failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	api := httpapi.New(httpapi.Options{Config: cfg, Store: st, Logger: logger, Version: version})
	server := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           api.AdminHandler(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		logger.Info("admin backend listening", "addr", cfg.AdminListen, "env", cfg.Env, "version", version)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("admin http server failed", "err", err)
			os.Exit(1)
		}
	}()
	<-shutdown
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := server.Shutdown(stopCtx); err != nil {
		logger.Warn("graceful shutdown failed", "err", err)
	}
}
