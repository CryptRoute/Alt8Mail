// Package officecollabora implements the WOPI host endpoints Collabora Online
// calls. It is deliberately independent of the Alt8Mail database: the httpapi
// package supplies a Service that resolves files, permissions and saves.
package officecollabora

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

// ConflictError means the editor's base timestamp is older than the current
// file. Collabora expects HTTP 409 with {"COOLStatusCode":1010}.
type ConflictError struct {
	Current time.Time
}

func (e *ConflictError) Error() string { return "officecollabora: stale base timestamp" }

// SaveError carries an HTTP status and a user-facing message back to the
// editor. Collabora renders the message in its save-error dialog.
type SaveError struct {
	Status  int
	Code    string
	Message string
}

func (e *SaveError) Error() string { return e.Message }

type FileInfo struct {
	FileID       string
	BaseFileName string
	Size         int64
	Version      string
	LastModified time.Time
	UserID       string
	UserName     string
	CanWrite     bool
}

type FileData struct {
	Data         []byte
	ContentType  string
	Version      string
	LastModified time.Time
}

type SaveResult struct {
	Version      string
	LastModified time.Time
}

// Service is implemented by httpapi.collaboraService.
type Service interface {
	CheckFileInfo(ctx context.Context, claims office.URLToken) (FileInfo, error)
	GetFile(ctx context.Context, claims office.URLToken) (FileData, error)
	SaveFile(ctx context.Context, claims office.URLToken, data []byte, base time.Time) (SaveResult, error)
}

type Handler struct {
	Secret            string
	Service           Service
	PostMessageOrigin string
	MaxBytes          int64
}

// CheckFileInfo: GET /api/office/wopi/files/{id}
func (h *Handler) CheckFileInfo(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	info, err := h.Service.CheckFileInfo(r.Context(), claims)
	if err != nil {
		h.serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"BaseFileName":            info.BaseFileName,
		"Size":                    info.Size,
		"Version":                 info.Version,
		"LastModifiedTime":        info.LastModified.UTC().Format(time.RFC3339Nano),
		"UserId":                  info.UserID,
		"UserFriendlyName":        info.UserName,
		"UserCanWrite":            info.CanWrite,
		"SupportsUpdate":          true,
		"SupportsLocks":           false,
		"UserCanNotWriteRelative": true,
		"PostMessageOrigin":       h.PostMessageOrigin,
	})
}

// GetFile: GET /api/office/wopi/files/{id}/contents
func (h *Handler) GetFile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	file, err := h.Service.GetFile(r.Context(), claims)
	if err != nil {
		h.serviceError(w, err)
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("X-WOPI-ItemVersion", file.Version)
	http.ServeContent(w, r, claims.FileID, file.LastModified, bytes.NewReader(file.Data))
}

// PutFile: POST /api/office/wopi/files/{id}/contents
func (h *Handler) PutFile(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !claims.CanWrite {
		http.Error(w, "read-only token", http.StatusForbidden)
		return
	}
	if override := r.Header.Get("X-WOPI-Override"); override != "" && override != "PUT" {
		http.Error(w, "unsupported WOPI override", http.StatusNotImplemented)
		return
	}

	var base time.Time
	if ts := r.Header.Get("X-COOL-WOPI-Timestamp"); ts != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			base = parsed
		}
	}

	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 100 << 20
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}

	result, err := h.Service.SaveFile(r.Context(), claims, body, base)
	if err != nil {
		h.serviceError(w, err)
		return
	}

	w.Header().Set("X-WOPI-ItemVersion", result.Version)
	writeJSON(w, http.StatusOK, map[string]string{
		"LastModifiedTime": result.LastModified.UTC().Format(time.RFC3339Nano),
	})
}

// Lock: POST /api/office/wopi/files/{id}
// Collabora is timestamp-based (SupportsLocks is false), so this is not used.
func (h *Handler) Lock(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	http.Error(w, "WOPI locking is not supported; this host uses timestamps", http.StatusNotImplemented)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (office.URLToken, bool) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "missing access token", http.StatusUnauthorized)
		return office.URLToken{}, false
	}
	claims, err := office.VerifyURLToken(h.Secret, token, time.Now())
	if err != nil || claims.Kind != office.TokenKindWOPI {
		http.Error(w, "invalid access token", http.StatusUnauthorized)
		return office.URLToken{}, false
	}
	if id := r.PathValue("id"); id != "" && claims.FileID != id {
		http.Error(w, "token does not cover this file", http.StatusForbidden)
		return office.URLToken{}, false
	}
	return claims, true
}

func (h *Handler) serviceError(w http.ResponseWriter, err error) {
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		current := conflict.Current.UTC().Format(time.RFC3339Nano)
		w.Header().Set("X-COOL-WOPI-Timestamp", current)
		writeJSON(w, http.StatusConflict, map[string]int{"COOLStatusCode": 1010})
		return
	}
	var saveErr *SaveError
	if errors.As(err, &saveErr) {
		http.Error(w, saveErr.Message, saveErr.Status)
		return
	}
	http.Error(w, "the Office storage request failed", http.StatusInternalServerError)
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("access_token")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ContentType keeps the OOXML types stable across hosts where the system MIME
// database may be incomplete.
func ContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".pdf":
		return "application/pdf"
	case ".odt":
		return "application/vnd.oasis.opendocument.text"
	case ".ods":
		return "application/vnd.oasis.opendocument.spreadsheet"
	case ".odp":
		return "application/vnd.oasis.opendocument.presentation"
	default:
		if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
