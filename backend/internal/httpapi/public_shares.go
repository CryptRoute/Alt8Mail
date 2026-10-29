package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type publicShare struct {
	contentFile
	OwnerName   string
	TenantID    string
	TenantName  string
	TenantColor string
	Access      string
	ExpiresAt   time.Time
	Recipient   string
}

func (s *Server) loadPublicShare(ctx context.Context, token string, touch bool) (publicShare, error) {
	var share publicShare
	err := s.store.DB.QueryRow(ctx, `
		SELECT f.id, f.provider_id, f.external_id, f.name, f.mime, f.path, f.description,
		       f.size_bytes, f.storage_key, COALESCE(f.attachment_id,''), f.external_url,
		       f.starred, f.locked, f.version, f.previewable, f.checksum, f.metadata,
		       f.created_at, f.updated_at, f.user_id::text,
		       u.name, u.tenant_id::text, t.name,
		       COALESCE(t.settings->'branding'->>'primaryColor', '#0f6cbd'),
		       s.access, s.expires_at, s.recipient_email::text
		FROM file_shares s
		JOIN files f ON f.id = s.file_id
		JOIN users u ON u.id = f.user_id
		JOIN tenants t ON t.id = f.tenant_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
	`, hashShareToken(token)).Scan(
		&share.ID, &share.ProviderID, &share.ExternalID, &share.Name, &share.MIME, &share.Path,
		&share.Description, &share.SizeBytes, &share.StorageKey, &share.AttachmentID,
		&share.ExternalURL, &share.Starred, &share.Locked, &share.Version, &share.Previewable,
		&share.Checksum, &share.Metadata, &share.CreatedAt, &share.UpdatedAt, &share.OwnerID,
		&share.OwnerName, &share.TenantID, &share.TenantName, &share.TenantColor,
		&share.Access, &share.ExpiresAt, &share.Recipient,
	)
	if err != nil {
		return publicShare{}, fmt.Errorf("share not found")
	}
	if touch {
		_, _ = s.store.DB.Exec(ctx, `
			UPDATE file_shares SET access_count = access_count + 1, last_accessed_at = now()
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		`, hashShareToken(token))
	}
	return share, nil
}

func (s *Server) publicShareMetadata(w http.ResponseWriter, r *http.Request) {
	share, err := s.loadPublicShare(r.Context(), r.PathValue("token"), false)
	if err != nil {
		writeError(w, http.StatusNotFound, "SHARE_NOT_FOUND", "This share link is invalid or has expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": share.Name, "mime": share.MIME, "sizeBytes": share.SizeBytes,
		"previewable": share.Previewable, "access": share.Access,
		"expiresAt": share.ExpiresAt.UTC().Format(time.RFC3339),
		"sharedBy":  share.OwnerName, "organization": share.TenantName,
		"downloadUrl": "/api/public/shares/" + r.PathValue("token") + "/content?download=1",
	})
}

func (s *Server) publicShareContent(w http.ResponseWriter, r *http.Request) {
	share, err := s.loadPublicShare(r.Context(), r.PathValue("token"), true)
	if err != nil {
		writeError(w, http.StatusNotFound, "SHARE_NOT_FOUND", "This share link is invalid or has expired")
		return
	}
	s.serveFileContent(w, r, share.contentFile, share.OwnerID)
}

