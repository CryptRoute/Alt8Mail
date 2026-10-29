package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type oauthProviderConfig struct {
	AuthorizeURL, TokenURL, Scope string
	ExtraAuth                     map[string]string
}

func (s *Server) oauthConfig(ctx context.Context, tenantID, providerID string) (oauthProviderConfig, string, string, bool) {
	cfg := s.storageRuntimeConfig(ctx, tenantID, providerID)
	if !cfg.Enabled || !cfg.Configured || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return oauthProviderConfig{}, "", "", false
	}
	switch providerID {
	case "gdrive":
		return oauthProviderConfig{
			AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scope:        "openid email https://www.googleapis.com/auth/drive",
			ExtraAuth:    map[string]string{"access_type": "offline", "prompt": "consent"},
		}, cfg.ClientID, cfg.ClientSecret, true
	case "dropbox":
		return oauthProviderConfig{
			AuthorizeURL: "https://www.dropbox.com/oauth2/authorize",
			TokenURL:     "https://api.dropboxapi.com/oauth2/token",
			Scope:        "account_info.read files.metadata.read files.content.read files.content.write",
			ExtraAuth:    map[string]string{"token_access_type": "offline"},
		}, cfg.ClientID, cfg.ClientSecret, true
	case "onedrive":
		return oauthProviderConfig{
			AuthorizeURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scope:        "offline_access Files.ReadWrite User.Read",
			ExtraAuth:    map[string]string{"prompt": "consent"},
		}, cfg.ClientID, cfg.ClientSecret, true
	}
	return oauthProviderConfig{}, "", "", false
}

func (s *Server) beginStorageOAuth(ctx context.Context, sess *sessionRecord, providerID string) (string, error) {
	config, clientID, _, ok := s.oauthConfig(ctx, sess.User.TenantID, providerID)
	if !ok {
		return "", fmt.Errorf("OAuth is not configured for %s", providerID)
	}
	state := randomToken(24)
	_, err := s.store.DB.Exec(ctx, `
		INSERT INTO storage_oauth_states (id, user_id, tenant_id, provider_id, expires_at)
		VALUES ($1, $2, $3, $4, now() + interval '10 minutes')
	`, state, sess.User.ID, sess.User.TenantID, providerID)
	if err != nil {
		return "", fmt.Errorf("could not start OAuth")
	}
	values := url.Values{
		"client_id": {clientID}, "redirect_uri": {s.oauthRedirectURL(providerID)},
		"response_type": {"code"}, "scope": {config.Scope}, "state": {state},
	}
	for key, value := range config.ExtraAuth {
		values.Set(key, value)
	}
	return config.AuthorizeURL + "?" + values.Encode(), nil
}

