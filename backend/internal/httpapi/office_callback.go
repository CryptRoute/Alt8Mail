package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
	"github.com/jackc/pgx/v5"
)

const (
	officeCallbackMaxBody  = 4 << 20
	officeDocumentMaxBytes = 100 << 20
)

// officeSignedContent serves a document to the OnlyOffice Document Server
// using a short-lived signed URL instead of a browser session.
func (s *Server) officeSignedContent(w http.ResponseWriter, r *http.Request) {
	secret := s.officeSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "OFFICE_SECRET", "Office is not configured")
		return
	}
	token, err := office.VerifyURLToken(secret, r.URL.Query().Get("token"), time.Now())
	if err != nil || token.Kind != office.TokenKindContent || token.FileID == "" {
		writeError(w, http.StatusUnauthorized, "OFFICE_TOKEN", "Invalid or expired document URL")
		return
	}
	var name, provider, storageKey, attachmentID string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT name, provider_id, storage_key, COALESCE(attachment_id,'') FROM files
		WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, token.FileID, token.UserID, token.TenantID).Scan(&name, &provider, &storageKey, &attachmentID)
	if err != nil || provider != "a8m" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	format, known := office.Lookup(name)
	mime := "application/octet-stream"
	if known {
		mime = format.MIME
	}
	path, ok := s.officeStoragePath(firstNonEmpty(storageKey, attachmentID))
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File content is unavailable")
		return
	}
	handle, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File content is unavailable")
		return
	}
	defer handle.Close()
	stat, err := handle.Stat()
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File content is unavailable")
		return
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, handle)
}

