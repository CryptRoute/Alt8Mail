package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// storageRuntimeConfig is the effective connector configuration for one
// tenant. Database-backed values override the deployment environment so an
// organization can manage OAuth applications and S3 buckets from the UI.
type storageRuntimeConfig struct {
	ProviderID   string
	ClientID     string
	ClientSecret string
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UseSSL       bool
	PathStyle    bool
	Enabled      bool
	Configured   bool
	Source       string
}

func (s *Server) envStorageConfig(providerID string) storageRuntimeConfig {
	cfg := storageRuntimeConfig{ProviderID: providerID, Enabled: true, UseSSL: true, PathStyle: true}
	switch providerID {
	case "gdrive":
		cfg.ClientID = s.cfg.GoogleClientID
		cfg.ClientSecret = s.cfg.GoogleClientSecret
	case "dropbox":
		cfg.ClientID = s.cfg.DropboxClientID
		cfg.ClientSecret = s.cfg.DropboxClientSecret
	case "onedrive":
		cfg.ClientID = s.cfg.MicrosoftClientID
		cfg.ClientSecret = s.cfg.MicrosoftClientSecret
	case "s3":
		cfg.Endpoint = s.cfg.S3Endpoint
		cfg.Region = s.cfg.S3Region
		cfg.Bucket = s.cfg.S3Bucket
		cfg.AccessKey = s.cfg.S3AccessKey
		cfg.SecretKey = s.cfg.S3SecretKey
		cfg.UseSSL = s.cfg.S3UseSSL
		cfg.PathStyle = s.cfg.S3PathStyle
	}
	cfg.Configured = storageConfigComplete(cfg)
	if cfg.Configured {
		cfg.Source = "environment"
	}
	return cfg
}

func storageConfigComplete(cfg storageRuntimeConfig) bool {
	switch cfg.ProviderID {
	case "gdrive", "dropbox", "onedrive":
		return cfg.ClientID != "" && cfg.ClientSecret != ""
	case "s3":
		return cfg.Bucket != "" && cfg.AccessKey != "" && cfg.SecretKey != ""
	default:
		return true
	}
}

