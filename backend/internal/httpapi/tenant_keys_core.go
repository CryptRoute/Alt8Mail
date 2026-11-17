package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var errTenantKeyLocked = errors.New("the tenant encryption key is locked")

func (s *Server) tenantKeyActive(ctx context.Context, tenantID string) bool {
	if hook := s.tenantKeyHooks.Active; hook != nil {
		return hook(ctx, tenantID)
	}
	return false
}

// sealTenantBlob delegates to the private BYOK module. Without an active
// customer key the blob stays plaintext.
func (s *Server) sealTenantBlob(ctx context.Context, tenantID string, data []byte) ([]byte, int, error) {
	if hook := s.tenantKeyHooks.SealBlob; hook != nil {
		sealed, version, handled, err := hook(ctx, tenantID, data)
		if handled || err != nil {
			return sealed, version, err
		}
	}
	return data, 0, nil
}

// openTenantBlob decrypts a BYOK blob through the private module. A BYOK blob
// without an installed module is an error, never returned as ciphertext.
func (s *Server) openTenantBlob(data []byte) ([]byte, error) {
	if hook := s.tenantKeyHooks.OpenBlob; hook != nil {
		plaintext, handled, err := hook(context.Background(), data)
		if handled || err != nil {
			return plaintext, err
		}
	}
	if strings.HasPrefix(string(data), tenantBlobPrefix+"\n") {
		return nil, fmt.Errorf("the tenant encryption module is unavailable")
	}
	return data, nil
}

func (s *Server) sealTenantSecret(ctx context.Context, tenantID, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if hook := s.tenantKeyHooks.SealSecret; hook != nil {
		sealed, handled, err := hook(ctx, tenantID, plaintext)
		if handled || err != nil {
			return sealed, err
		}
	}
	return s.sealWorkspaceToken(plaintext)
}

func (s *Server) openTenantSecret(ctx context.Context, tenantID, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if hook := s.tenantKeyHooks.OpenSecret; hook != nil {
		plaintext, handled, err := hook(ctx, tenantID, value)
		if handled || err != nil {
			return plaintext, err
		}
	}
	if strings.HasPrefix(value, "byok:v1:") {
		return "", errTenantKeyLocked
	}
	return s.openWorkspaceToken(value)
}
