package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
	"github.com/meridian-mail/meridian/backend/internal/config"
	"github.com/meridian-mail/meridian/backend/internal/store"
)

func main() {
	email := flag.String("email", "", "user email")
	password := flag.String("password", "", "new web password (omit to read from stdin)")
	grantSystemAdmin := flag.Bool("grant-system-admin", false, "grant platform-wide admin access")
	revokeSystemAdmin := flag.Bool("revoke-system-admin", false, "revoke platform-wide admin access")
	listAdmins := flag.Bool("list-admins", false, "list users with admin access")
	flag.Parse()
	if *listAdmins {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "configuration:", err)
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		if err != nil {
			fmt.Fprintln(os.Stderr, "storage:", err)
			os.Exit(1)
		}
		defer st.Close()
		rows, err := st.DB.Query(ctx, `
			SELECT u.email::text, u.role, u.is_system_admin,
			       COALESCE((SELECT string_agg(r.name, ', ') FROM admin_role_assignments a
			         JOIN admin_roles r ON r.id = a.role_id WHERE a.user_id = u.id), '')
			FROM users u WHERE u.deleted_at IS NULL
			  AND (u.role IN ('owner','admin') OR u.is_system_admin
			       OR EXISTS (SELECT 1 FROM admin_role_assignments a WHERE a.user_id = u.id))
			ORDER BY u.email
		`)
		if err != nil {
			fmt.Fprintln(os.Stderr, "list admins:", err)
			os.Exit(1)
		}
		defer rows.Close()
		for rows.Next() {
			var mail, role, roles string
			var system bool
			if err := rows.Scan(&mail, &role, &system, &roles); err == nil {
				fmt.Printf("%s\trole=%s\tsystem=%t\tcustom=%s\n", mail, role, system, roles)
			}
		}
		return
	}
	if *email == "" {
		fmt.Fprintln(os.Stderr, "usage: meridian-admin -email user@example.test [-password ...] | -grant-system-admin | -revoke-system-admin | -list-admins")
		os.Exit(2)
	}
	if *grantSystemAdmin && *revokeSystemAdmin {
		fmt.Fprintln(os.Stderr, "choose either -grant-system-admin or -revoke-system-admin")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		fmt.Fprintln(os.Stderr, "storage:", err)
		os.Exit(1)
	}
	defer st.Close()
	if *grantSystemAdmin || *revokeSystemAdmin {
		value := *grantSystemAdmin
		tag, err := st.DB.Exec(ctx, `
			UPDATE users SET is_system_admin = $2, updated_at = now()
			WHERE email = $1 AND status = 'active' AND deleted_at IS NULL
		`, strings.ToLower(strings.TrimSpace(*email)), value)
		if err != nil {
			fmt.Fprintln(os.Stderr, "update system admin:", err)
			os.Exit(1)
		}
		if tag.RowsAffected() != 1 {
			fmt.Fprintln(os.Stderr, "user not found or inactive")
			os.Exit(1)
		}
		if value {
			fmt.Printf("System administrator granted to %s\n", *email)
		} else {
			fmt.Printf("System administrator revoked from %s\n", *email)
		}
		return
	}
	value := *password
	if value == "" {
		reader := bufio.NewReader(os.Stdin)
		fmt.Fprint(os.Stderr, "New web password: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not read password:", err)
			os.Exit(1)
		}
		value = strings.TrimSpace(line)
	}
	hash, err := auth.HashPassword(value)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tag, err := st.DB.Exec(ctx, `
		UPDATE users SET password_hash = $2, password_changed_at = now(), updated_at = now()
		WHERE email = $1 AND status = 'active'
	`, strings.ToLower(strings.TrimSpace(*email)), hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update password:", err)
		os.Exit(1)
	}
	if tag.RowsAffected() != 1 {
		fmt.Fprintln(os.Stderr, "user not found or inactive")
		os.Exit(1)
	}
	fmt.Printf("Web password updated for %s\n", *email)
}
