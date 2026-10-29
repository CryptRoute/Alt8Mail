package control

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/auth"
)

var safeDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var safeLocalPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._+=-]{0,127}$`)
var safeSelectorPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

func normalizeDomain(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func normalizeLocal(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validDomain(value string) bool {
	return len(value) <= 253 && safeDomainPattern.MatchString(value) && !strings.Contains(value, "..")
}

func validLocalPart(value string) bool {
	return safeLocalPattern.MatchString(value)
}

func validSelector(value string) bool {
	return safeSelectorPattern.MatchString(value)
}

func randomPassword(length int) (string, error) {
	if length < 16 {
		length = 16
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%^&*"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf), nil
}

func hashMailPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("mailbox password is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "doveadm", "pw", "-s", "SHA512-CRYPT")
	command.Stdin = strings.NewReader(password + "\n" + password + "\n")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("doveadm pw failed: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{SHA512-CRYPT}") {
			return line, nil
		}
	}
	return "", fmt.Errorf("doveadm returned no SHA512-CRYPT hash")
}

func (s *Server) maildirPath(domain, localPart string) string {
	return filepath.Join(s.cfg.VmailRoot, domain, localPart, "Maildir")
}

func vmailOwner() (int, int, error) {
	entry, err := user.Lookup("vmail")
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(entry.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(entry.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func (s *Server) ensureMaildir(domain, localPart string) (string, error) {
	domain = normalizeDomain(domain)
	localPart = normalizeLocal(localPart)
	if !validDomain(domain) || !validLocalPart(localPart) {
		return "", fmt.Errorf("unsafe mailbox path component")
	}
	uid, gid, err := vmailOwner()
	if err != nil {
		return "", fmt.Errorf("vmail account: %w", err)
	}
	base := filepath.Clean(s.cfg.VmailRoot)
	userDir := filepath.Join(base, domain, localPart)
	maildir := filepath.Join(userDir, "Maildir")
	if !strings.HasPrefix(maildir, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("mailbox path escapes the mail root")
	}
	directories := []string{
		filepath.Join(base, domain), userDir, maildir,
		filepath.Join(maildir, "cur"), filepath.Join(maildir, "new"), filepath.Join(maildir, "tmp"),
	}
	for _, folder := range []string{"Sent", "Drafts", "Archive", "Junk", "Trash"} {
		folderDir := filepath.Join(maildir, "."+folder)
		directories = append(directories, folderDir,
			filepath.Join(folderDir, "cur"), filepath.Join(folderDir, "new"), filepath.Join(folderDir, "tmp"))
	}
	for _, directory := range directories {
		if info, statErr := os.Lstat(directory); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("mailbox path contains a symlink")
		}
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return "", err
		}
		if err := os.Chown(directory, uid, gid); err != nil {
			return "", err
		}
		if err := os.Chmod(directory, 0o750); err != nil {
			return "", err
		}
	}
	return maildir, nil
}

func rspamdOwner() (int, int, error) {
	for _, name := range []string{"_rspamd", "rspamd"} {
		entry, err := user.Lookup(name)
		if err == nil {
			uid, _ := strconv.Atoi(entry.Uid)
			gid, _ := strconv.Atoi(entry.Gid)
			return uid, gid, nil
		}
	}
	return 0, 0, fmt.Errorf("rspamd user not found")
}

func publicKeyBase64(key *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(der)
}

func (s *Server) generateDKIMKey(ctx context.Context, domainID, domain, selector string, force bool) (string, error) {
	domain = normalizeDomain(domain)
	selector = normalizeSelector(selector)
	if !validDomain(domain) || !validSelector(selector) {
		return "", fail(400, "DKIM", "Domain or selector is not safe for a key path")
	}
	dir, err := filepath.Abs(s.cfg.DKIMDir)
	if err != nil {
		return "", err
	}
	keyPath := filepath.Join(dir, domain+"."+selector+".key")
	if !strings.HasPrefix(keyPath, dir+string(os.PathSeparator)) {
		return "", fail(400, "DKIM", "DKIM path escapes the key directory")
	}
	if force {
		_ = os.Remove(keyPath)
	}
	var privateKey *rsa.PrivateKey
	if !force {
		if raw, readErr := os.ReadFile(keyPath); readErr == nil {
			if block, _ := pem.Decode(raw); block != nil {
				if parsed, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
					privateKey = parsed
				}
			}
		}
	}
	if privateKey == nil {
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", err
		}
		block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
			return "", err
		}
		uid, gid, ownerErr := rspamdOwner()
		if ownerErr == nil {
			_ = os.Chown(keyPath, uid, gid)
		}
	}
	publicKey := publicKeyBase64(&privateKey.PublicKey)
	if publicKey == "" {
		return "", fmt.Errorf("could not derive DKIM public key")
	}
	record := "v=DKIM1; k=rsa; p=" + publicKey
	_, err = s.db.Exec(ctx, `
		UPDATE public.mail_domains
		SET dkim_selector=$3, dkim_public_key=$4, dkim_record=$5,
		    dkim_key_created_at=COALESCE(dkim_key_created_at, now()), dkim_key_error=''
		WHERE id=$1 AND domain=$2
	`, domainID, domain, selector, publicKey, record)
	if err != nil {
		return "", err
	}
	if file, createErr := os.Create(keyPath + ".txt"); createErr == nil {
		_, _ = file.WriteString(record + "\n")
		file.Close()
		_ = os.Chmod(keyPath+".txt", 0o644)
		uid, gid, ownerErr := rspamdOwner()
		if ownerErr == nil {
			_ = os.Chown(keyPath+".txt", uid, gid)
		}
	}
	if _, err := s.writeSelectorMap(ctx); err != nil {
		return record, err
	}
	_, _ = runCommand(ctx, 20*time.Second, "systemctl", "reload", "rspamd")
	return record, nil
}

func normalizeSelector(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "meridian"
	}
	return value
}

func (s *Server) writeSelectorMap(ctx context.Context) (bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT lower(domain::text), COALESCE(NULLIF(dkim_selector,''),'meridian')
		FROM public.mail_domains ORDER BY domain
	`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var domain, selector string
		if rows.Scan(&domain, &selector) == nil && validDomain(domain) && validSelector(selector) {
			lines = append(lines, domain+" "+selector)
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(s.cfg.DKIMDir, "selectors.map")
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	if uid, gid, err := rspamdOwner(); err == nil {
		_ = os.Chown(path, uid, gid)
	}
	return true, nil
}