func (s *Server) storageRuntimeConfig(ctx context.Context, tenantID, providerID string) storageRuntimeConfig {
	cfg := s.envStorageConfig(providerID)
	var clientID, secretEnc string
	var raw []byte
	var enabled bool
	err := s.store.DB.QueryRow(ctx, `
		SELECT client_id, client_secret_enc, config, enabled
		FROM storage_provider_configs WHERE tenant_id=$1 AND provider_id=$2
	`, tenantID, providerID).Scan(&clientID, &secretEnc, &raw, &enabled)
	if err != nil {
		return cfg
	}
	cfg.Enabled = enabled
	cfg.Source = "database"
	if strings.TrimSpace(clientID) != "" {
		cfg.ClientID = strings.TrimSpace(clientID)
	}
	if secretEnc != "" {
		secret, openErr := s.openWorkspaceToken(secretEnc)
		if openErr != nil {
			s.logger.Warn("storage credential decrypt failed", "provider", providerID, "tenant", tenantID, "err", openErr)
			cfg.Configured = false
			cfg.Source = "unreadable"
			return cfg
		}
		cfg.ClientSecret = secret
		if providerID == "s3" {
			cfg.SecretKey = secret
		}
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) == nil {
		if value, ok := values["endpoint"].(string); ok && value != "" {
			cfg.Endpoint = strings.TrimSpace(value)
		}
		if value, ok := values["region"].(string); ok && value != "" {
			cfg.Region = strings.TrimSpace(value)
		}
		if value, ok := values["bucket"].(string); ok && value != "" {
			cfg.Bucket = strings.TrimSpace(value)
		}
		if value, ok := values["accessKey"].(string); ok && value != "" {
			cfg.AccessKey = strings.TrimSpace(value)
		}
		if value, ok := values["useSSL"].(bool); ok {
			cfg.UseSSL = value
		}
		if value, ok := values["pathStyle"].(bool); ok {
			cfg.PathStyle = value
		}
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cfg.Configured = cfg.Enabled && storageConfigComplete(cfg)
	return cfg
}

func (s *Server) storageProviderDefsFor(ctx context.Context, tenantID string) []storageProviderDef {
	defs := s.storageProviderDefs()
	for index := range defs {
		if defs[index].ID == "a8m" {
			continue
		}
		defs[index].Configured = s.storageRuntimeConfig(ctx, tenantID, defs[index].ID).Configured
	}
	return defs
}

func (s *Server) defaultStorageProvider(ctx context.Context, userID string) string {
	var providerID string
	if err := s.store.DB.QueryRow(ctx, `SELECT provider_id FROM storage_defaults WHERE user_id=$1`, userID).Scan(&providerID); err != nil || providerID == "" {
		return "a8m"
	}
	return providerID
}

func (s *Server) setDefaultStorage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body struct {
		ProviderID string `json:"providerId"`
	}
	if !s.decodeBody(w, r, &body, 8*1024) {
		return
	}
	providerID := strings.TrimSpace(body.ProviderID)
	provider, ok := s.storageProvider(providerID)
	if !ok {
		writeError(w, http.StatusBadRequest, "STORAGE_PROVIDER", "Choose a valid storage provider")
		return
	}
	if provider.Kind != "internal" {
		connected, err := s.storageConnected(r.Context(), sess.User.ID, providerID)
		if err != nil || !connected {
			writeError(w, http.StatusConflict, "PROVIDER_OFFLINE", "Connect that storage provider before making it the default")
			return
		}
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_defaults (user_id, tenant_id, provider_id, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET tenant_id=EXCLUDED.tenant_id, provider_id=EXCLUDED.provider_id, updated_at=now()
	`, sess.User.ID, sess.User.TenantID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_DEFAULT", "Could not save the default storage provider")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "storage.default", provider.Name, "Default storage changed")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "defaultProvider": providerID})
}

type storageCredentialBody struct {
	ClientID     *string `json:"clientId"`
	ClientSecret *string `json:"clientSecret"`
	Endpoint     *string `json:"endpoint"`
	Region       *string `json:"region"`
	Bucket       *string `json:"bucket"`
	AccessKey    *string `json:"accessKey"`
	SecretKey    *string `json:"secretKey"`
	UseSSL       *bool   `json:"useSSL"`
	PathStyle    *bool   `json:"pathStyle"`
	Enabled      *bool   `json:"enabled"`
}

func (s *Server) storageCredentialsPayload(ctx context.Context, tenantID, providerID string) map[string]any {
	provider, _ := s.storageProvider(providerID)
	cfg := s.storageRuntimeConfig(ctx, tenantID, providerID)
	return map[string]any{
		"id": provider.ID, "name": provider.Name, "kind": provider.Kind,
		"configured": cfg.Configured, "enabled": cfg.Enabled, "source": cfg.Source,
		"clientId": cfg.ClientID, "hasClientSecret": cfg.ClientSecret != "", "hasSecret": cfg.SecretKey != "",
		"endpoint": cfg.Endpoint, "region": cfg.Region, "bucket": cfg.Bucket, "accessKey": cfg.AccessKey,
		"useSSL": cfg.UseSSL, "pathStyle": cfg.PathStyle,
		"redirectUri": s.oauthRedirectURL(providerID),
	}
}

func (s *Server) getStorageCredentials(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "STORAGE_ADMIN", "Only organization owners and administrators can manage storage credentials")
		return
	}
	providers := []map[string]any{}
	for _, provider := range s.storageProviderDefsFor(r.Context(), sess.User.TenantID) {
		if provider.ID == "a8m" {
			continue
		}
		providers = append(providers, s.storageCredentialsPayload(r.Context(), sess.User.TenantID, provider.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func (s *Server) putStorageCredentials(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "STORAGE_ADMIN", "Only organization owners and administrators can manage storage credentials")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := s.storageProvider(providerID)
	if !ok || provider.Kind == "internal" {
		writeError(w, http.StatusBadRequest, "STORAGE_PROVIDER", "Choose a valid external storage provider")
		return
	}
	var body storageCredentialBody
	if !s.decodeBody(w, r, &body, 128*1024) {
		return
	}
	current := s.storageRuntimeConfig(r.Context(), sess.User.TenantID, providerID)
	var existingSecretEnc string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT client_secret_enc FROM storage_provider_configs WHERE tenant_id=$1 AND provider_id=$2`, sess.User.TenantID, providerID).Scan(&existingSecretEnc)
	clientID := current.ClientID
	if body.ClientID != nil {
		clientID = strings.TrimSpace(*body.ClientID)
	}
	secret := current.ClientSecret
	if provider.Kind == "s3" {
		secret = current.SecretKey
	}
	if provider.Kind == "oauth" {
		if body.ClientSecret != nil && strings.TrimSpace(*body.ClientSecret) != "" {
			secret = strings.TrimSpace(*body.ClientSecret)
		}
		if clientID == "" || secret == "" {
			writeError(w, http.StatusBadRequest, "STORAGE_CREDENTIALS", "Client ID and client secret are required")
			return
		}
	} else {
		if body.Bucket != nil {
			current.Bucket = strings.TrimSpace(*body.Bucket)
		}
		if body.AccessKey != nil {
			current.AccessKey = strings.TrimSpace(*body.AccessKey)
		}
		if body.SecretKey != nil && strings.TrimSpace(*body.SecretKey) != "" {
			secret = strings.TrimSpace(*body.SecretKey)
		}
		if body.Endpoint != nil {
			current.Endpoint = strings.TrimSpace(*body.Endpoint)
		}
		if body.Region != nil {
			current.Region = strings.TrimSpace(*body.Region)
		}
		if body.UseSSL != nil {
			current.UseSSL = *body.UseSSL
		}
		if body.PathStyle != nil {
			current.PathStyle = *body.PathStyle
		}
		if current.Bucket == "" || current.AccessKey == "" || secret == "" {
			writeError(w, http.StatusBadRequest, "STORAGE_CREDENTIALS", "Bucket, access key and secret key are required")
			return
		}
		if current.Region == "" {
			current.Region = "us-east-1"
		}
	}
	secretEnc := existingSecretEnc
	if body.ClientSecret != nil && strings.TrimSpace(*body.ClientSecret) != "" {
		sealed, err := s.sealWorkspaceToken(strings.TrimSpace(*body.ClientSecret))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STORAGE_CREDENTIALS", "Could not protect the client secret")
			return
		}
		secretEnc = sealed
	}
	if body.SecretKey != nil && strings.TrimSpace(*body.SecretKey) != "" {
		sealed, err := s.sealWorkspaceToken(strings.TrimSpace(*body.SecretKey))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "STORAGE_CREDENTIALS", "Could not protect the secret key")
			return
		}
		secretEnc = sealed
	}
	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	configJSON, _ := json.Marshal(map[string]any{
		"endpoint": current.Endpoint, "region": current.Region, "bucket": current.Bucket,
		"accessKey": current.AccessKey, "useSSL": current.UseSSL, "pathStyle": current.PathStyle,
	})
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_provider_configs (tenant_id, provider_id, client_id, client_secret_enc, config, enabled, updated_by, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (tenant_id, provider_id) DO UPDATE
		SET client_id=EXCLUDED.client_id, client_secret_enc=EXCLUDED.client_secret_enc,
		    config=EXCLUDED.config, enabled=EXCLUDED.enabled, updated_by=EXCLUDED.updated_by, updated_at=now()
	`, sess.User.TenantID, providerID, clientID, secretEnc, configJSON, enabled, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_CREDENTIALS", "Could not save storage credentials")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "storage.credentials_updated", provider.Name, "Storage credentials updated")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": s.storageCredentialsPayload(r.Context(), sess.User.TenantID, providerID)})
}

func (s *Server) deleteStorageCredentials(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "STORAGE_ADMIN", "Only organization owners and administrators can manage storage credentials")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := s.storageProvider(providerID)
	if !ok || provider.Kind == "internal" {
		writeError(w, http.StatusBadRequest, "STORAGE_PROVIDER", "Choose a valid external storage provider")
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `DELETE FROM storage_provider_configs WHERE tenant_id=$1 AND provider_id=$2`, sess.User.TenantID, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_CREDENTIALS", "Could not clear storage credentials")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "storage.credentials_cleared", provider.Name, "Storage credentials cleared")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) s3ConfiguredFor(ctx context.Context, tenantID string) bool {
	return s.storageRuntimeConfig(ctx, tenantID, "s3").Configured
}

func (s *Server) s3Runtime(ctx context.Context, tenantID string) (storageRuntimeConfig, error) {
	cfg := s.storageRuntimeConfig(ctx, tenantID, "s3")
	if !cfg.Configured {
		return cfg, fmt.Errorf("S3/MinIO is not configured")
	}
	return cfg, nil
}
