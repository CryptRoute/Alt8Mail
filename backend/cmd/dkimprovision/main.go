package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
)

var domainPartPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var selectorPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

type domainRow struct {
	id, domain, selector string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if os.Geteuid() != 0 {
		logger.Error("dkimprovision must run as root")
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		logger.Error("storage initialization failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	owner, err := dkimOwner()
	if err != nil {
		logger.Error("DKIM key owner lookup failed", "err", err)
		os.Exit(1)
	}
	if err := ensureDirectory(cfg.DKIMDir, owner.uid, owner.gid); err != nil {
		logger.Error("DKIM directory initialization failed", "dir", cfg.DKIMDir, "err", err)
		os.Exit(1)
	}

	rows, err := st.DB.Query(ctx, `
		SELECT id::text, lower(domain::text), COALESCE(NULLIF(dkim_selector, ''), 'a8m')
		FROM mail_domains ORDER BY domain
	`)
	if err != nil {
		logger.Error("domain query failed", "err", err)
		os.Exit(1)
	}
	domains := []domainRow{}
	for rows.Next() {
		var row domainRow
		if rows.Scan(&row.id, &row.domain, &row.selector) == nil {
			domains = append(domains, row)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		logger.Error("domain query failed", "err", err)
		os.Exit(1)
	}

	selectorMap := map[string]string{}
	created, updated := 0, 0
	for _, row := range domains {
		row.domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(row.domain)), ".")
		row.selector = strings.ToLower(strings.TrimSpace(row.selector))
		if row.selector == "" {
			row.selector = "a8m"
		}
		if !domainPartPattern.MatchString(row.domain) || strings.Contains(row.domain, "..") || len(row.domain) > 253 {
			_ = updateDomainKeyError(ctx, st, row.id, "domain is not safe for a DKIM key path")
			logger.Warn("DKIM domain rejected", "domain", row.domain)
			continue
		}
		if !selectorPattern.MatchString(row.selector) {
			_ = updateDomainKeyError(ctx, st, row.id, "selector is not safe for a DKIM key path")
			logger.Warn("DKIM selector rejected", "domain", row.domain, "selector", row.selector)
			continue
		}
		keyPath := filepath.Join(cfg.DKIMDir, row.domain+"."+row.selector+".key")
		if !strings.HasPrefix(keyPath, filepath.Clean(cfg.DKIMDir)+string(os.PathSeparator)) {
			_ = updateDomainKeyError(ctx, st, row.id, "DKIM key path escapes the key directory")
			continue
		}
		privateKey, wasCreated, err := loadOrCreateKey(keyPath, owner.uid, owner.gid)
		if err != nil {
			_ = updateDomainKeyError(ctx, st, row.id, err.Error())
			logger.Warn("DKIM key provisioning failed", "domain", row.domain, "selector", row.selector, "err", err)
			continue
		}
		publicKey := publicKeyBase64(&privateKey.PublicKey)
		if publicKey == "" {
			_ = updateDomainKeyError(ctx, st, row.id, "could not derive the RSA public key")
			logger.Warn("DKIM public key derivation failed", "domain", row.domain, "selector", row.selector)
			continue
		}
		record := "v=DKIM1; k=rsa; p=" + publicKey
		if _, err := st.DB.Exec(ctx, `
			UPDATE mail_domains
			SET dkim_public_key = $2, dkim_record = $3,
			    dkim_key_created_at = COALESCE(dkim_key_created_at, now()),
			    dkim_key_error = ''
			WHERE id = $1
		`, row.id, publicKey, record); err != nil {
			logger.Warn("DKIM database update failed", "domain", row.domain, "err", err)
			continue
		}
		selectorMap[row.domain] = row.selector
		if wasCreated {
			created++
		} else {
			updated++
		}
	}

	mapChanged, err := writeSelectorMap(cfg.DKIMSelectorMap, selectorMap)
	if err != nil {
		logger.Error("DKIM selector map write failed", "path", cfg.DKIMSelectorMap, "err", err)
		os.Exit(1)
	}
	if created > 0 || mapChanged {
		if err := reloadRspamd(); err != nil {
			logger.Warn("Rspamd reload failed", "err", err)
		}
	}
	logger.Info("DKIM provisioning pass complete", "domains", len(selectorMap), "created", created, "existing", updated, "map_changed", mapChanged)
}

type dkimFileOwner struct {
	uid, gid int
}

func dkimOwner() (dkimFileOwner, error) {
	for _, name := range []string{"_rspamd", "rspamd"} {
		entry, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := parseID(entry.Uid)
		if err != nil {
			return dkimFileOwner{}, err
		}
		gid, err := parseID(entry.Gid)
		if err != nil {
			return dkimFileOwner{}, err
		}
		return dkimFileOwner{uid: uid, gid: gid}, nil
	}
	return dkimFileOwner{}, fmt.Errorf("no _rspamd/rspamd system user found")
}

func parseID(value string) (int, error) {
	var id int
	if _, err := fmt.Sscanf(value, "%d", &id); err != nil {
		return 0, fmt.Errorf("invalid uid/gid %q", value)
	}
	return id, nil
}

func ensureDirectory(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("DKIM path is not a directory")
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func loadOrCreateKey(path string, uid, gid int) (*rsa.PrivateKey, bool, error) {
	if data, err := os.ReadFile(path); err == nil {
		key, parseErr := parsePrivateKey(data)
		if parseErr != nil {
			return nil, false, parseErr
		}
		return key, false, nil
	} else if !os.IsNotExist(err) {
		return nil, false, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, false, err
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	temp, err := os.CreateTemp(filepath.Dir(path), ".dkim-key-*")
	if err != nil {
		return nil, false, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return nil, false, err
	}
	if _, err := temp.Write(pem.EncodeToMemory(block)); err != nil {
		temp.Close()
		return nil, false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return nil, false, err
	}
	if err := temp.Close(); err != nil {
		return nil, false, err
	}
	if err := os.Chown(tempName, uid, gid); err != nil {
		return nil, false, err
	}
	if err := os.Rename(tempName, path); err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("DKIM private key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("DKIM private key is not RSA/PKCS8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("DKIM private key is not RSA")
	}
	return key, nil
}

func publicKeyBase64(key *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(der)
}

func updateDomainKeyError(ctx context.Context, st *store.Store, id, message string) error {
	_, err := st.DB.Exec(ctx, `UPDATE mail_domains SET dkim_key_error = $2 WHERE id = $1`, id, truncate(message, 500))
	return err
}

func writeSelectorMap(path string, selectors map[string]string) (bool, error) {
	lines := make([]string, 0, len(selectors))
	for domain, selector := range selectors {
		lines = append(lines, domain+" "+selector)
	}
	sort.Strings(lines)
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".selectors-*")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return false, err
	}
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tempName, path); err != nil {
		return false, err
	}
	return true, nil
}

func reloadRspamd() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", "reload", "rspamd")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, truncate(strings.TrimSpace(string(output)), 300))
	}
	return nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
