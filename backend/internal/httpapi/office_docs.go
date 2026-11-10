package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
	"github.com/jackc/pgx/v5"
)

const officeVersionLimit = 100

type officeDocumentRecord struct {
	ID             string
	TenantID       string
	UserID         string
	FileID         string
	Format         string
	DocumentType   string
	CurrentVersion int
	CurrentKey     string
}

type officeVersionRecord struct {
	ID                string
	Version           int
	StorageKey        string
	SizeBytes         int64
	Checksum          string
	Source            string
	CreatedAt         time.Time
	DLPClassification string
	DLPWarnings       []byte
	DLPMatches        []byte
	ReviewStatus      string
	ReviewNote        string
	ReviewedByEmail   string
	ReviewedAt        *time.Time
}

func (s *Server) registerOfficeDocumentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/office/documents", s.requireUser(s.createOfficeDocument))
	mux.HandleFunc("POST /api/office/documents/{id}/open", s.requireUser(s.openOfficeDocument))
	mux.HandleFunc("GET /api/office/documents/{id}/versions", s.requireUser(s.listOfficeVersions))
	mux.HandleFunc("GET /api/office/documents/{id}/versions/compare", s.requireUser(s.compareOfficeVersions))
	mux.HandleFunc("GET /api/office/documents/{id}/versions/{version}/download", s.requireUser(s.downloadOfficeVersion))
	mux.HandleFunc("POST /api/office/documents/{id}/versions/{version}/restore", s.requireUser(s.restoreOfficeVersion))
	mux.HandleFunc("POST /api/office/documents/{id}/versions/{version}/review", s.requireUser(s.reviewOfficeVersion))
	mux.HandleFunc("POST /api/office/documents/{id}/convert", s.requireUser(s.convertOfficeDocument))
	mux.HandleFunc("POST /api/office/ai", s.userFeature(FeatureOfficeAI, s.officeAI))
	mux.HandleFunc("GET /api/office/content", s.officeSignedContent)
	mux.HandleFunc("POST /api/office/callback", s.officeCallback)
	mux.HandleFunc("POST /api/public/shares/{token}/office", s.publicShareOfficeOpen)
}

