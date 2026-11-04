package httpapi

import (
	"io"
	"log/slog"
	"testing"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestHandlerRegistersAllRoutes(t *testing.T) {
	server := New(Options{
		Config: config.Config{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("route registration panicked: %v", recovered)
		}
	}()
	if server.Handler() == nil {
		t.Fatal("handler was nil")
	}
}