var publicShareTemplate = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>{{.Name}} · {{.TenantName}}</title>
<style>
:root{color-scheme:light dark;--brand:{{.TenantColor}};--ink:#122033;--muted:#667085;--line:#e4e7ec;--surface:#fff;--bg:#f7f8fb}
@media (prefers-color-scheme:dark){:root{--ink:#eef2f7;--muted:#98a2b3;--line:#273449;--surface:#101827;--bg:#070b12}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;min-height:100vh;display:grid;place-items:center;padding:32px 18px}
.card{width:min(760px,100%);background:var(--surface);border:1px solid var(--line);border-radius:18px;box-shadow:0 18px 50px rgba(16,24,40,.08);overflow:hidden}
.head{display:flex;align-items:center;gap:10px;padding:18px 22px;border-bottom:1px solid var(--line)}
.mark{width:32px;height:32px;border-radius:9px;background:var(--brand);color:white;display:grid;place-items:center;font-weight:800}
.head b{font-size:13px}.head small{display:block;color:var(--muted);font-size:11px}
.body{padding:28px 24px}.eyebrow{color:var(--brand);font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.08em}
h1{font-size:24px;margin:8px 0 4px;line-height:1.2;word-break:break-word}.meta{color:var(--muted);font-size:12px}
.preview{margin:22px 0;border:1px solid var(--line);border-radius:12px;overflow:hidden;background:var(--bg);min-height:180px;display:grid;place-items:center;padding:18px}
.preview img{max-width:100%;max-height:420px;display:block}.preview iframe{width:100%;height:420px;border:0}.filemark{font-size:34px;font-weight:800;color:var(--brand)}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:18px}
a.btn,button{appearance:none;border:0;border-radius:9px;padding:10px 16px;background:var(--brand);color:#fff;font-weight:650;text-decoration:none;cursor:pointer;font-size:13px}
.secondary{background:transparent;color:var(--ink);border:1px solid var(--line)}
.note{margin-top:18px;color:var(--muted);font-size:11px}
.edit{margin-top:24px;padding-top:18px;border-top:1px solid var(--line)}form{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
input[type=file]{max-width:100%;color:var(--muted)}
</style>
</head>
<body><main class="card">
<header class="head"><span class="mark">M</span><div><b>{{.TenantName}}</b><small>Secure file share</small></div></header>
<div class="body">
<span class="eyebrow">Shared file</span>
<h1>{{.Name}}</h1>
<div class="meta">{{.SizeLabel}} · expiring {{.ExpiresLabel}}{{if .Recipient}} · intended for {{.Recipient}}{{end}}</div>
<div class="preview">{{if .Image}}<img src="{{.ContentURL}}" alt="{{.Name}}">{{else if .PDF}}<iframe src="{{.ContentURL}}" title="{{.Name}}"></iframe>{{else}}<span class="filemark">{{.Type}}</span>{{end}}</div>
<div class="actions"><a class="btn" href="{{.ContentURL}}?download=1">Download file</a></div>
{{if .CanEdit}}<div class="edit"><h2 style="font-size:15px;margin:0 0 10px">Replace this file</h2><form method="post" enctype="multipart/form-data" action="{{.ReplaceURL}}"><input type="file" name="file" required><button type="submit">Upload new version</button></form><p class="note">This link has edit access. The new file replaces the shared version.</p></div>{{end}}
<p class="note">Shared by {{.OwnerName}} through {{.TenantName}}. Do not forward this link if the sender asked you to keep it private.</p>
</div></main></body></html>`))

func (s *Server) publicSharePage(w http.ResponseWriter, r *http.Request) {
	share, err := s.loadPublicShare(r.Context(), r.PathValue("token"), true)
	if err != nil {
		http.Error(w, "This share link is invalid or has expired.", http.StatusNotFound)
		return
	}
	data := map[string]any{
		"Name": share.Name, "TenantName": share.TenantName, "TenantColor": share.TenantColor,
		"SizeLabel": formatBytes(share.SizeBytes), "ExpiresLabel": share.ExpiresAt.UTC().Format("2 Jan 2006"),
		"Recipient": share.Recipient, "OwnerName": share.OwnerName,
		"Image": strings.HasPrefix(share.MIME, "image/"), "PDF": share.MIME == "application/pdf",
		"Type":       strings.ToUpper(safeFileName(strings.TrimPrefix(filepath.Ext(share.Name), "."))),
		"ContentURL": "/api/public/shares/" + r.PathValue("token") + "/content",
		"ReplaceURL": "/api/public/shares/" + r.PathValue("token") + "/replace",
		"CanEdit":    share.Access == "edit",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; frame-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if err := publicShareTemplate.Execute(w, data); err != nil {
		s.logger.Warn("public share page render failed", "err", err)
	}
}

func (s *Server) publicShareReplace(w http.ResponseWriter, r *http.Request) {
	share, err := s.loadPublicShare(r.Context(), r.PathValue("token"), false)
	if err != nil {
		http.Error(w, "This share link is invalid or has expired.", http.StatusNotFound)
		return
	}
	if share.Access != "edit" {
		http.Error(w, "This link only allows viewing.", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1024*1024)
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		http.Error(w, "The uploaded file is too large or invalid.", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Choose a file to upload.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxAttachmentBytes {
		http.Error(w, "The uploaded file is too large or empty.", http.StatusBadRequest)
		return
	}
	if err := s.scanUpload(r.Context(), data); err != nil {
		if errors.Is(err, errUploadInfected) {
			http.Error(w, "The uploaded file failed the antivirus scan.", http.StatusUnprocessableEntity)
		} else {
			s.logger.Warn("guest share antivirus scan failed", "err", err)
			http.Error(w, "The file could not be scanned for malware.", http.StatusServiceUnavailable)
		}
		return
	}
	name := safeFileName(header.Filename)
	if name == "" {
		name = share.Name
	}
	mime, previewable := detectUploadMIME(data, header.Header.Get("Content-Type"), name)
	var storageKey, externalID, externalURL string
	switch share.ProviderID {
	case "a8m":
		storageKey = newID("att")
		if err := os.WriteFile(filepath.Join(s.cfg.UploadDir, storageKey), data, 0o640); err != nil {
			http.Error(w, "The file could not be stored.", http.StatusInternalServerError)
			return
		}
	case "s3":
		storageKey = fmt.Sprintf("tenants/%s/users/%s/files/%s/%s", share.TenantID, share.OwnerID, share.ID, name)
		if err := s.s3PutObject(r.Context(), storageKey, mime, data); err != nil {
			http.Error(w, "The S3 upload failed.", http.StatusBadGateway)
			return
		}
	default:
		sess := &sessionRecord{User: userRecord{ID: share.OwnerID, TenantID: share.TenantID}}
		externalID, externalURL, err = s.uploadToOAuthProvider(r.Context(), sess, share.ProviderID, name, mime, data)
		if err != nil {
			http.Error(w, "The provider upload failed.", http.StatusBadGateway)
			return
		}
	}
	checksum := sha256.Sum256(data)
	_, err = s.store.DB.Exec(r.Context(), `
		UPDATE files SET name=$2, mime=$3, size_bytes=$4, storage_key=$5, external_id=$6,
		       external_url=$7, previewable=$8, version=version+1, checksum=$9, updated_at=now()
		WHERE id=$1
	`, share.ID, name, mime, len(data), storageKey, externalID, externalURL,
		previewable || mime == "application/pdf" || strings.HasPrefix(mime, "image/"), hex.EncodeToString(checksum[:]))
	if err != nil {
		http.Error(w, "The file record could not be updated.", http.StatusInternalServerError)
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO workspace_audit (tenant_id, user_id, actor, action, target, detail)
		VALUES ($1,$2,'Guest link','file.share_updated',$3,'File replaced through an edit share link')
	`, share.TenantID, share.OwnerID, name)
	http.Redirect(w, r, "/files/s/"+r.PathValue("token")+"?updated=1", http.StatusSeeOther)
}