func (s *Server) createOfficeDocument(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, ok := s.officeReady(w, r, sess.User); !ok {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Format   string `json:"format"`
		FolderID string `json:"folderId"`
	}
	if !s.decodeBody(w, r, &body, 32<<10) {
		return
	}
	format, ok := office.LookupExt(body.Format)
	if !ok || !format.NewDocument {
		writeError(w, http.StatusBadRequest, "OFFICE_FORMAT", "Choose a Word, Excel or PowerPoint document")
		return
	}
	name := office.NameWithExtension(safeFileName(body.Name), format.Ext)
	if len(name) > 180 {
		suffix := "." + format.Ext
		name = truncate(strings.TrimSuffix(name, suffix), 180-len(suffix)) + suffix
	}
	if name == "" || name == "."+format.Ext {
		writeError(w, http.StatusBadRequest, "OFFICE_NAME", "Give the document a name")
		return
	}
	folderID := strings.TrimSpace(body.FolderID)
	folderPath, _, err := s.folderPathFor(r.Context(), sess.User.ID, folderID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_FOLDER", err.Error())
		return
	}
	data, err := office.Template(format.Ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TEMPLATE", "The blank document template is unavailable")
		return
	}
	author := strings.TrimSpace(sess.User.Name)
	if author == "" {
		author = sess.User.Email
	}
	data, err = office.SetDocumentProperties(data, author, strings.TrimSuffix(name, "."+format.Ext))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_PROPERTIES", "Could not prepare the document properties")
		return
	}
	if err := s.enforceStorageQuota(r.Context(), sess.User, int64(len(data))); err != nil {
		writeError(w, http.StatusInsufficientStorage, "STORAGE_QUOTA", err.Error())
		return
	}
	storageKey, checksum, err := s.officeStoreBytesForTenant(r.Context(), sess.User.TenantID, data, format.Ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_STORAGE", "Could not store the new document")
		return
	}
	fileID := newID("file")
	documentID := newID("odoc")
	currentKey := office.NewDocumentKey(documentID, 1)
	path := folderPath
	if path == "" {
		path = "Files"
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not create the document")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path, folder_id, description,
		                   size_bytes, storage_key, attachment_id, external_url, previewable, checksum, metadata)
		VALUES ($1,$2,$3,'a8m','',$4,$5,$6,NULLIF($7,''),$8,$9,$10,NULL,'',false,$11,'{}')
	`, fileID, sess.User.TenantID, sess.User.ID, name, format.MIME, truncate(path, 160), folderID,
		"Created in Alt8Mail Office.", len(data), storageKey, checksum); err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not create the document")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO office_documents (id, tenant_id, user_id, file_id, format, document_type, current_version, current_key)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7)
	`, documentID, sess.User.TenantID, sess.User.ID, fileID, format.Ext, format.DocumentType, currentKey); err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not create the document")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO office_document_versions (id, document_id, version, storage_key, size_bytes, checksum, source, created_by)
		VALUES ($1,$2,1,$3,$4,$5,'template',$6)
	`, newID("ofv"), documentID, storageKey, len(data), checksum, sess.User.ID); err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not create the document version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not create the document")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.created", name, "Created in Alt8Mail Office")
	file, _ := s.loadFile(r.Context(), sess.User, fileID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"file": file,
		"document": officeDocumentPayload(officeDocumentRecord{
			ID: documentID, TenantID: sess.User.TenantID, UserID: sess.User.ID, FileID: fileID,
			Format: format.Ext, DocumentType: format.DocumentType, CurrentVersion: 1, CurrentKey: currentKey,
		}),
	})
}

func (s *Server) openOfficeDocument(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	runtime, ok := s.officeReady(w, r, sess.User)
	if !ok {
		return
	}
	file, err := s.loadAccessibleFileContent(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if file.ProviderID != "a8m" {
		writeError(w, http.StatusBadRequest, "OFFICE_PROVIDER", "Office editing is available for files in Alt8Mail Drive")
		return
	}
	format, ok := office.Lookup(file.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "OFFICE_FORMAT", "This file type is not supported by Office")
		return
	}
	document, err := s.ensureOfficeDocument(r.Context(), file.OwnerID, file.TenantID, file, format)
	if err != nil {
		var quotaErr storageQuotaError
		if errors.As(err, &quotaErr) {
			writeError(w, http.StatusInsufficientStorage, "STORAGE_QUOTA", quotaErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "OFFICE", err.Error())
		return
	}
	base := s.officePublicBase()
	if base == "" {
		writeError(w, http.StatusConflict, "OFFICE_URL", "The webmail public URL is not configured")
		return
	}
	theme := "theme-light"
	var body struct {
		Theme string `json:"theme"`
	}
	if r.ContentLength > 0 {
		if !s.decodeBody(w, r, &body, 4096) {
			return
		}
		if body.Theme == "theme-dark" || body.Theme == "theme-classic-light" {
			theme = body.Theme
		}
	}
	now := time.Now()
	contentToken, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindContent, FileID: file.ID, UserID: file.OwnerID,
		TenantID: file.TenantID, ExpiresAt: now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the document URL")
		return
	}
	callbackToken, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindCallback, DocumentID: document.ID, Key: document.CurrentKey,
		UserID: file.OwnerID, TenantID: file.TenantID, EditorUserID: sess.User.ID,
		Actor: sess.User.Email, ExpiresAt: now.Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the callback URL")
		return
	}
	contentURL := base + "/api/office/content?token=" + url.QueryEscape(contentToken)
	callbackURL := base + "/api/office/callback?token=" + url.QueryEscape(callbackToken)
	editable := (file.Access == "owner" || file.Access == "edit") && format.Editable
	mode := "view"
	if editable {
		mode = "edit"
	}
	config, err := s.officeEditorConfig(runtime, file.Name, format, document.CurrentKey, mode, theme, sess.User.ID, sess.User.Name, contentURL, callbackURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the editor configuration")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO office_edit_sessions (id, document_id, document_key, user_id, tenant_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (document_id, document_key) DO UPDATE SET status='open', last_callback_at=now()
	`, newID("osess"), document.ID, document.CurrentKey, file.OwnerID, file.TenantID)
	accessLabel := "view"
	if editable {
		accessLabel = "edit"
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.opened", file.Name, "Opened in Office with "+accessLabel+" access")
	writeJSON(w, http.StatusOK, map[string]any{
		"config":            config,
		"documentServerUrl": runtime.PublicURL,
		"editable":          editable,
		"document":          officeDocumentPayload(document),
		"file":              s.publicFile(file.fileRecord, sess.User, nil),
		"aiEnabled":         s.officeAIEnabled(r.Context(), sess.User),
	})
}

