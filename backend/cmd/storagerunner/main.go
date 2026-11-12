package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/control"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	operationID := flag.String("operation", "", "storage operation UUID")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *operationID == "" {
		logger.Error("missing -operation")
		os.Exit(2)
	}
	cfg, err := control.LoadConfig()
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	server, err := control.New(control.Options{Config: cfg, DB: pool, Logger: logger, Version: "storage-runner"})
	if err != nil {
		logger.Error("control initialization failed", "err", err)
		pool.Close()
		os.Exit(1)
	}
	defer server.Close()
	if err := server.RunStorageOperation(ctx, *operationID); err != nil {
		logger.Error("storage operation failed", "operation", *operationID, "err", err)
		os.Exit(1)
	}
	logger.Info("storage operation finished", "operation", *operationID)
}