func (s *Server) createPostmaster(ctx context.Context, tenantID, domain string) error {
	domain = normalizeDomain(domain)
	email := "postmaster@" + domain
	var existing int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE email=$1`, email).Scan(&existing)
	if existing > 0 {
		return nil
	}
	random, err := randomPassword(28)
	if err != nil {
		return err
	}
	webHash, err := auth.HashPassword(random)
	if err != nil {
		return err
	}
	mailHash, err := hashMailPassword(random)
	if err != nil {
		return err
	}
	maildir, err := s.ensureMaildir(domain, "postmaster")
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `
		INSERT INTO public.users (tenant_id, email, name, initials, role, status,
			password_hash, mail_password_hash, password_changed_at)
		VALUES ($1,$2,'Postmaster','PM','member','active',$3,$4,now())
		RETURNING id::text
	`, tenantID, email, webHash, mailHash).Scan(&userID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active, provision_status, provisioned_at)
		VALUES ($1,$2,$3,$4,'postmaster',$5,true,'ready',now())
	`, tenantID, userID, email, domain, maildir)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.user_aliases (tenant_id, user_id, email, label, kind, can_send, active)
		VALUES ($1,$2,$3,'Primary','primary',true,true)
		ON CONFLICT (email) DO NOTHING
	`, tenantID, userID, email)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