func (s *Server) officeEditorConfig(runtime officeRuntime, title string, format office.Format, documentKey, mode, theme, userID, userName, contentURL, callbackURL string) (map[string]any, error) {
	editable := mode == "edit" && format.Editable
	config := map[string]any{
		"document": map[string]any{
			"fileType": format.Ext,
			"key":      documentKey,
			"title":    title,
			"url":      contentURL,
			"permissions": map[string]any{
				"edit": editable, "download": true, "print": true,
				"review": false, "comment": editable, "fillForms": editable,
			},
		},
		"documentType": format.DocumentType,
		"editorConfig": map[string]any{
			"mode":        mode,
			"coEditing":   map[string]any{"mode": "fast", "change": editable},
			"lang":        "en",
			"callbackUrl": callbackURL,
			"user":        map[string]any{"id": userID, "name": userName},
			"customization": map[string]any{
				"about":             false,
				"autosave":          true,
				"compactHeader":     true,
				"feedback":          false,
				"forcesave":         true,
				"help":              false,
				"hideRightMenu":     true,
				"uiTheme":           theme,
				"zoom":              100,
				"compactToolbar":    false,
				"toolbarNoTabs":     false,
				"showReviewChanges": false,
			},
		},
		"type":   "desktop",
		"width":  "100%",
		"height": "100%",
	}
	token, err := office.SignJWT(runtime.Secret, config, time.Hour)
	if err != nil {
		return nil, err
	}
	config["token"] = token
	return config, nil
}

func (s *Server) ensureOfficeDocument(ctx context.Context, userID, tenantID string, file contentFile, format office.Format) (officeDocumentRecord, error) {
	if existing, err := s.loadOfficeDocumentByFile(ctx, userID, file.ID); err == nil {
		return existing, nil
	}
	storageKey := strings.TrimSpace(file.StorageKey)
	checksum := strings.TrimSpace(file.Checksum)
	sizeBytes := file.SizeBytes
	ownsStorage := false
	if storageKey != "" {
		// Reuse the existing Cabinet blob for the initial version instead of
		// copying it. This avoids double-counting the document against the
		// Cabinet quota and keeps the first edit cheap.
		if _, ok := s.officeStoragePath(storageKey); !ok {
			return officeDocumentRecord{}, fmt.Errorf("the current file content is unavailable")
		}
		if checksum == "" || sizeBytes <= 0 {
			data, readErr := s.officeReadStorage(storageKey)
			if readErr != nil {
				return officeDocumentRecord{}, fmt.Errorf("the current file content is unavailable")
			}
			if checksum == "" {
				sum := sha256.Sum256(data)
				checksum = hex.EncodeToString(sum[:])
			}
			if sizeBytes <= 0 {
				sizeBytes = int64(len(data))
			}
		}
	} else {
		data, readErr := s.officeReadStorage(file.AttachmentID)
		if readErr != nil {
			return officeDocumentRecord{}, fmt.Errorf("the current file content is unavailable")
		}
		var storeErr error
		storageKey, checksum, storeErr = s.officeStoreBytesForTenant(ctx, tenantID, data, format.Ext)
		if storeErr != nil {
			return officeDocumentRecord{}, fmt.Errorf("could not prepare the initial document version")
		}
		ownsStorage = true
		sizeBytes = int64(len(data))
		if owner, ownerErr := s.userByID(ctx, userID); ownerErr == nil {
			if quotaErr := s.enforceStorageQuota(ctx, owner, sizeBytes); quotaErr != nil {
				if ownsStorage {
					s.officeRemoveStorage(storageKey)
				}
				return officeDocumentRecord{}, quotaErr
			}
		}
	}
	documentID := newID("odoc")
	currentKey := office.NewDocumentKey(documentID, 1)
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		if ownsStorage {
			s.officeRemoveStorage(storageKey)
		}
		return officeDocumentRecord{}, fmt.Errorf("could not start the document session")
	}
	defer tx.Rollback(ctx)
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO office_documents (id, tenant_id, user_id, file_id, format, document_type, current_version, current_key)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7)
		ON CONFLICT (file_id) DO NOTHING
		RETURNING id
	`, documentID, tenantID, userID, file.ID, format.Ext, format.DocumentType, currentKey).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if ownsStorage {
			s.officeRemoveStorage(storageKey)
		}
		return s.loadOfficeDocumentByFile(ctx, userID, file.ID)
	}
	if err != nil {
		if ownsStorage {
			s.officeRemoveStorage(storageKey)
		}
		return officeDocumentRecord{}, fmt.Errorf("could not create the Office document")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO office_document_versions (id, document_id, version, storage_key, size_bytes, checksum, source, created_by)
		VALUES ($1,$2,1,$3,$4,$5,'initial',$6)
	`, newID("ofv"), documentID, storageKey, sizeBytes, checksum, userID); err != nil {
		if ownsStorage {
			s.officeRemoveStorage(storageKey)
		}
		return officeDocumentRecord{}, fmt.Errorf("could not create the initial document version")
	}
	if err := tx.Commit(ctx); err != nil {
		if ownsStorage {
			s.officeRemoveStorage(storageKey)
		}
		return officeDocumentRecord{}, fmt.Errorf("could not create the Office document")
	}
	return officeDocumentRecord{
		ID: documentID, TenantID: tenantID, UserID: userID, FileID: file.ID,
		Format: format.Ext, DocumentType: format.DocumentType, CurrentVersion: 1, CurrentKey: currentKey,
	}, nil
}

