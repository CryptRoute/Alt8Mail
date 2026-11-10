package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var errTenantKeyLocked = errors.New("the tenant encryption key is locked")

type unlockedTenantKey struct {
	Version   int
	Key       []byte
	ExpiresAt time.Time
}

func wrapTenantDEK(kek, dek []byte, tenantID string, version int) (string, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, dek, []byte(fmt.Sprintf("a8m-byok-v1:%s:%d", tenantID, version)))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func unwrapTenantDEK(kek []byte, wrapped, tenantID string, version int) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("invalid wrapped tenant key")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, []byte(fmt.Sprintf("a8m-byok-v1:%s:%d", tenantID, version)))
}

func tenantKeyFingerprint(kek []byte) string {
	sum := sha256.Sum256(kek)
	return hex.EncodeToString(sum[:8])
}

func (s *Server) cacheTenantKey(tenantID string, version int, key []byte) {
	s.tenantKeyMu.Lock()
	defer s.tenantKeyMu.Unlock()
	if s.tenantKeys == nil {
		s.tenantKeys = map[string]map[int]unlockedTenantKey{}
	}
	if s.tenantKeys[tenantID] == nil {
		s.tenantKeys[tenantID] = map[int]unlockedTenantKey{}
	}
	s.tenantKeys[tenantID][version] = unlockedTenantKey{Version: version, Key: append([]byte{}, key...), ExpiresAt: time.Now().Add(30 * time.Minute)}
}

func (s *Server) unlockedTenantKey(tenantID string, version int) ([]byte, bool) {
	s.tenantKeyMu.Lock()
	defer s.tenantKeyMu.Unlock()
	versions, ok := s.tenantKeys[tenantID]
	if !ok {
		return nil, false
	}
	entry, ok := versions[version]
	if !ok || time.Now().After(entry.ExpiresAt) {
		delete(versions, version)
		if len(versions) == 0 {
			delete(s.tenantKeys, tenantID)
		}
		return nil, false
	}
	return append([]byte{}, entry.Key...), true
}

func (s *Server) activeTenantKey(ctx context.Context, tenantID string) (version int, wrapped, fingerprint string, err error) {
	err = s.store.DB.QueryRow(ctx, `
		SELECT version, wrapped_dek, key_fingerprint FROM public.tenant_encryption_keys
		WHERE tenant_id=$1 AND status='active' ORDER BY version DESC LIMIT 1
	`, tenantID).Scan(&version, &wrapped, &fingerprint)
	return version, wrapped, fingerprint, err
}

func (s *Server) sealTenantSecret(ctx context.Context, tenantID, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	version, _, _, err := s.activeTenantKey(ctx, tenantID)
	if err != nil {
		return s.sealWorkspaceToken(plaintext)
	}
	key, ok := s.unlockedTenantKey(tenantID, version)
	if !ok {
		return "", errTenantKeyLocked
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(fmt.Sprintf("a8m-secret-v1:%s:%d", tenantID, version)))
	return fmt.Sprintf("byok:v1:%d:%s", version, base64.RawStdEncoding.EncodeToString(sealed)), nil
}

func (s *Server) openTenantSecret(ctx context.Context, tenantID, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "byok:v1:") {
		return s.openWorkspaceToken(value)
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "byok:v1:"), ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid tenant secret")
	}
	var version int
	if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
		return "", fmt.Errorf("invalid tenant secret version")
	}
	key, ok := s.unlockedTenantKey(tenantID, version)
	if !ok {
		return "", errTenantKeyLocked
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid tenant secret")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(fmt.Sprintf("a8m-secret-v1:%s:%d", tenantID, version)))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) adminGetEncryption(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, version, status, key_fingerprint, created_at, rotated_at, retired_at
		FROM public.tenant_encryption_keys
		WHERE tenant_id=$1 ORDER BY version DESC
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not load tenant keys")
		return
	}
	defer rows.Close()
	versions := []map[string]any{}
	activeVersion := 0
	for rows.Next() {
		var id, status, fingerprint string
		var version int
		var created time.Time
		var rotated, retired *time.Time
		if rows.Scan(&id, &version, &status, &fingerprint, &created, &rotated, &retired) == nil {
			item := map[string]any{
				"id": id, "version": version, "status": status, "fingerprint": fingerprint,
				"createdAt": created.UTC().Format(time.RFC3339),
			}
			if rotated != nil {
				item["rotatedAt"] = rotated.UTC().Format(time.RFC3339)
			}
			if retired != nil {
				item["retiredAt"] = retired.UTC().Format(time.RFC3339)
			}
			if status == "active" {
				activeVersion = version
			}
			versions = append(versions, item)
		}
	}
	_, unlocked := s.unlockedTenantKey(sess.ActiveTenantID, activeVersion)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": activeVersion > 0, "activeVersion": activeVersion,
		"unlocked": unlocked, "versions": versions,
		"warning": "If the customer key is lost, Alt8Mail cannot recover encrypted tenant secrets. Store it in your key ceremony system; there is no vendor-key fallback.",
	})
}

func decodeCustomerKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw))
	}
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("customer key must be 32 bytes encoded as base64")
	}
	return key, nil
}

