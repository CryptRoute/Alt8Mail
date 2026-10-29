package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
)

func main() {
	email := flag.String("email", "avery.chen@a8m.test", "mailbox to seed")
	path := flag.String("state", "seed/state.json", "feature-state JSON document")
	replace := flag.Bool("replace", false, "replace an existing state document")
	flag.Parse()

	raw, err := os.ReadFile(*path)
	if err != nil {
		log.Fatalf("read state: %v", err)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		log.Fatalf("validate state JSON: %v", err)
	}
	var compact bytes.Buffer
	encoder := json.NewEncoder(&compact)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		log.Fatalf("normalize state JSON: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, 2, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatalf("open services: %v", err)
	}
	defer st.Close()

	var userID, tenantID string
	if err := st.DB.QueryRow(ctx, `SELECT id::text, tenant_id::text FROM users WHERE email = $1`, *email).Scan(&userID, &tenantID); err != nil {
		log.Fatalf("find user %s: %v", *email, err)
	}
	var exists bool
	_ = st.DB.QueryRow(ctx, `SELECT true FROM app_states WHERE user_id = $1`, userID).Scan(&exists)
	if exists && !*replace {
		log.Printf("state already exists for %s; use -replace to overwrite", *email)
		return
	}
	if _, err := st.DB.Exec(ctx, `
		INSERT INTO app_states (user_id, tenant_id, state, updated_at)
		VALUES ($1, $2, $3::jsonb, now())
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, updated_at = now()
	`, userID, tenantID, compact.Bytes()); err != nil {
		log.Fatalf("seed state: %v", err)
	}
	fmt.Printf("seeded app state for %s (%s)\n", *email, userID)
}