func (s *Server) loadOfficeDocumentByFile(ctx context.Context, userID, fileID string) (officeDocumentRecord, error) {
	return s.scanOfficeDocument(ctx, `
		SELECT id, tenant_id::text, user_id::text, file_id, format, document_type, current_version, current_key
		FROM office_documents WHERE file_id=$1 AND user_id=$2
	`, fileID, userID)
}

func (s *Server) loadOfficeDocumentByFileID(ctx context.Context, fileID string) (officeDocumentRecord, error) {
	return s.scanOfficeDocument(ctx, `
		SELECT id, tenant_id::text, user_id::text, file_id, format, document_type, current_version, current_key
		FROM office_documents WHERE file_id=$1
	`, fileID)
}

func (s *Server) loadOfficeDocumentByID(ctx context.Context, documentID string) (officeDocumentRecord, error) {
	return s.scanOfficeDocument(ctx, `
		SELECT id, tenant_id::text, user_id::text, file_id, format, document_type, current_version, current_key
		FROM office_documents WHERE id=$1
	`, documentID)
}

func (s *Server) scanOfficeDocument(ctx context.Context, query string, args ...any) (officeDocumentRecord, error) {
	var document officeDocumentRecord
	err := s.store.DB.QueryRow(ctx, query, args...).Scan(&document.ID, &document.TenantID, &document.UserID,
		&document.FileID, &document.Format, &document.DocumentType, &document.CurrentVersion, &document.CurrentKey)
	return document, err
}

func officeDocumentPayload(document officeDocumentRecord) map[string]any {
	format, _ := office.LookupExt(document.Format)
	return map[string]any{
		"id": document.ID, "fileId": document.FileID, "format": document.Format,
		"documentType": document.DocumentType, "currentVersion": document.CurrentVersion,
		"editable": format.Editable,
	}
}