func (s *Server) adminCreateEncryptionKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		CustomerKey string `json:"customerKey"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	kek, err := decodeCustomerKey(body.CustomerKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION", err.Error())
		return
	}
	if version, _, _, err := s.activeTenantKey(r.Context(), sess.ActiveTenantID); err == nil && version > 0 {
		writeError(w, http.StatusConflict, "ENCRYPTION", "A tenant key already exists; rotate it instead")
		return
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not generate the tenant key")
		return
	}
	wrapped, err := wrapTenantDEK(kek, dek, sess.ActiveTenantID, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not wrap the tenant key")
		return
	}
	keyID := newID("tkey")
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO public.tenant_encryption_keys (id, tenant_id, version, status, wrapped_dek, key_fingerprint, created_by)
		VALUES ($1,$2,1,'active',$3,$4,$5)
	`, keyID, sess.ActiveTenantID, wrapped, tenantKeyFingerprint(kek), sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not save the tenant key")
		return
	}
	s.cacheTenantKey(sess.ActiveTenantID, 1, dek)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.encryption_key_created", "Tenant encryption",
		"Enabled customer-managed encryption", map[string]any{"tenantId": sess.ActiveTenantID, "version": 1})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "version": 1, "enabled": true, "unlocked": true})
}

func (s *Server) adminUnlockEncryption(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		CustomerKey string `json:"customerKey"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	kek, err := decodeCustomerKey(body.CustomerKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION", err.Error())
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT version, wrapped_dek FROM public.tenant_encryption_keys
		WHERE tenant_id=$1 AND status IN ('active','retired') ORDER BY version DESC
	`, sess.ActiveTenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not load tenant keys")
		return
	}
	type candidate struct {
		version int
		wrapped string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if rows.Scan(&item.version, &item.wrapped) == nil {
			candidates = append(candidates, item)
		}
	}
	rows.Close()
	unlocked := 0
	activeVersion, _, _, _ := s.activeTenantKey(r.Context(), sess.ActiveTenantID)
	for _, item := range candidates {
		dek, err := unwrapTenantDEK(kek, item.wrapped, sess.ActiveTenantID, item.version)
		if err != nil {
			continue
		}
		s.cacheTenantKey(sess.ActiveTenantID, item.version, dek)
		unlocked++
		if item.version == activeVersion {
			s.cacheTenantKey(sess.ActiveTenantID, item.version, dek)
		}
	}
	if unlocked == 0 {
		writeError(w, http.StatusBadRequest, "ENCRYPTION", "The customer key does not unlock this organization")
		return
	}
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.encryption_unlocked", "Tenant encryption",
		"Unlocked customer-managed keys", map[string]any{"tenantId": sess.ActiveTenantID, "keys": unlocked})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unlocked": true, "keys": unlocked, "activeVersion": activeVersion})
}

func (s *Server) adminLockEncryption(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	s.tenantKeyMu.Lock()
	delete(s.tenantKeys, sess.ActiveTenantID)
	s.tenantKeyMu.Unlock()
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.encryption_locked", "Tenant encryption",
		"Locked customer-managed keys", map[string]any{"tenantId": sess.ActiveTenantID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unlocked": false})
}

func (s *Server) adminRotateEncryptionKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireAdminSession(r)
	var body struct {
		CustomerKey string `json:"customerKey"`
	}
	if !s.decodeBody(w, r, &body, 16*1024) {
		return
	}
	kek, err := decodeCustomerKey(body.CustomerKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION", err.Error())
		return
	}
	currentVersion, wrapped, _, err := s.activeTenantKey(r.Context(), sess.ActiveTenantID)
	if err != nil || currentVersion == 0 {
		writeError(w, http.StatusConflict, "ENCRYPTION", "Enable customer-managed encryption before rotating")
		return
	}
	if _, err := unwrapTenantDEK(kek, wrapped, sess.ActiveTenantID, currentVersion); err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION", "The customer key does not match the active key")
		return
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not generate the new tenant key")
		return
	}
	newVersion := currentVersion + 1
	wrappedNew, err := wrapTenantDEK(kek, dek, sess.ActiveTenantID, newVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not wrap the new tenant key")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not rotate the tenant key")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE public.tenant_encryption_keys SET status='retired', retired_at=now()
		WHERE tenant_id=$1 AND status='active'
	`, sess.ActiveTenantID); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not retire the previous key")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO public.tenant_encryption_keys (id, tenant_id, version, status, wrapped_dek, key_fingerprint, created_by, rotated_at)
		VALUES ($1,$2,$3,'active',$4,$5,$6,now())
	`, newID("tkey"), sess.ActiveTenantID, newVersion, wrappedNew, tenantKeyFingerprint(kek), sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not save the new tenant key")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION", "Could not rotate the tenant key")
		return
	}
	s.cacheTenantKey(sess.ActiveTenantID, newVersion, dek)
	s.recordAdminAudit(r.Context(), sess, sess.User, "admin.encryption_rotated", "Tenant encryption",
		"Rotated the customer-managed data key", map[string]any{"tenantId": sess.ActiveTenantID, "version": newVersion})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": newVersion, "active": true})
}