// officeCallback receives save events from the Document Server. The callback
// URL token proves which document was opened; the JWT body proves the event was
// produced by a Document Server that knows the shared secret.
func (s *Server) officeCallback(w http.ResponseWriter, r *http.Request) {
	secret := s.officeSigningSecret()
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "OFFICE_SECRET", "Office is not configured")
		return
	}
	urlToken, err := office.VerifyURLToken(secret, r.URL.Query().Get("token"), time.Now())
	if err != nil || urlToken.Kind != office.TokenKindCallback || urlToken.DocumentID == "" || urlToken.Key == "" {
		writeError(w, http.StatusUnauthorized, "OFFICE_TOKEN", "Invalid or expired callback URL")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, officeCallbackMaxBody+1))
	if err != nil || len(raw) > officeCallbackMaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "OFFICE_CALLBACK", "The Office callback is too large")
		return
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_CALLBACK", "The Office callback is malformed")
		return
	}
	jwtRaw, _ := envelope["token"].(string)
	if strings.TrimSpace(jwtRaw) == "" {
		jwtRaw = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	claims, err := office.VerifyJWT(secret, jwtRaw)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "OFFICE_CALLBACK", "The Office callback signature is invalid")
		return
	}
	if nested, ok := claims["payload"].(map[string]any); ok {
		claims = nested
	}
	status, ok := office.ClaimInt(claims, "status")
	if !ok || (status != 1 && status != 2 && status != 3 && status != 4 && status != 6 && status != 7) {
		writeError(w, http.StatusBadRequest, "OFFICE_CALLBACK", "The Office callback status is invalid")
		return
	}
	key := office.ClaimString(claims, "key")
	if key == "" || key != urlToken.Key {
		writeError(w, http.StatusBadRequest, "OFFICE_CALLBACK", "The Office callback key does not match the session")
		return
	}
	document, err := s.loadOfficeDocumentByID(r.Context(), urlToken.DocumentID)
	if err != nil || document.UserID != urlToken.UserID || document.TenantID != urlToken.TenantID {
		writeError(w, http.StatusNotFound, "OFFICE_CALLBACK", "Office document not found")
		return
	}
	if fileType := office.ClaimString(claims, "filetype"); fileType != "" && !strings.EqualFold(fileType, document.Format) {
		writeError(w, http.StatusBadRequest, "OFFICE_CALLBACK", "The Office callback file type does not match the document")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO office_edit_sessions (id, document_id, document_key, user_id, tenant_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (document_id, document_key) DO NOTHING
	`, newID("osess"), document.ID, key, urlToken.UserID, urlToken.TenantID)

	switch status {
	case 1, 3, 4, 7:
		sessionState := "open"
		switch status {
		case 3, 7:
			sessionState = "error"
		case 4:
			sessionState = "closed"
		}
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE office_edit_sessions SET status=$3, last_status=$4, last_callback_at=now()
			WHERE document_id=$1 AND document_key=$2
		`, document.ID, key, sessionState, status)
		writeJSON(w, http.StatusOK, map[string]any{"error": 0})
		return
	}

	// Status 2 is the final save; status 6 is a force-save while editing. Both
	// carry a URL with the current document content.
	fileURL := office.ClaimString(claims, "url")
	if strings.TrimSpace(fileURL) == "" {
		writeError(w, http.StatusBadRequest, "OFFICE_CALLBACK", "The Office callback has no document URL")
		return
	}
	if urlToken.ShareID != "" {
		var access string
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT s.access FROM file_shares s JOIN files f ON f.id=s.file_id
			WHERE s.id=$1 AND s.file_id=$2 AND s.revoked_at IS NULL
			  AND s.expires_at > now() AND NOT f.locked
		`, urlToken.ShareID, document.FileID).Scan(&access)
		if err != nil || access != "edit" {
			writeError(w, http.StatusForbidden, "OFFICE_SHARE", "This edit link is no longer active")
			return
		}
	}
	// Policy changes must also stop saves from editors that are already open.
	// Guest policy is evaluated against the verified recipient email; internal
	// policy uses the live user record so group and domain restrictions apply.
	if urlToken.ShareID != "" {
		policy, allowed := s.officeGuestAllowed(r.Context(), document.TenantID, urlToken.GuestEmail)
		if !allowed || !policy.ExternalEditing {
			writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Guest editing is no longer allowed by organization policy")
			return
		}
	} else {
		editorID := document.UserID
		if urlToken.EditorUserID != "" {
			editorID = urlToken.EditorUserID
		}
		var editorEmail string
		_ = s.store.DB.QueryRow(r.Context(), `
			SELECT email::text FROM users WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL
		`, editorID, document.TenantID).Scan(&editorEmail)
		if _, allowed := s.officeUserAllowed(r.Context(), userRecord{ID: editorID, TenantID: document.TenantID, Email: editorEmail}); !allowed {
			writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Office is no longer enabled for this editor by organization policy")
			return
		}
	}
	internalEditor := urlToken.EditorUserID != "" && urlToken.EditorUserID != document.UserID
	if internalEditor {
		var canEdit bool
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT EXISTS(
				SELECT 1 FROM file_permissions p
				WHERE p.file_id=$1 AND p.access='edit' AND (
					(p.grantee_type='user' AND p.grantee_id=$2) OR
					(p.grantee_type='group' AND p.grantee_id IN (
						SELECT gm.group_id FROM group_members gm
						JOIN groups g ON g.id = gm.group_id AND g.status = 'active' AND g.tenant_id = $3
						WHERE gm.user_id = $2
					))
				)
			)
		`, document.FileID, urlToken.EditorUserID, document.TenantID).Scan(&canEdit)
		if err != nil || !canEdit {
			writeError(w, http.StatusForbidden, "OFFICE_PERMISSION", "Your edit access to this file was revoked")
			return
		}
	}
	eventKey := officeCallbackEventKey(key, status, fileURL)
	eventID, state, err := s.officeClaimCallbackEvent(r.Context(), document.ID, key, status, eventKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_CALLBACK", "Could not record the Office callback")
		return
	}
	if state == "done" {
		writeJSON(w, http.StatusOK, map[string]any{"error": 0})
		return
	}
	if state == "processing" {
		// Autosave and the editor's Save button can deliver overlapping
		// callbacks for the same content. Wait briefly for the in-flight save
		// instead of reporting a false failure to the Document Server.
		deadline := time.Now().Add(8 * time.Second)
		for {
			var current string
			_ = s.store.DB.QueryRow(r.Context(), `
				SELECT state FROM office_callback_events WHERE id=$1
			`, eventID).Scan(&current)
			if current == "done" {
				writeJSON(w, http.StatusOK, map[string]any{"error": 0})
				return
			}
			if current == "failed" {
				eventID, state, err = s.officeClaimCallbackEvent(r.Context(), document.ID, key, status, eventKey)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "OFFICE_CALLBACK", "Could not retry the Office callback")
					return
				}
				if state == "done" {
					writeJSON(w, http.StatusOK, map[string]any{"error": 0})
					return
				}
				break
			}
			if time.Now().After(deadline) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": 1, "message": "Save already in progress"})
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	// OnlyOffice often reports its cache URL with an internal Docker hostname.
	// Rewrite that origin to the configured Office public URL, not the webmail
	// origin: only the Office gateway proxies the Document Server cache.
	var officePublicURL string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT public_url FROM public.office_settings WHERE id='default'
	`).Scan(&officePublicURL)
	data, _, err := s.officeFetchURL(r.Context(), officeDocumentFetchURL(fileURL, officePublicURL), officeDocumentMaxBytes)
	if err != nil {
		s.officeFailCallbackEvent(r.Context(), eventID, err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": 1, "message": err.Error()})
		return
	}
	if !officeValidDocumentBytes(document.Format, data) {
		message := "OnlyOffice returned invalid document content"
		s.officeFailCallbackEvent(r.Context(), eventID, message)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": 1, "message": message})
		return
	}
	actor := "OnlyOffice"
	action := "office.saved"
	createdBy := urlToken.UserID
	if urlToken.ShareID != "" {
		actor = "Guest link"
		if urlToken.GuestEmail != "" {
			actor = "Guest " + urlToken.GuestEmail
		}
		action = "office.guest_saved"
		createdBy = ""
	} else if internalEditor {
		actor = firstNonEmpty(urlToken.Actor, "Shared editor")
		action = "office.shared_saved"
		createdBy = urlToken.EditorUserID
	}
	var fileName string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT name FROM files WHERE id=$1`, document.FileID).Scan(&fileName)
	if fileName == "" {
		fileName = document.FileID
	}
	dlp := s.evaluateOfficeSaveDLP(r.Context(), document.TenantID, fileName, data)
	if dlp.Blocked {
		message := "This document is blocked by your organization's data protection policy."
		s.officeFailCallbackEvent(r.Context(), eventID, message+" "+strings.Join(dlp.Warnings, " "))
		s.officeGuestAudit(r.Context(), document.TenantID, document.UserID, actor, "office.dlp_blocked", fileName,
			truncate("Office save blocked · "+strings.Join(dlp.Warnings, " "), 500))
		writeJSON(w, http.StatusOK, map[string]any{
			"error": 1, "message": message, "classification": dlp.Classification,
			"warnings": dlp.Warnings, "matches": dlp.Matches,
		})
		return
	}
	officePolicy := s.tenantPolicyFor(r.Context(), document.TenantID).Office
	// A save adds a new immutable version. When retention will prune older
	// versions in the same transaction, those bytes are released immediately.
	prunedBytes := s.officeRetentionPruneBytes(r.Context(), document.ID, officePolicy.VersionRetention)
	addBytes := int64(len(data)) - prunedBytes
	if addBytes < 0 {
		addBytes = 0
	}
	owner, ownerErr := s.userByID(r.Context(), document.UserID)
	if ownerErr != nil {
		s.officeFailCallbackEvent(r.Context(), eventID, "the document owner is unavailable")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": 1, "message": "Could not check the Cabinet storage quota"})
		return
	}
	if quotaErr := s.enforceStorageQuota(r.Context(), owner, addBytes); quotaErr != nil {
		message := "This save would exceed the Cabinet storage quota. Remove files or ask an administrator for more storage."
		if urlToken.ShareID == "" && !internalEditor {
			message = quotaErr.Error()
		}
		s.officeFailCallbackEvent(r.Context(), eventID, message)
		s.officeGuestAudit(r.Context(), document.TenantID, document.UserID, actor, "office.quota_blocked", fileName,
			truncate("Office save blocked by the Cabinet quota · "+quotaErr.Error(), 500))
		writeJSON(w, http.StatusOK, map[string]any{"error": 1, "message": message})
		return
	}
	version, _, name, err := s.officeSaveCallbackVersion(r.Context(), document, key, data, dlp, officePolicy.VersionRetention, createdBy, eventID)
	if err != nil {
		s.officeFailCallbackEvent(r.Context(), eventID, err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": 1, "message": "Could not save the document"})
		return
	}
	detail := fmt.Sprintf("Saved version %d from the Office editor", version)
	if urlToken.ShareID != "" {
		detail = fmt.Sprintf("Guest saved version %d through a shared edit link", version)
	} else if internalEditor {
		detail = fmt.Sprintf("%s saved version %d through internal sharing", actor, version)
	}
	s.officeGuestAudit(r.Context(), document.TenantID, document.UserID, actor, action, name, detail)
	writeJSON(w, http.StatusOK, map[string]any{"error": 0})
}

