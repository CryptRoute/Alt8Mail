package httpapi

import (
	"context"
)

const tenantBlobPrefix = "A8MENC1"

// openCabinetBlob decrypts a file blob. Client-side encrypted files are stored
// as opaque ciphertext and pass through unchanged.
func (s *Server) openCabinetBlob(ctx context.Context, fileID string, data []byte) ([]byte, error) {
	var clientEncrypted bool
	_ = s.store.DB.QueryRow(ctx, `SELECT client_encrypted FROM public.files WHERE id=$1`, fileID).Scan(&clientEncrypted)
	if clientEncrypted {
		return data, nil
	}
	return s.openTenantBlob(data)
}
