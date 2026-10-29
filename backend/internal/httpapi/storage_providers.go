package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type storageProviderDef struct {
	ID, Name, Kind, Color, Description string
	Configured                         bool
}

func (s *Server) storageProviderDefs() []storageProviderDef {
	return []storageProviderDef{
		{ID: "meridian", Name: "Meridian Drive", Kind: "internal", Color: "#0f6cbd",
			Description: "Company-managed storage on this server.", Configured: true},
		{ID: "s3", Name: "S3 / MinIO", Kind: "s3", Color: "#d97706",
			Description: "Use the configured S3-compatible bucket.", Configured: s.s3Configured()},
		{ID: "gdrive", Name: "Google Drive", Kind: "oauth", Color: "#188038",
			Description: "Connect Google Workspace Drive.", Configured: s.cfg.GoogleClientID != "" && s.cfg.GoogleClientSecret != ""},
		{ID: "dropbox", Name: "Dropbox Business", Kind: "oauth", Color: "#0061ff",
			Description: "Connect a Dropbox Business account.", Configured: s.cfg.DropboxClientID != "" && s.cfg.DropboxClientSecret != ""},
		{ID: "onedrive", Name: "Microsoft OneDrive", Kind: "oauth", Color: "#0364b8",
			Description: "Connect a Microsoft 365 OneDrive.", Configured: s.cfg.MicrosoftClientID != "" && s.cfg.MicrosoftClientSecret != ""},
	}
}

func (s *Server) s3Configured() bool {
	return s.cfg.S3Bucket != "" && s.cfg.S3AccessKey != "" && s.cfg.S3SecretKey != ""
}

func (s *Server) storageProvider(id string) (storageProviderDef, bool) {
	for _, provider := range s.storageProviderDefs() {
		if provider.ID == id {
			return provider, true
		}
	}
	return storageProviderDef{}, false
}

func (s *Server) publicStorageProviders(ctx context.Context, user userRecord) ([]map[string]any, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT provider_id, account_email::text, status, used_bytes, limit_bytes, last_sync_at
		FROM storage_accounts WHERE user_id = $1
	`, user.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := map[string]storageAccountSummary{}
	for rows.Next() {
		var account storageAccountSummary
		if rows.Scan(&account.ProviderID, &account.AccountEmail, &account.Status, &account.UsedBytes, &account.LimitBytes, &account.LastSyncAt) == nil {
			accounts[account.ProviderID] = account
		}
	}
	providerFiles := map[string]int64{}
	fileRows, err := s.store.DB.Query(ctx, `
		SELECT provider_id, count(*), COALESCE(sum(size_bytes),0) FROM files WHERE user_id = $1 GROUP BY provider_id
	`, user.ID)
	if err == nil {
		for fileRows.Next() {
			var providerID string
			var count, used int64
			if fileRows.Scan(&providerID, &count, &used) == nil {
				providerFiles[providerID] = used
			}
		}
		fileRows.Close()
	}
	providers := []map[string]any{}
	for _, provider := range s.storageProviderDefs() {
		account, hasAccount := accounts[provider.ID]
		connected := false
		accountEmail := ""
		status := "disconnected"
		used := providerFiles[provider.ID]
		var limit int64
		var lastSync any
		if provider.ID == "meridian" {
			connected = true
			accountEmail = user.Email
			status = "synced"
			limit = 100 * 1024 * 1024 * 1024
		} else if provider.Kind == "s3" {
			if hasAccount && account.Status == "connected" {
				connected = true
				accountEmail = firstNonEmpty(account.AccountEmail, s.cfg.S3Bucket)
				status = "synced"
				used = account.UsedBytes
				limit = account.LimitBytes
				if account.LastSyncAt != nil {
					lastSync = account.LastSyncAt.UTC().Format(time.RFC3339)
				}
			}
		} else if hasAccount && account.Status == "connected" {
			connected = true
			accountEmail = account.AccountEmail
			status = "synced"
			used = account.UsedBytes
			limit = account.LimitBytes
			if account.LastSyncAt != nil {
				lastSync = account.LastSyncAt.UTC().Format(time.RFC3339)
			}
		}
		providers = append(providers, map[string]any{
			"id": provider.ID, "name": provider.Name, "kind": provider.Kind,
			"connected": connected, "account": accountEmail, "usedBytes": used, "limitBytes": limit,
			"color": provider.Color, "status": status, "lastSyncAt": lastSync,
			"description": provider.Description, "configured": provider.Configured,
		})
	}
	return providers, nil
}

type storageAccountSummary struct {
	ProviderID, AccountEmail, Status string
	UsedBytes, LimitBytes            int64
	LastSyncAt                       *time.Time
}

func (s *Server) workspaceKey() []byte {
	sum := sha256.Sum256([]byte(s.cfg.SessionSecret + "|meridian-workspace-token-v1"))
	return sum[:]
}

func (s *Server) sealWorkspaceToken(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	block, err := aes.NewCipher(s.workspaceKey())
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
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Server) openWorkspaceToken(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.workspaceKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted workspace token is truncated")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Server) oauthRedirectURL(providerID string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/api/storage/oauth/" + url.PathEscape(providerID) + "/callback"
}