func officeCallbackEventKey(key string, status int, fileURL string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", key, status, fileURL)))
	return hex.EncodeToString(sum[:])
}

func (s *Server) officeClaimCallbackEvent(ctx context.Context, documentID, documentKey string, status int, eventKey string) (string, string, error) {
	eventID := newID("ocb")
	var inserted string
	err := s.store.DB.QueryRow(ctx, `
		INSERT INTO office_callback_events (id, document_id, document_key, status, event_key)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (document_key, status, event_key) DO NOTHING
		RETURNING id
	`, eventID, documentID, documentKey, status, eventKey).Scan(&inserted)
	if err == nil {
		return inserted, "new", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	var existingID, state string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT id, state FROM office_callback_events
		WHERE document_key=$1 AND status=$2 AND event_key=$3
	`, documentKey, status, eventKey).Scan(&existingID, &state); err != nil {
		return "", "", err
	}
	if state == "failed" {
		if _, err := s.store.DB.Exec(ctx, `
			UPDATE office_callback_events SET state='processing', error='', updated_at=now() WHERE id=$1
		`, existingID); err != nil {
			return "", "", err
		}
		return existingID, "retry", nil
	}
	if state == "processing" {
		// A crashed save must not block this callback forever. Claim a stale
		// processing event after five minutes and let OnlyOffice retry.
		var claimed string
		err := s.store.DB.QueryRow(ctx, `
			UPDATE office_callback_events SET state='processing', updated_at=now()
			WHERE id=$1 AND state='processing' AND updated_at < now() - interval '5 minutes'
			RETURNING id
		`, existingID).Scan(&claimed)
		if err == nil {
			return claimed, "retry", nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return existingID, "processing", nil
		}
		return "", "", err
	}
	return existingID, state, nil
}

func (s *Server) officeFailCallbackEvent(ctx context.Context, eventID, message string) {
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE office_callback_events SET state='failed', error=$2, updated_at=now() WHERE id=$1
	`, eventID, truncate(message, 500))
}

