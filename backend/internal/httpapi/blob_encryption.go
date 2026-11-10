package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

const tenantBlobPrefix = "A8MENC1"

// sealTenantBlob encrypts a file blob with the active tenant data key. A zero
// version means the tenant has not enabled BYOK and the blob is plaintext.
// When BYOK is enabled but locked, the write fails rather than falling back.
func (s *Server) sealTenantBlob(ctx context.Context, tenantID string, data []byte) ([]byte, int, error) {
	version, _, _, err := s.activeTenantKey(ctx, tenantID)
	if err != nil {
		return data, 0, nil
	}
	key, ok := s.unlockedTenantKey(tenantID, version)
	if !ok {
		return nil, 0, errTenantKeyLocked
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, err
	}
	sealed := gcm.Seal(nonce, nonce, data, []byte(fmt.Sprintf("a8m-blob-v1:%s:%d", tenantID, version)))
	envelope := tenantBlobPrefix + "\n" + tenantID + "\n" + fmt.Sprint(version) + "\n" + base64.RawStdEncoding.EncodeToString(sealed)
	return []byte(envelope), version, nil
}

// openTenantBlob decrypts a blob produced by sealTenantBlob. Plaintext blobs
// (including legacy files) pass through unchanged.
func (s *Server) openCabinetBlob(ctx context.Context, fileID string, data []byte) ([]byte, error) {
	var clientEncrypted bool
	_ = s.store.DB.QueryRow(ctx, `SELECT client_encrypted FROM public.files WHERE id=$1`, fileID).Scan(&clientEncrypted)
	if clientEncrypted {
		return data, nil
	}
	return s.openTenantBlob(data)
}

func (s *Server) openTenantBlob(data []byte) ([]byte, error) {
	if !strings.HasPrefix(string(data), tenantBlobPrefix+"\n") {
		return data, nil
	}
	parts := strings.SplitN(string(data), "\n", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid encrypted blob")
	}
	tenantID := parts[1]
	version := 0
	if _, err := fmt.Sscanf(parts[2], "%d", &version); err != nil {
		return nil, fmt.Errorf("invalid encrypted blob version")
	}
	key, ok := s.unlockedTenantKey(tenantID, version)
	if !ok {
		return nil, errTenantKeyLocked
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("invalid encrypted blob")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, []byte(fmt.Sprintf("a8m-blob-v1:%s:%d", tenantID, version)))
}