func (s *Server) startStorageOAuth(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	providerID := r.PathValue("provider")
	target, err := s.beginStorageOAuth(r.Context(), sess, providerID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OAUTH", err.Error())
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) storageOAuthCallback(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider")
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if state == "" || code == "" {
		s.redirectOAuthResult(w, r, "error", "OAuth authorization was cancelled")
		return
	}
	var userID, tenantID, storedProvider string
	err := s.store.DB.QueryRow(r.Context(), `
		DELETE FROM storage_oauth_states
		WHERE id=$1 AND provider_id=$2 AND expires_at > now()
		RETURNING user_id::text, tenant_id::text, provider_id
	`, state, providerID).Scan(&userID, &tenantID, &storedProvider)
	if err != nil {
		s.redirectOAuthResult(w, r, "error", "OAuth state is invalid or expired")
		return
	}
	config, clientID, clientSecret, ok := s.oauthConfig(r.Context(), tenantID, providerID)
	if !ok {
		s.redirectOAuthResult(w, r, "error", "OAuth provider is not configured")
		return
	}
	token, err := s.exchangeOAuthCode(r.Context(), config, clientID, clientSecret, providerID, code)
	if err != nil {
		s.redirectOAuthResult(w, r, "error", err.Error())
		return
	}
	accountEmail, err := s.oauthAccountEmail(r.Context(), providerID, token.AccessToken)
	if err != nil {
		s.redirectOAuthResult(w, r, "error", "Could not read the provider account")
		return
	}
	accessEnc, err := s.sealWorkspaceToken(token.AccessToken)
	if err != nil {
		s.redirectOAuthResult(w, r, "error", "Could not protect the OAuth token")
		return
	}
	refreshEnc, _ := s.sealWorkspaceToken(token.RefreshToken)
	var expiresAt any
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO storage_accounts (id, tenant_id, user_id, provider_id, provider_kind, account_email,
		                              access_token_enc, refresh_token_enc, token_expires_at, scope, status)
		VALUES ($1,$2,$3,$4,'oauth',$5,$6,$7,$8,$9,'connected')
		ON CONFLICT (user_id, provider_id) DO UPDATE
		SET account_email=EXCLUDED.account_email, access_token_enc=EXCLUDED.access_token_enc,
		    refresh_token_enc=CASE WHEN EXCLUDED.refresh_token_enc <> '' THEN EXCLUDED.refresh_token_enc
		                            ELSE storage_accounts.refresh_token_enc END,
		    token_expires_at=EXCLUDED.token_expires_at, scope=EXCLUDED.scope,
		    status='connected', updated_at=now()
	`, newID("storage"), tenantID, userID, providerID, accountEmail, accessEnc, refreshEnc, expiresAt, token.Scope)
	if err != nil {
		s.redirectOAuthResult(w, r, "error", "Could not save the provider account")
		return
	}
	sess := &sessionRecord{User: userRecord{ID: userID, TenantID: tenantID}}
	_ = s.syncStorageProvider(r.Context(), sess, providerID)
	s.redirectOAuthResult(w, r, "connected", "")
}

func (s *Server) redirectOAuthResult(w http.ResponseWriter, r *http.Request, status, message string) {
	values := url.Values{"storage": {status}}
	if message != "" {
		values.Set("storage_error", message)
	}
	http.Redirect(w, r, strings.TrimRight(s.cfg.PublicURL, "/")+"/?"+values.Encode(), http.StatusFound)
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

func (s *Server) exchangeOAuthCode(ctx context.Context, config oauthProviderConfig, clientID, clientSecret, providerID, code string) (oauthToken, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"client_secret": {clientSecret}, "redirect_uri": {s.oauthRedirectURL(providerID)},
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenURL, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return oauthToken{}, fmt.Errorf("OAuth token exchange failed")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if response.StatusCode >= 300 {
		return oauthToken{}, fmt.Errorf("OAuth token exchange rejected (%d)", response.StatusCode)
	}
	var token oauthToken
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return oauthToken{}, fmt.Errorf("OAuth provider returned no access token")
	}
	return token, nil
}

func (s *Server) oauthAccountEmail(ctx context.Context, providerID, accessToken string) (string, error) {
	var endpoint, method string
	switch providerID {
	case "gdrive":
		endpoint, method = "https://openidconnect.googleapis.com/v1/userinfo", http.MethodGet
	case "dropbox":
		endpoint, method = "https://api.dropboxapi.com/2/users/get_current_account", http.MethodPost
	case "onedrive":
		endpoint, method = "https://graph.microsoft.com/v1.0/me?$select=mail,userPrincipalName", http.MethodGet
	default:
		return "", fmt.Errorf("unsupported OAuth provider")
	}
	request, _ := http.NewRequestWithContext(ctx, method, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("provider account lookup failed (%d)", response.StatusCode)
	}
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	for _, key := range []string{"email", "mail", "userPrincipalName"} {
		if value := stateString(payload, key); value != "" {
			return strings.ToLower(value), nil
		}
	}
	return "", fmt.Errorf("provider returned no account email")
}

func (s *Server) oauthAccessToken(ctx context.Context, userID, providerID string) (string, error) {
	var accessEnc, refreshEnc, tenantID string
	var expiresAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT access_token_enc, refresh_token_enc, token_expires_at, tenant_id::text
		FROM storage_accounts WHERE user_id=$1 AND provider_id=$2 AND status='connected'
	`, userID, providerID).Scan(&accessEnc, &refreshEnc, &expiresAt, &tenantID)
	if err != nil {
		return "", fmt.Errorf("%s is not connected", providerID)
	}
	if expiresAt == nil || expiresAt.After(time.Now().Add(60*time.Second)) {
		return s.openWorkspaceToken(accessEnc)
	}
	refresh, err := s.openWorkspaceToken(refreshEnc)
	if err != nil || refresh == "" {
		return "", fmt.Errorf("%s authorization expired; reconnect the provider", providerID)
	}
	config, clientID, clientSecret, ok := s.oauthConfig(ctx, tenantID, providerID)
	if !ok {
		return "", fmt.Errorf("OAuth provider is not configured")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}, "client_secret": {clientSecret}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenURL, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed")
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("OAuth refresh was rejected; reconnect the provider")
	}
	var token oauthToken
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("OAuth refresh returned no access token")
	}
	accessEnc, _ = s.sealWorkspaceToken(token.AccessToken)
	refreshEnc, _ = s.sealWorkspaceToken(firstNonEmpty(token.RefreshToken, refresh))
	var newExpiry any
	if token.ExpiresIn > 0 {
		newExpiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE storage_accounts SET access_token_enc=$3, refresh_token_enc=$4, token_expires_at=$5, updated_at=now()
		WHERE user_id=$1 AND provider_id=$2
	`, userID, providerID, accessEnc, refreshEnc, newExpiry)
	return token.AccessToken, nil
}

func storageHTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}

func (s *Server) syncStorageProvider(ctx context.Context, sess *sessionRecord, providerID string) error {
	provider, ok := s.storageProvider(providerID)
	if !ok {
		return fmt.Errorf("unknown storage provider")
	}
	switch provider.Kind {
	case "s3":
		return s.syncS3Provider(ctx, sess)
	case "oauth":
		return s.syncOAuthProvider(ctx, sess, providerID)
	default:
		return nil
	}
}

func (s *Server) syncOAuthProvider(ctx context.Context, sess *sessionRecord, providerID string) error {
	token, err := s.oauthAccessToken(ctx, sess.User.ID, providerID)
	if err != nil {
		return err
	}
	switch providerID {
	case "gdrive":
		return s.syncGoogleDrive(ctx, sess, token)
	case "dropbox":
		return s.syncDropbox(ctx, sess, token)
	case "onedrive":
		return s.syncOneDrive(ctx, sess, token)
	}
	return fmt.Errorf("unsupported OAuth provider")
}

func (s *Server) upsertRemoteFile(ctx context.Context, sess *sessionRecord, providerID, externalID, name, mime, path, externalURL string, size int64) {
	if externalID == "" || name == "" {
		return
	}
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path,
		                  description, size_bytes, external_url, previewable, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'{}')
		ON CONFLICT (user_id, provider_id, external_id) WHERE external_id <> ''
		DO UPDATE SET name=EXCLUDED.name, mime=EXCLUDED.mime, path=EXCLUDED.path,
		              size_bytes=EXCLUDED.size_bytes, external_url=EXCLUDED.external_url,
		              previewable=EXCLUDED.previewable, updated_at=now()
	`, "file_"+providerID+"_"+hashShort(externalID), sess.User.TenantID, sess.User.ID, providerID,
		externalID, name, mime, path, "Synced from "+providerID, size, externalURL, filePreviewable(mime))
}

