package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
)

// fileClassification reads the DLP label attached to a stored file.
func fileClassification(file contentFile) string {
	var metadata map[string]any
	if len(file.Metadata) > 0 {
		_ = json.Unmarshal(file.Metadata, &metadata)
	}
	return canonicalDLPClassification(stateString(metadata, "dlpClassification"))
}

func (s *Server) currentFileHooks() enterprise.FileHooks {
	s.fileHooksMu.RLock()
	defer s.fileHooksMu.RUnlock()
	return s.fileHooks
}

// fileWatermark applies the paid file-governance watermark to downloads. The
// community build has no hook and returns an empty watermark.
func (s *Server) fileWatermark(r *http.Request, file contentFile, tenantID string) string {
	hooks := s.currentFileHooks()
	if hooks.Watermark == nil {
		return ""
	}
	actor := "shared link"
	if sess := s.requireSessionFromContext(r); sess != nil {
		actor = sess.User.Email
	}
	return hooks.Watermark(r.Context(), enterprise.FileRef{
		ID: file.ID, Name: file.Name, MIME: file.MIME, OwnerID: file.OwnerID,
		TenantID: file.TenantID, Classification: fileClassification(file),
	}, actor)
}

// fileOnLegalHold asks the paid file-governance module whether a file is
// protected. Community builds always answer false.
func (s *Server) fileOnLegalHold(ctx context.Context, tenantID, fileID, ownerID string) (bool, error) {
	hooks := s.currentFileHooks()
	if hooks.OnLegalHold == nil {
		return false, nil
	}
	return hooks.OnLegalHold(ctx, tenantID, fileID, ownerID)
}

// readAccessibleFileContent loads a file the user owns or can access, together
// with its bytes from the configured provider.
func (s *Server) readAccessibleFileContent(ctx context.Context, user userRecord, fileID string) (contentFile, []byte, error) {
	file, err := s.loadAccessibleFileContent(ctx, user, fileID)
	if err != nil {
		return file, nil, err
	}
	switch file.ProviderID {
	case "a8m":
		path := firstNonEmpty(file.StorageKey, file.AttachmentID)
		if path == "" || strings.ContainsAny(path, `/\`) || path == "." || path == ".." {
			return file, nil, fmt.Errorf("file content is unavailable")
		}
		data, err := os.ReadFile(filepath.Join(s.cfg.UploadDir, path))
		if err != nil {
			return file, nil, err
		}
		data, err = s.openCabinetBlob(ctx, file.ID, data)
		return file, data, err
	case "s3":
		data, err := s.s3GetObject(ctx, user.TenantID, file.StorageKey)
		if err != nil {
			return file, nil, err
		}
		data, err = s.openCabinetBlob(ctx, file.ID, data)
		return file, data, err
	default:
		data, _, err := s.downloadFromOAuthProvider(ctx, user.ID, file.ProviderID, file.ExternalID)
		return file, data, err
	}
}

// deleteOfficeVersion removes one Office document version row and its blob.
func (s *Server) deleteOfficeVersion(ctx context.Context, versionID, storageKey string) error {
	if _, err := s.store.DB.Exec(ctx, `DELETE FROM public.office_document_versions WHERE id=$1`, versionID); err != nil {
		return err
	}
	if strings.HasPrefix(storageKey, "ofv_") {
		s.officeRemoveStorage(storageKey)
	}
	return nil
}

// purgeFileRecord deletes a file row and its provider/office blobs. It is used
// by the paid file-retention worker; ownership and tenancy are rechecked.
func (s *Server) purgeFileRecord(ctx context.Context, fileID, ownerID, tenantID string) error {
	var provider, storageKey string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT provider_id, storage_key FROM public.files WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, fileID, ownerID, tenantID).Scan(&provider, &storageKey); err != nil {
		return err
	}
	officeKeys := []string{}
	rows, err := s.store.DB.Query(ctx, `
		SELECT v.storage_key FROM public.office_document_versions v
		JOIN public.office_documents d ON d.id=v.document_id
		WHERE d.file_id=$1
	`, fileID)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil && strings.HasPrefix(key, "ofv_") {
				officeKeys = append(officeKeys, key)
			}
		}
		rows.Close()
	}
	if provider == "s3" && storageKey != "" {
		if err := s.s3DeleteObject(ctx, tenantID, storageKey); err != nil {
			return err
		}
	}
	result, err := s.store.DB.Exec(ctx, `DELETE FROM public.files WHERE id=$1 AND user_id=$2 AND tenant_id=$3`, fileID, ownerID, tenantID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("file not found")
	}
	for _, key := range officeKeys {
		s.officeRemoveStorage(key)
	}
	return nil
}