func (s *Server) officeSaveCallbackVersion(ctx context.Context, document officeDocumentRecord, documentKey string, data []byte, dlp officeDLPResult, retention int, userID, eventID string) (int, string, string, error) {
	if dlp.Warnings == nil {
		dlp.Warnings = []string{}
	}
	if dlp.Matches == nil {
		dlp.Matches = []dlpMatch{}
	}
	warningsJSON, _ := json.Marshal(dlp.Warnings)
	matchesJSON, _ := json.Marshal(dlp.Matches)
	var prunedStorageKeys []string
	storageKey, checksum, err := s.officeStoreBytes(data, document.Format)
	if err != nil {
		return 0, "", "", fmt.Errorf("could not store the saved document")
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	defer tx.Rollback(ctx)
	var currentVersion int
	var fileID, name string
	if err := tx.QueryRow(ctx, `
		SELECT d.current_version, d.file_id, f.name
		FROM office_documents d JOIN files f ON f.id=d.file_id
		WHERE d.id=$1
		FOR UPDATE OF d
	`, document.ID).Scan(&currentVersion, &fileID, &name); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	nextVersion := currentVersion + 1
	versionID := newID("ofv")
	newDocumentKey := office.NewDocumentKey(document.ID, nextVersion)
	if _, err := tx.Exec(ctx, `
		INSERT INTO office_document_versions
		  (id, document_id, version, storage_key, size_bytes, checksum, source, created_by,
		   dlp_classification, dlp_warnings, dlp_matches)
		VALUES ($1,$2,$3,$4,$5,$6,'editor',NULLIF($7,'')::uuid,$8,$9::jsonb,$10::jsonb)
	`, versionID, document.ID, nextVersion, storageKey, len(data), checksum, userID,
		dlp.Classification, string(warningsJSON), string(matchesJSON)); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE office_documents SET current_version=$2, current_key=$3, last_saved_at=now(), updated_at=now() WHERE id=$1
	`, document.ID, nextVersion, newDocumentKey); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	if dlp.Scanned {
		metadataJSON, _ := json.Marshal(map[string]any{
			"dlpClassification": dlp.Classification,
			"dlpWarnings":       dlp.Warnings,
			"dlpMatches":        dlp.Matches,
		})
		if _, err := tx.Exec(ctx, `
			UPDATE files SET storage_key=$1, size_bytes=$2, checksum=$3, version=$4, updated_at=now(),
			  metadata = COALESCE(metadata,'{}'::jsonb) || $5::jsonb
			WHERE id=$6
		`, storageKey, len(data), checksum, nextVersion, string(metadataJSON), fileID); err != nil {
			s.officeRemoveStorage(storageKey)
			return 0, "", "", err
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE files SET storage_key=$1, size_bytes=$2, checksum=$3, version=$4, updated_at=now() WHERE id=$5
	`, storageKey, len(data), checksum, nextVersion, fileID); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	prunedStorageKeys, err = officePruneVersions(ctx, tx, document.ID, retention)
	if err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE office_edit_sessions SET status='saved', last_status=2, last_callback_at=now()
		WHERE document_id=$1 AND document_key=$2
	`, document.ID, documentKey); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE office_callback_events SET state='done', version_id=$2, error='', updated_at=now() WHERE id=$1
	`, eventID, versionID); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		s.officeRemoveStorage(storageKey)
		return 0, "", "", err
	}
	for _, key := range prunedStorageKeys {
		s.officeRemoveStorage(key)
	}
	return nextVersion, versionID, name, nil
}