func (s *Server) listOfficeVersions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	fileID := r.PathValue("id")
	access, err := s.fileAccessForUser(r.Context(), sess.User, fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	document, err := s.loadOfficeDocumentByFileID(r.Context(), fileID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"document": nil, "versions": []any{}, "access": access.Access})
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, version, size_bytes, checksum, source, created_at,
		       dlp_classification, dlp_warnings, dlp_matches,
		       review_status, review_note, COALESCE(reviewed_by_email::text,''), reviewed_at
		FROM office_document_versions WHERE document_id=$1 ORDER BY version DESC LIMIT $2
	`, document.ID, officeVersionLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSIONS", "Could not load document versions")
		return
	}
	defer rows.Close()
	versions := []map[string]any{}
	for rows.Next() {
		var version officeVersionRecord
		if rows.Scan(&version.ID, &version.Version, &version.SizeBytes, &version.Checksum, &version.Source, &version.CreatedAt,
			&version.DLPClassification, &version.DLPWarnings, &version.DLPMatches,
			&version.ReviewStatus, &version.ReviewNote, &version.ReviewedByEmail, &version.ReviewedAt) != nil {
			continue
		}
		warnings := decodeJSONAny(version.DLPWarnings, []any{})
		matches := decodeJSONAny(version.DLPMatches, []any{})
		if !access.IsOwner() {
			// Keep the classification visible to collaborators but not the
			// organization's rule names or matched terms.
			warnings = []any{}
			matches = []any{}
		}
		reviewedAt := ""
		if version.ReviewedAt != nil {
			reviewedAt = version.ReviewedAt.UTC().Format(time.RFC3339)
		}
		versions = append(versions, map[string]any{
			"id": version.ID, "version": version.Version, "sizeBytes": version.SizeBytes,
			"source": version.Source, "createdAt": version.CreatedAt.UTC().Format(time.RFC3339),
			"current":           version.Version == document.CurrentVersion,
			"dlpClassification": version.DLPClassification,
			"dlpWarnings":       warnings,
			"dlpMatches":        matches,
			"reviewStatus":      version.ReviewStatus,
			"reviewNote":        version.ReviewNote,
			"reviewedBy":        version.ReviewedByEmail,
			"reviewedAt":        reviewedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": officeDocumentPayload(document), "versions": versions, "access": access.Access})
}

func (s *Server) compareOfficeVersions(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	fileID := r.PathValue("id")
	from, fromErr := strconv.Atoi(r.URL.Query().Get("from"))
	to, toErr := strconv.Atoi(r.URL.Query().Get("to"))
	if fromErr != nil || toErr != nil || from < 1 || to < 1 {
		writeError(w, http.StatusBadRequest, "OFFICE_COMPARE", "Choose two document versions to compare")
		return
	}
	if from == to {
		writeError(w, http.StatusBadRequest, "OFFICE_COMPARE", "Choose two different versions")
		return
	}
	if _, err := s.fileAccessForUser(r.Context(), sess.User, fileID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	document, err := s.loadOfficeDocumentByFileID(r.Context(), fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_DOCUMENT", "Office document not found")
		return
	}
	var name string
	if err := s.store.DB.QueryRow(r.Context(), `SELECT name FROM files WHERE id=$1`, fileID).Scan(&name); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	loadVersion := func(number int) (officeVersionRecord, error) {
		var record officeVersionRecord
		err := s.store.DB.QueryRow(r.Context(), `
			SELECT id, version, storage_key, size_bytes, checksum, source, created_at,
			       dlp_classification, dlp_warnings, dlp_matches
			FROM office_document_versions WHERE document_id=$1 AND version=$2
		`, document.ID, number).Scan(&record.ID, &record.Version, &record.StorageKey, &record.SizeBytes,
			&record.Checksum, &record.Source, &record.CreatedAt, &record.DLPClassification,
			&record.DLPWarnings, &record.DLPMatches)
		return record, err
	}
	fromVersion, err := loadVersion(from)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version not found")
		return
	}
	toVersion, err := loadVersion(to)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version not found")
		return
	}
	readVersion := func(record officeVersionRecord) (string, error) {
		data, err := s.officeReadStorage(record.StorageKey)
		if err != nil || len(data) > officeDocumentMaxBytes {
			return "", fmt.Errorf("version content is unavailable")
		}
		return office.ExtractText(name, data)
	}
	before, err := readVersion(fromVersion)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "OFFICE_COMPARE", "This file type does not expose text for comparison")
		return
	}
	after, err := readVersion(toVersion)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "OFFICE_COMPARE", "This file type does not expose text for comparison")
		return
	}
	lines, truncated := officeLineDiff(before, after)
	s.recordWorkspaceAudit(r.Context(), sess, "office.versions_compared", name,
		fmt.Sprintf("Compared Office versions %d and %d", from, to))
	versionPayload := func(record officeVersionRecord) map[string]any {
		return map[string]any{
			"version": record.Version, "source": record.Source, "sizeBytes": record.SizeBytes,
			"createdAt":         record.CreatedAt.UTC().Format(time.RFC3339),
			"dlpClassification": record.DLPClassification,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": versionPayload(fromVersion), "to": versionPayload(toVersion),
		"lines": lines, "truncated": truncated,
	})
}

func (s *Server) downloadOfficeVersion(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	versionNumber, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || versionNumber < 1 {
		writeError(w, http.StatusBadRequest, "OFFICE_VERSION", "Choose a valid document version")
		return
	}
	if _, err := s.fileAccessForUser(r.Context(), sess.User, r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	var storageKey, format, name, classification string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT v.storage_key, d.format, f.name, COALESCE(f.metadata->>'dlpClassification','')
		FROM office_documents d
		JOIN office_document_versions v ON v.document_id=d.id
		JOIN files f ON f.id=d.file_id
		WHERE d.file_id=$1 AND v.version=$2
	`, r.PathValue("id"), versionNumber).Scan(&storageKey, &format, &name, &classification)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version not found")
		return
	}
	fileFormat, _ := office.LookupExt(format)
	displayName := officeVersionFileName(name, versionNumber, format)
	s.recordWorkspaceAudit(r.Context(), sess, "office.version_downloaded", name,
		fmt.Sprintf("Downloaded Office version %d", versionNumber))
	w.Header().Set("Content-Type", fileFormat.MIME)
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(displayName))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.tenantPolicyFor(r.Context(), sess.User.TenantID).Files.WatermarkDownloads && dlpClassificationRank(canonicalDLPClassification(classification)) >= dlpClassificationRank("Confidential") {
		w.Header().Set("X-A8M-Watermark", canonicalDLPClassification(classification)+" · "+sess.User.Email+" · "+time.Now().UTC().Format(time.RFC3339)+" · Alt8Mail")
	}
	data, err := s.officeReadStorage(storageKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version content is unavailable")
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}

