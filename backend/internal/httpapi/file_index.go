package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

type fileTextIndexResult struct {
	FileID  string `json:"fileId"`
	Source  string `json:"source"`
	OCRUsed bool   `json:"ocrUsed"`
	Chars   int    `json:"chars"`
}

func (s *Server) readFileForIndex(ctx context.Context, user userRecord, fileID string) (contentFile, []byte, error) {
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
		return file, data, err
	case "s3":
		data, err := s.s3GetObject(ctx, user.TenantID, file.StorageKey)
		return file, data, err
	default:
		data, _, err := s.downloadFromOAuthProvider(ctx, user.ID, file.ProviderID, file.ExternalID)
		return file, data, err
	}
}

func extractFileText(name string, data []byte) (string, bool, error) {
	if text, err := office.ExtractText(name, data); err == nil && strings.TrimSpace(text) != "" {
		return text, false, nil
	}
	return "", false, nil
}

// runOCR executes an operator-configured OCR sidecar. The command is not user
// controlled; a {} placeholder is replaced with the temporary file path.
func runOCR(ctx context.Context, ext string, data []byte) (string, error) {
	command := strings.Fields(strings.TrimSpace(os.Getenv("A8M_OCR_COMMAND")))
	if len(command) == 0 {
		return "", fmt.Errorf("OCR is not configured")
	}
	suffix := "." + strings.TrimPrefix(ext, ".")
	if suffix == "." {
		suffix = ".bin"
	}
	temp, err := os.CreateTemp("", "a8m-ocr-*"+suffix)
	if err != nil {
		return "", err
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	args := []string{}
	for _, arg := range command[1:] {
		args = append(args, strings.ReplaceAll(arg, "{}", path))
	}
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, command[0], args...).Output()
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "", fmt.Errorf("OCR returned no text")
	}
	return truncate(text, 40000), nil
}

func (s *Server) indexFileText(ctx context.Context, user userRecord, fileID string) (fileTextIndexResult, error) {
	result := fileTextIndexResult{FileID: fileID, Source: "unavailable"}
	file, data, err := s.readFileForIndex(ctx, user, fileID)
	if err != nil {
		return result, err
	}
	text, ocrUsed, _ := extractFileText(file.Name, data)
	if strings.TrimSpace(text) != "" {
		result.Source = "text"
		result.OCRUsed = ocrUsed
	} else if isOCRReadable(file.MIME, file.Name) {
		if ocrText, err := runOCR(ctx, strings.TrimPrefix(filepath.Ext(file.Name), "."), data); err == nil {
			text = ocrText
			result.Source = "ocr"
			result.OCRUsed = true
		}
	}
	result.Chars = len([]rune(text))
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO public.file_text_index (file_id, tenant_id, user_id, content, source, ocr_used, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (file_id) DO UPDATE SET
		  content=EXCLUDED.content, source=EXCLUDED.source, ocr_used=EXCLUDED.ocr_used, updated_at=now()
	`, fileID, user.TenantID, user.ID, text, result.Source, result.OCRUsed)
	if err != nil {
		return result, err
	}
	return result, nil
}

func isOCRReadable(mime, name string) bool {
	mime = strings.ToLower(mime)
	if strings.HasPrefix(mime, "image/") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(name), ".pdf")
}

func (s *Server) indexFileContent(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	result, err := s.indexFileText(r.Context(), sess.User, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "FILE_INDEX", "This file could not be indexed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) searchIndexedFiles(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 {
		writeError(w, http.StatusBadRequest, "FILE_SEARCH", "Type a longer search query")
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT f.id, f.name, f.mime, f.path, i.content, i.source, i.ocr_used, i.updated_at
		FROM public.file_text_index i JOIN public.files f ON f.id=i.file_id
		WHERE i.user_id=$1
		  AND (to_tsvector('simple', i.content) @@ websearch_to_tsquery('simple', $2)
		       OR i.content ILIKE $3 ESCAPE '\')
		ORDER BY i.updated_at DESC LIMIT 50
	`, sess.User.ID, query, likePattern(query))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FILE_SEARCH", "Could not search indexed files")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, mime, path, content, source string
		var ocrUsed bool
		var updated time.Time
		if rows.Scan(&id, &name, &mime, &path, &content, &source, &ocrUsed, &updated) == nil {
			excerpt := content
			if index := strings.Index(strings.ToLower(content), strings.ToLower(query)); index >= 0 {
				start := index - 80
				if start < 0 {
					start = 0
				}
				end := index + len(query) + 160
				if end > len(content) {
					end = len(content)
				}
				excerpt = strings.TrimSpace(content[start:end])
			} else {
				excerpt = truncate(content, 240)
			}
			items = append(items, map[string]any{
				"id": id, "fileId": id, "name": name, "mime": mime, "path": path,
				"excerpt": excerpt, "source": source, "ocrUsed": ocrUsed,
				"updatedAt": updated.UTC().Format(time.RFC3339),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "query": query})
}

func (s *Server) registerFileIndexRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/files/{id}/index", s.requireUser(s.indexFileContent))
	mux.HandleFunc("GET /api/files/search", s.requireUser(s.searchIndexedFiles))
}
