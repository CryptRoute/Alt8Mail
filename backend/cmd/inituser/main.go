package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/config"
	"github.com/meridian-mail/meridian/backend/internal/stateinit"
	"github.com/meridian-mail/meridian/backend/internal/store"
)

func main() {
	email := flag.String("email", "", "active mailbox user to initialize")
	flag.Parse()
	if *email == "" {
		log.Fatal("-email is required")
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, 2, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatalf("open services: %v", err)
	}
	defer st.Close()

	state, err := stateinit.Build(ctx, st.DB, *email)
	if err != nil {
		log.Fatalf("build state: %v", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		log.Fatalf("encode state: %v", err)
	}
	var userID, tenantID string
	if err := st.DB.QueryRow(ctx, `SELECT id::text, tenant_id::text FROM users WHERE email = $1`, *email).Scan(&userID, &tenantID); err != nil {
		log.Fatalf("find user: %v", err)
	}
	if _, err := st.DB.Exec(ctx, `
		INSERT INTO app_states (user_id, tenant_id, state, updated_at)
		VALUES ($1, $2, $3::jsonb, now())
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()
	`, userID, tenantID, raw); err != nil {
		log.Fatalf("write state: %v", err)
	}
	log.Printf("initialized real mailbox state for %s (%s) with %d directory contacts", *email, userID, len(state["people"].([]any)))
}