func (s *Server) syncGoogleDrive(ctx context.Context, sess *sessionRecord, token string) error {
	pageToken := ""
	total := int64(0)
	for {
		values := url.Values{
			"pageSize": {"1000"}, "q": {"trashed=false"},
			"fields": {"nextPageToken,files(id,name,mimeType,size,modifiedTime,webViewLink,parents)"},
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/files?"+values.Encode(), nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
		response.Body.Close()
		if response.StatusCode >= 300 {
			return fmt.Errorf("Google Drive listing failed (%d)", response.StatusCode)
		}
		var payload struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []struct {
				ID, Name, MimeType, Size, WebViewLink string
			} `json:"files"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		for _, file := range payload.Files {
			if file.MimeType == "application/vnd.google-apps.folder" {
				continue
			}
			size, _ := strconv.ParseInt(file.Size, 10, 64)
			total += size
			s.upsertRemoteFile(ctx, sess, "gdrive", file.ID, file.Name, firstNonEmpty(file.MimeType, "application/octet-stream"), "Google Drive", file.WebViewLink, size)
		}
		if payload.NextPageToken == "" {
			break
		}
		pageToken = payload.NextPageToken
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE storage_accounts SET used_bytes=$3, last_sync_at=now(), status='connected' WHERE user_id=$1 AND provider_id=$2`, sess.User.ID, "gdrive", total)
	return nil
}

func (s *Server) syncDropbox(ctx context.Context, sess *sessionRecord, token string) error {
	type dropboxEntry struct {
		Tag         string `json:".tag"`
		ID          string `json:"id"`
		Name        string `json:"name"`
		PathDisplay string `json:"path_display"`
		PathLower   string `json:"path_lower"`
		Size        int64  `json:"size"`
	}
	var cursor string
	total := int64(0)
	for {
		endpoint := "https://api.dropboxapi.com/2/files/list_folder"
		bodyText := `{"path":"","recursive":true,"limit":2000}`
		if cursor != "" {
			endpoint = "https://api.dropboxapi.com/2/files/list_folder/continue"
			bodyText = fmt.Sprintf(`{"cursor":%q}`, cursor)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(bodyText))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
		response.Body.Close()
		if response.StatusCode >= 300 {
			return fmt.Errorf("Dropbox listing failed (%d)", response.StatusCode)
		}
		var payload struct {
			Entries []dropboxEntry `json:"entries"`
			Cursor  string         `json:"cursor"`
			HasMore bool           `json:"has_more"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		for _, entry := range payload.Entries {
			if entry.Tag != "file" {
				continue
			}
			total += entry.Size
			path := entry.PathDisplay
			if index := strings.LastIndex(path, "/"); index > 0 {
				path = path[1:index]
			}
			s.upsertRemoteFile(ctx, sess, "dropbox", entry.PathLower, entry.Name, mimeForExtension(entry.Name), path, "", entry.Size)
		}
		if !payload.HasMore {
			break
		}
		cursor = payload.Cursor
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE storage_accounts SET used_bytes=$3, last_sync_at=now(), status='connected' WHERE user_id=$1 AND provider_id=$2`, sess.User.ID, "dropbox", total)
	return nil
}

func (s *Server) syncOneDrive(ctx context.Context, sess *sessionRecord, token string) error {
	endpoint := "https://graph.microsoft.com/v1.0/me/drive/root/delta?$top=200"
	total := int64(0)
	for endpoint != "" {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
		response.Body.Close()
		if response.StatusCode >= 300 {
			return fmt.Errorf("OneDrive listing failed (%d)", response.StatusCode)
		}
		var payload struct {
			NextLink string `json:"@odata.nextLink"`
			Value    []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				WebURL string `json:"webUrl"`
				Size   int64  `json:"size"`
				File   *struct {
					MimeType string `json:"mimeType"`
				} `json:"file"`
				ParentReference struct {
					Path string `json:"path"`
				} `json:"parentReference"`
			} `json:"value"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		for _, entry := range payload.Value {
			if entry.File == nil {
				continue
			}
			total += entry.Size
			parent := strings.TrimPrefix(entry.ParentReference.Path, "/drive/root:")
			parent = firstNonEmpty(strings.Trim(parent, "/"), "OneDrive")
			s.upsertRemoteFile(ctx, sess, "onedrive", entry.ID, entry.Name, firstNonEmpty(entry.File.MimeType, "application/octet-stream"), parent, entry.WebURL, entry.Size)
		}
		endpoint = payload.NextLink
	}
	_, _ = s.store.DB.Exec(ctx, `UPDATE storage_accounts SET used_bytes=$3, last_sync_at=now(), status='connected' WHERE user_id=$1 AND provider_id=$2`, sess.User.ID, "onedrive", total)
	return nil
}

func (s *Server) uploadToOAuthProvider(ctx context.Context, sess *sessionRecord, providerID, name, mime string, data []byte) (string, string, error) {
	token, err := s.oauthAccessToken(ctx, sess.User.ID, providerID)
	if err != nil {
		return "", "", err
	}
	switch providerID {
	case "gdrive":
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.SetBoundary("a8m" + randomToken(8))
		meta, _ := writer.CreatePart(map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}})
		_, _ = meta.Write([]byte(fmt.Sprintf(`{"name":%q,"mimeType":%q}`, name, mime)))
		part, _ := writer.CreatePart(map[string][]string{"Content-Type": {mime}})
		_, _ = part.Write(data)
		writer.Close()
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id,webViewLink", &body)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "multipart/related; boundary="+writer.Boundary())
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return "", "", err
		}
		defer response.Body.Close()
		payloadBody, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
		if response.StatusCode >= 300 {
			return "", "", fmt.Errorf("Google Drive upload failed (%d)", response.StatusCode)
		}
		var result map[string]any
		_ = json.Unmarshal(payloadBody, &result)
		return stateString(result, "id"), stateString(result, "webViewLink"), nil
	case "dropbox":
		arg := fmt.Sprintf(`{"path":"/Alt8Mail/%s","mode":"add","autorename":true,"mute":false}`, strings.ReplaceAll(name, `"`, ""))
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://content.dropboxapi.com/2/files/upload", bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Dropbox-API-Arg", arg)
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return "", "", err
		}
		defer response.Body.Close()
		payloadBody, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
		if response.StatusCode >= 300 {
			return "", "", fmt.Errorf("Dropbox upload failed (%d)", response.StatusCode)
		}
		var result map[string]any
		_ = json.Unmarshal(payloadBody, &result)
		return firstNonEmpty(stateString(result, "path_lower"), stateString(result, "id")), stateString(result, "path_display"), nil
	case "onedrive":
		endpoint := "https://graph.microsoft.com/v1.0/me/drive/root:/Alt8Mail/" + url.PathEscape(name) + ":/content"
		request, _ := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", mime)
		response, err := storageHTTPClient().Do(request)
		if err != nil {
			return "", "", err
		}
		defer response.Body.Close()
		payloadBody, _ := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
		if response.StatusCode >= 300 {
			return "", "", fmt.Errorf("OneDrive upload failed (%d)", response.StatusCode)
		}
		var result map[string]any
		_ = json.Unmarshal(payloadBody, &result)
		return stateString(result, "id"), stateString(result, "webUrl"), nil
	}
	return "", "", fmt.Errorf("unsupported OAuth provider")
}

func (s *Server) downloadFromOAuthProvider(ctx context.Context, userID, providerID, externalID string) ([]byte, string, error) {
	if externalID == "" {
		return nil, "", fmt.Errorf("file has no provider identifier")
	}
	token, err := s.oauthAccessToken(ctx, userID, providerID)
	if err != nil {
		return nil, "", err
	}
	var endpoint, method string
	switch providerID {
	case "gdrive":
		endpoint, method = "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(externalID)+"?alt=media", http.MethodGet
	case "dropbox":
		endpoint, method = "https://content.dropboxapi.com/2/files/download", http.MethodPost
	case "onedrive":
		endpoint, method = "https://graph.microsoft.com/v1.0/me/drive/items/"+url.PathEscape(externalID)+"/content", http.MethodGet
	default:
		return nil, "", fmt.Errorf("unsupported storage provider")
	}
	request, _ := http.NewRequestWithContext(ctx, method, endpoint, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if providerID == "dropbox" {
		request.Header.Set("Dropbox-API-Arg", fmt.Sprintf(`{"path":%q}`, externalID))
	}
	response, err := storageHTTPClient().Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("provider download failed (%d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxS3ObjectBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxS3ObjectBytes {
		return nil, "", fmt.Errorf("provider file exceeds the download limit")
	}
	return data, response.Header.Get("Content-Type"), nil
}