func officeVersionFileName(name string, version int, ext string) string {
	base := name
	if current := filepath.Ext(base); current != "" {
		base = strings.TrimSuffix(base, current)
	}
	return fmt.Sprintf("%s (v%d).%s", strings.TrimSpace(base), version, ext)
}

func (s *Server) restoreOfficeVersion(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, allowed := s.officeUserAllowed(r.Context(), sess.User); !allowed {
		writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Office is not enabled for your account by organization policy")
		return
	}
	versionNumber, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || versionNumber < 1 {
		writeError(w, http.StatusBadRequest, "OFFICE_VERSION", "Choose a valid document version")
		return
	}
	access, err := s.fileAccessForUser(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if !access.CanEdit() {
		writeError(w, http.StatusForbidden, "OFFICE_PERMISSION", "You have view-only access to this document")
		return
	}
	document, err := s.loadOfficeDocumentByFileID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_DOCUMENT", "Office document not found")
		return
	}
	if document.DocumentType == "pdf" {
		writeError(w, http.StatusBadRequest, "OFFICE_VERSION", "PDF versions cannot be restored as editable documents")
		return
	}
	var storageKey, name string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT v.storage_key, f.name
		FROM office_document_versions v
		JOIN office_documents d ON d.id=v.document_id
		JOIN files f ON f.id=d.file_id
		WHERE v.document_id=$1 AND v.version=$2
	`, document.ID, versionNumber).Scan(&storageKey, &name)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version not found")
		return
	}
	data, err := s.officeReadStorage(storageKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version content is unavailable")
		return
	}
	officePolicy := s.tenantPolicyFor(r.Context(), document.TenantID).Office
	prunedBytes := s.officeRetentionPruneBytes(r.Context(), document.ID, officePolicy.VersionRetention)
	addBytes := int64(len(data)) - prunedBytes
	if addBytes < 0 {
		addBytes = 0
	}
	if owner, ownerErr := s.userByID(r.Context(), access.OwnerID); ownerErr == nil {
		if quotaErr := s.enforceStorageQuota(r.Context(), owner, addBytes); quotaErr != nil {
			s.recordWorkspaceAudit(r.Context(), sess, "office.quota_blocked", name,
				"Version restore blocked by the Cabinet quota")
			writeError(w, http.StatusInsufficientStorage, "STORAGE_QUOTA", quotaErr.Error())
			return
		}
	}
	newKey, checksum, err := s.officeStoreBytesForTenant(r.Context(), document.TenantID, data, document.Format)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_STORAGE", "Could not restore the version")
		return
	}
	var prunedKeys []string
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	defer tx.Rollback(r.Context())
	var currentVersion int
	if err := tx.QueryRow(r.Context(), `SELECT current_version FROM office_documents WHERE id=$1 FOR UPDATE`, document.ID).Scan(&currentVersion); err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	nextVersion := currentVersion + 1
	newDocumentKey := office.NewDocumentKey(document.ID, nextVersion)
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO office_document_versions (id, document_id, version, storage_key, size_bytes, checksum, source, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,'restore',$7)
	`, newID("ofv"), document.ID, nextVersion, newKey, len(data), checksum, sess.User.ID); err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE office_documents SET current_version=$2, current_key=$3, last_saved_at=now(), updated_at=now() WHERE id=$1
	`, document.ID, nextVersion, newDocumentKey); err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	if _, err := tx.Exec(r.Context(), `
		UPDATE files SET storage_key=$1, size_bytes=$2, checksum=$3, version=$4, updated_at=now() WHERE id=$5
	`, newKey, len(data), checksum, nextVersion, document.FileID); err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	prunedKeys, err = officePruneVersions(r.Context(), tx, document.ID, officePolicy.VersionRetention)
	if err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not apply version retention")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.officeRemoveStorage(newKey)
		writeError(w, http.StatusInternalServerError, "OFFICE_VERSION", "Could not restore the version")
		return
	}
	for _, key := range prunedKeys {
		s.officeRemoveStorage(key)
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.version_restored", name,
		fmt.Sprintf("Restored version %d as version %d", versionNumber, nextVersion))
	file, _ := s.loadFile(r.Context(), sess.User, document.FileID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": nextVersion, "file": file})
}

func (s *Server) reviewOfficeVersion(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, allowed := s.officeUserAllowed(r.Context(), sess.User); !allowed {
		writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Office is not enabled for your account by organization policy")
		return
	}
	fileID := r.PathValue("id")
	versionNumber, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || versionNumber < 1 {
		writeError(w, http.StatusBadRequest, "OFFICE_VERSION", "Choose a valid document version")
		return
	}
	access, err := s.fileAccessForUser(r.Context(), sess.User, fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if !access.CanEdit() {
		writeError(w, http.StatusForbidden, "OFFICE_PERMISSION", "You have view-only access to this document")
		return
	}
	document, err := s.loadOfficeDocumentByFileID(r.Context(), fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_DOCUMENT", "Office document not found")
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !s.decodeBody(w, r, &body, 16<<10) {
		return
	}
	body.Status = strings.ToLower(strings.TrimSpace(body.Status))
	if body.Status != "approved" && body.Status != "changes_requested" {
		writeError(w, http.StatusBadRequest, "OFFICE_REVIEW", "Choose approved or changes requested")
		return
	}
	body.Note = truncate(strings.TrimSpace(body.Note), 1000)
	var reviewedAt time.Time
	err = s.store.DB.QueryRow(r.Context(), `
		UPDATE office_document_versions
		SET review_status=$3, review_note=$4, reviewed_by=$5, reviewed_by_email=$6, reviewed_at=now()
		WHERE document_id=$1 AND version=$2
		RETURNING reviewed_at
	`, document.ID, versionNumber, body.Status, body.Note, sess.User.ID, sess.User.Email).Scan(&reviewedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "OFFICE_VERSION", "Version not found")
		return
	}
	var name string
	_ = s.store.DB.QueryRow(r.Context(), `SELECT name FROM files WHERE id=$1`, fileID).Scan(&name)
	label := "Approved"
	if body.Status == "changes_requested" {
		label = "Changes requested"
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.version_reviewed", firstNonEmpty(name, fileID),
		fmt.Sprintf("Version %d · %s", versionNumber, label))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": map[string]any{
		"version": versionNumber, "status": body.Status, "note": body.Note,
		"reviewedBy": sess.User.Email, "reviewedAt": reviewedAt.UTC().Format(time.RFC3339),
	}})
}

func (s *Server) convertOfficeDocument(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	runtime, ok := s.officeReady(w, r, sess.User)
	if !ok {
		return
	}
	file, err := s.loadAccessibleFileContent(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "File not found")
		return
	}
	if file.ProviderID != "a8m" {
		writeError(w, http.StatusBadRequest, "OFFICE_PROVIDER", "PDF conversion is available for files in Alt8Mail Drive")
		return
	}
	format, ok := office.Lookup(file.Name)
	if !ok || format.DocumentType == "pdf" {
		writeError(w, http.StatusBadRequest, "OFFICE_FORMAT", "This file cannot be converted to PDF")
		return
	}
	base := s.officePublicBase()
	if base == "" {
		writeError(w, http.StatusConflict, "OFFICE_URL", "The webmail public URL is not configured")
		return
	}
	contentToken, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindContent, FileID: file.ID, UserID: file.OwnerID,
		TenantID: file.TenantID, ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the document URL")
		return
	}
	contentURL := base + "/api/office/content?token=" + url.QueryEscape(contentToken)
	pdf, err := s.officeConvertToPDF(r.Context(), runtime, format, file.Name, contentURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "OFFICE_CONVERT", err.Error())
		return
	}
	if err := s.enforceStorageQuota(r.Context(), sess.User, int64(len(pdf))); err != nil {
		writeError(w, http.StatusInsufficientStorage, "STORAGE_QUOTA", err.Error())
		return
	}
	storageKey, checksum, err := s.officeStoreBytesForTenant(r.Context(), sess.User.TenantID, pdf, "pdf")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_STORAGE", "Could not store the converted PDF")
		return
	}
	outFolderID := ""
	path := "Files"
	if file.Access == "owner" {
		outFolderID = file.FolderID
		folderPath, _, _ := s.folderPathFor(r.Context(), sess.User.ID, outFolderID)
		path = folderPath
		if path == "" {
			path = firstNonEmpty(file.Path, "Files")
		}
	}
	baseName := file.Name
	if ext := filepath.Ext(baseName); ext != "" {
		baseName = strings.TrimSuffix(baseName, ext)
	}
	name := s.officeUniqueFileName(r.Context(), sess.User.ID, outFolderID, baseName+".pdf")
	fileID := newID("file")
	if _, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path, folder_id, description,
		                   size_bytes, storage_key, attachment_id, external_url, previewable, checksum, metadata)
		VALUES ($1,$2,$3,'a8m','',$4,'application/pdf',$5,NULLIF($6,''),$7,$8,$9,NULL,'',true,$10,'{}')
	`, fileID, sess.User.TenantID, sess.User.ID, name, truncate(path, 160), outFolderID,
		"PDF conversion of "+file.Name+".", len(pdf), storageKey, checksum); err != nil {
		s.officeRemoveStorage(storageKey)
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not save the converted PDF")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.converted", name, "Converted "+file.Name+" to PDF")
	converted, _ := s.loadFile(r.Context(), sess.User, fileID)
	writeJSON(w, http.StatusCreated, map[string]any{"file": converted})
}