func (s *Server) officeFetchURL(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, "", fmt.Errorf("the Office callback URL is invalid")
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("unsupported redirect scheme")
			}
			if req.URL.User != nil {
				return fmt.Errorf("redirect with credentials is not allowed")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("could not download the saved document: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the document download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("could not read the saved document")
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("the saved document is larger than the Office limit")
	}
	return data, response.Header.Get("Content-Type"), nil
}

func officeDocumentFetchURL(raw, publicBase string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	base, err := url.Parse(strings.TrimRight(publicBase, "/") + "/")
	if err != nil || base.Host == "" {
		return raw
	}
	if !parsed.IsAbs() {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.Host == "" {
		return raw
	}
	host := strings.ToLower(parsed.Hostname())
	internal := host == "localhost" || host == "::1" || !strings.Contains(host, ".")
	if _, err := netip.ParseAddr(host); err == nil {
		// Container names, loopback, private addresses and a raw public IP can
		// all appear in the Document Server cache URL. The raw IP especially
		// cannot complete the gateway TLS handshake, so route it through the
		// configured Office origin.
		internal = true
	}
	if internal {
		parsed.Scheme = base.Scheme
		parsed.Host = base.Host
	}
	return parsed.String()
}

func officeValidDocumentBytes(format string, data []byte) bool {
	if len(data) < 8 {
		return false
	}
	switch format {
	case "docx", "xlsx", "pptx", "odt", "ods", "odp":
		return bytes.HasPrefix(data, []byte("PK\x03\x04"))
	case "doc", "xls", "ppt":
		return bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	case "pdf":
		return bytes.HasPrefix(data, []byte("%PDF-"))
	case "csv", "txt", "rtf":
		return len(bytes.TrimSpace(data)) > 0
	default:
		return false
	}
}