func (s *Server) officeUniqueFileName(ctx context.Context, userID, folderID, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 1; index <= 50; index++ {
		candidate := name
		if index > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", base, index, ext)
		}
		var exists bool
		err := s.store.DB.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM files WHERE user_id=$1 AND COALESCE(folder_id,'')=$2 AND lower(name)=lower($3))
		`, userID, folderID, candidate).Scan(&exists)
		if err == nil && !exists {
			return candidate
		}
	}
	return fmt.Sprintf("%s (%d)%s", base, time.Now().Unix(), ext)
}

func (s *Server) officeStoragePath(key string) (string, bool) {
	if key == "" || strings.ContainsAny(key, `/\`) || key == "." || key == ".." {
		return "", false
	}
	return filepath.Join(s.cfg.UploadDir, key), true
}

func (s *Server) officeStoreBytes(data []byte, ext string) (string, string, error) {
	return s.officeStoreBytesForTenant(context.Background(), "", data, ext)
}

func (s *Server) officeStoreBytesForTenant(ctx context.Context, tenantID string, data []byte, ext string) (string, string, error) {
	if err := os.MkdirAll(s.cfg.UploadDir, 0o750); err != nil {
		return "", "", err
	}
	key := newID("ofv") + "." + strings.TrimPrefix(ext, ".")
	path, ok := s.officeStoragePath(key)
	if !ok {
		return "", "", fmt.Errorf("invalid storage key")
	}
	stored := data
	if tenantID != "" {
		sealed, _, err := s.sealTenantBlob(ctx, tenantID, data)
		if err != nil {
			return "", "", err
		}
		stored = sealed
	}
	if err := os.WriteFile(path, stored, 0o640); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	return key, hex.EncodeToString(sum[:]), nil
}

func (s *Server) officeReadStorage(key string) ([]byte, error) {
	path, ok := s.officeStoragePath(key)
	if !ok {
		return nil, fmt.Errorf("invalid storage key")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return s.openTenantBlob(data)
}

func (s *Server) officeRemoveStorage(key string) {
	if path, ok := s.officeStoragePath(key); ok {
		_ = os.Remove(path)
	}
}
