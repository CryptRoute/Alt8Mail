package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
	"github.com/CryptRoute/Alt8Mail/backend/internal/officecollabora"
)

// collaboraHandler is built per request so a rotated signing secret is picked
// up without restarting the API.
func (s *Server) collaboraHandler() *officecollabora.Handler {
	return &officecollabora.Handler{
		Secret:            s.officeSigningSecret(),
		Service:           &collaboraService{server: s},
		PostMessageOrigin: s.officePublicBase(),
		MaxBytes:          100 << 20,
	}
}

type collaboraFile struct {
	file       contentFile
	document   officeDocumentRecord
	format     office.Format
	canWrite   bool
	actor      string
	createdBy  string
	guest      bool
	guestEmail string
	shareID    string
}

type collaboraService struct {
	server *Server
}

func (c *collaboraService) resolve(ctx context.Context, claims office.URLToken, forWrite bool) (collaboraFile, error) {
	s := c.server
	var (
		file       contentFile
		format     office.Format
		canWrite   bool
		actor      = firstNonEmpty(claims.DisplayName, claims.Actor, "Office")
		createdBy  = claims.UserID
		guest      bool
		guestEmail string
		shareID    string
		ok         bool
	)

	switch {
	case claims.ShareToken != "":
		share, err := s.loadPublicShare(ctx, claims.ShareToken, false)
		if err != nil || share.ShareID != claims.ShareID || share.ID != claims.FileID {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusNotFound, Code: "SHARE_NOT_FOUND", Message: "This share link is invalid or has expired"}
		}
		if share.Locked {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "SHARE_LOCKED", Message: "This file is locked until the owner approves access"}
		}
		if share.Recipient != "" && !strings.EqualFold(strings.TrimSpace(share.Recipient), strings.TrimSpace(claims.GuestEmail)) {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "SHARE_EMAIL", Message: "Confirm the recipient email on this device again"}
		}
		if share.ProviderID != "a8m" {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusBadRequest, Code: "OFFICE_PROVIDER", Message: "Office is available for files in Alt8Mail Drive"}
		}
		format, ok = office.Lookup(share.Name)
		if !ok {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusBadRequest, Code: "OFFICE_FORMAT", Message: "This file type is not supported by Office"}
		}
		policy, allowed := s.officeGuestAllowed(ctx, share.TenantID, claims.GuestEmail)
		if !allowed {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_POLICY", Message: "Office is not enabled for this share by organization policy"}
		}
		file = share.contentFile
		file.TenantID = share.TenantID
		file.Access = share.Access
		canWrite = share.Access == "edit" && format.Editable && policy.ExternalEditing
		guest = true
		guestEmail = claims.GuestEmail
		shareID = share.ShareID
		createdBy = ""
		actor = "Guest link"
		if guestEmail != "" {
			actor = "Guest " + guestEmail
		}

	case claims.EditorUserID != "" && claims.EditorUserID != claims.UserID:
		user, err := s.userByID(ctx, claims.EditorUserID)
		if err != nil || user.TenantID != claims.TenantID {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_EDITOR", Message: "The editor account is unavailable"}
		}
		if _, allowed := s.officeUserAllowed(ctx, user); !allowed {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_POLICY", Message: "Office is no longer enabled for this editor by organization policy"}
		}
		file, err = s.loadAccessibleFileContent(ctx, user, claims.FileID)
		if err != nil || file.OwnerID != claims.UserID || file.TenantID != claims.TenantID {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "File not found"}
		}
		// View-only grants may open the document; only a write needs the live
		// edit grant. This mirrors the ONLYOFFICE callback check on save.
		if forWrite {
			var canEdit bool
			err = s.store.DB.QueryRow(ctx, `
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
			`, file.ID, claims.EditorUserID, claims.TenantID).Scan(&canEdit)
			if err != nil || !canEdit {
				return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_PERMISSION", Message: "Your edit access to this file was revoked"}
			}
		}
		format, ok = office.Lookup(file.Name)
		if !ok {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusBadRequest, Code: "OFFICE_FORMAT", Message: "This file type is not supported by Office"}
		}
		canWrite = (file.Access == "owner" || file.Access == "edit") && format.Editable
		createdBy = claims.EditorUserID
		actor = firstNonEmpty(claims.DisplayName, claims.Actor, "Shared editor")

	default:
		owner, err := s.userByID(ctx, claims.UserID)
		if err != nil || owner.TenantID != claims.TenantID {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "File not found"}
		}
		if _, allowed := s.officeUserAllowed(ctx, owner); !allowed {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_POLICY", Message: "Office is no longer enabled for this account by organization policy"}
		}
		file, err = s.loadFileContent(ctx, claims.UserID, claims.FileID)
		if err != nil || file.TenantID != claims.TenantID {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "File not found"}
		}
		format, ok = office.Lookup(file.Name)
		if !ok {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusBadRequest, Code: "OFFICE_FORMAT", Message: "This file type is not supported by Office"}
		}
		canWrite = format.Editable
		createdBy = claims.UserID
		actor = firstNonEmpty(claims.DisplayName, claims.Actor, "Office")
	}

	if forWrite && !canWrite {
		return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusForbidden, Code: "OFFICE_READONLY", Message: "This session is read-only"}
	}

	document, err := s.loadOfficeDocumentByFileID(ctx, file.ID)
	if err != nil {
		document, err = s.ensureOfficeDocument(ctx, file.OwnerID, file.TenantID, file, format)
		if err != nil {
			return collaboraFile{}, &officecollabora.SaveError{Status: http.StatusInternalServerError, Code: "OFFICE", Message: "Could not prepare the Office document"}
		}
	}

	return collaboraFile{
		file: file, document: document, format: format, canWrite: canWrite,
		actor: actor, createdBy: createdBy, guest: guest, guestEmail: guestEmail, shareID: shareID,
	}, nil
}

func (c *collaboraService) CheckFileInfo(ctx context.Context, claims office.URLToken) (officecollabora.FileInfo, error) {
	resolved, err := c.resolve(ctx, claims, false)
	if err != nil {
		return officecollabora.FileInfo{}, err
	}
	name := firstNonEmpty(claims.DisplayName, claims.Actor, "Office")
	userID := claims.UserID
	if resolved.guest {
		userID = officeGuestUserID(resolved.shareID, resolved.guestEmail)
		name = "Guest"
		if resolved.guestEmail != "" {
			name = resolved.guestEmail
		}
	} else if claims.EditorUserID != "" && claims.EditorUserID != claims.UserID {
		userID = claims.EditorUserID
	}
	return officecollabora.FileInfo{
		FileID:       resolved.file.ID,
		BaseFileName: resolved.file.Name,
		Size:         resolved.file.SizeBytes,
		Version:      collaboraVersion(resolved.file),
		LastModified: resolved.file.UpdatedAt,
		UserID:       userID,
		UserName:     name,
		CanWrite:     resolved.canWrite,
	}, nil
}

func (c *collaboraService) GetFile(ctx context.Context, claims office.URLToken) (officecollabora.FileData, error) {
	resolved, err := c.resolve(ctx, claims, false)
	if err != nil {
		return officecollabora.FileData{}, err
	}
	data, err := c.server.officeReadStorage(firstNonEmpty(resolved.file.StorageKey, resolved.file.AttachmentID))
	if err != nil {
		return officecollabora.FileData{}, &officecollabora.SaveError{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "File content is unavailable"}
	}
	return officecollabora.FileData{
		Data:         data,
		ContentType:  officecollabora.ContentType(resolved.file.Name),
		Version:      collaboraVersion(resolved.file),
		LastModified: resolved.file.UpdatedAt,
	}, nil
}

func (c *collaboraService) SaveFile(ctx context.Context, claims office.URLToken, data []byte, base time.Time) (officecollabora.SaveResult, error) {
	s := c.server
	resolved, err := c.resolve(ctx, claims, true)
	if err != nil {
		return officecollabora.SaveResult{}, err
	}

	// Collabora's timestamp conflict test. A request without the header is an
	// explicit user overwrite and is accepted.
	if !base.IsZero() && base.Before(resolved.file.UpdatedAt) {
		return officecollabora.SaveResult{}, &officecollabora.ConflictError{Current: resolved.file.UpdatedAt}
	}

	dlp := s.evaluateOfficeSaveDLP(ctx, resolved.file.TenantID, resolved.file.Name, data)
	if dlp.Blocked {
		message := "This document is blocked by your organization's data protection policy."
		s.officeGuestAudit(ctx, resolved.file.TenantID, resolved.document.UserID, resolved.actor,
			"office.dlp_blocked", resolved.file.Name,
			truncate(message+" "+strings.Join(dlp.Warnings, " "), 500))
		return officecollabora.SaveResult{}, &officecollabora.SaveError{
			Status: http.StatusForbidden, Code: "OFFICE_DLP", Message: message,
		}
	}

	officePolicy := s.tenantPolicyFor(ctx, resolved.file.TenantID).Office
	prunedBytes := s.officeRetentionPruneBytes(ctx, resolved.document.ID, officePolicy.VersionRetention)
	addBytes := int64(len(data)) - prunedBytes
	if addBytes < 0 {
		addBytes = 0
	}
	owner, err := s.userByID(ctx, resolved.document.UserID)
	if err != nil {
		return officecollabora.SaveResult{}, &officecollabora.SaveError{Status: http.StatusInternalServerError, Code: "OFFICE", Message: "The document owner is unavailable"}
	}
	if quotaErr := s.enforceStorageQuota(ctx, owner, addBytes); quotaErr != nil {
		s.officeGuestAudit(ctx, resolved.file.TenantID, resolved.document.UserID, resolved.actor,
			"office.quota_blocked", resolved.file.Name,
			truncate("Office save blocked by the Cabinet quota · "+quotaErr.Error(), 500))
		return officecollabora.SaveResult{}, &officecollabora.SaveError{
			Status: http.StatusInsufficientStorage, Code: "STORAGE_QUOTA",
			Message: "This save would exceed the Cabinet storage quota. Remove files or ask an administrator for more storage.",
		}
	}

	sum := sha256.Sum256(data)
	eventKey := "wopi:" + hex.EncodeToString(sum[:]) + ":" + base.UTC().Format(time.RFC3339Nano)
	documentKey := "wopi:" + resolved.document.ID
	eventID, state, err := s.officeClaimCallbackEvent(ctx, resolved.document.ID, documentKey, 2, eventKey)
	if err != nil {
		return officecollabora.SaveResult{}, &officecollabora.SaveError{Status: http.StatusInternalServerError, Code: "OFFICE", Message: "Could not record the Office save"}
	}
	if state == "done" {
		// Identical content already landed; return the current state.
		if current, loadErr := c.resolve(ctx, claims, false); loadErr == nil {
			resolved = current
		}
		return officecollabora.SaveResult{Version: collaboraVersion(resolved.file), LastModified: resolved.file.UpdatedAt}, nil
	}
	if state == "processing" {
		return officecollabora.SaveResult{}, &officecollabora.SaveError{
			Status: http.StatusConflict, Code: "OFFICE_SAVE_BUSY", Message: "A save is already in progress. Retry in a moment.",
		}
	}

	version, _, name, err := s.officeSaveCallbackVersion(ctx, resolved.document, documentKey, data, dlp,
		officePolicy.VersionRetention, resolved.createdBy, eventID)
	if err != nil {
		s.officeFailCallbackEvent(ctx, eventID, err.Error())
		return officecollabora.SaveResult{}, &officecollabora.SaveError{Status: http.StatusInternalServerError, Code: "OFFICE_SAVE", Message: "Could not save the document"}
	}

	action := "office.saved"
	if resolved.guest {
		action = "office.guest_saved"
	} else if resolved.createdBy != "" && resolved.createdBy != resolved.document.UserID {
		action = "office.shared_saved"
	}
	s.officeGuestAudit(ctx, resolved.file.TenantID, resolved.document.UserID, resolved.actor, action, name,
		fmt.Sprintf("Saved version %d from the Collabora editor", version))

	return officecollabora.SaveResult{Version: hex.EncodeToString(sum[:]), LastModified: time.Now().UTC()}, nil
}

func collaboraVersion(file contentFile) string {
	if checksum := strings.TrimSpace(file.Checksum); checksum != "" {
		return checksum
	}
	return strconv.Itoa(file.Version)
}

func (s *Server) collaboraEditorURL(runtime officeRuntime, base, fileID, token string) string {
	wopiSrc := base + "/api/office/wopi/files/" + url.PathEscape(fileID)
	return runtime.PublicURL + "/browser/dist/cool.html" +
		"?WOPISrc=" + url.QueryEscape(wopiSrc) +
		"&lang=en" +
		"&access_token=" + url.QueryEscape(token)
}

func (s *Server) openOfficeDocumentCollabora(w http.ResponseWriter, r *http.Request, sess *sessionRecord, runtime officeRuntime, file contentFile, document officeDocumentRecord, format office.Format) {
	base := s.officeWopiBase(r)
	if base == "" {
		writeError(w, http.StatusConflict, "OFFICE_URL", "The webmail public URL is not configured")
		return
	}
	canWrite := (file.Access == "owner" || file.Access == "edit") && format.Editable
	now := time.Now()
	token, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindWOPI, FileID: file.ID, UserID: file.OwnerID, TenantID: file.TenantID,
		EditorUserID: sess.User.ID, Actor: sess.User.Email,
		DisplayName: firstNonEmpty(sess.User.Name, sess.User.Email),
		CanWrite:    canWrite, ExpiresAt: now.Add(12 * time.Hour).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the editor session")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO office_edit_sessions (id, document_id, document_key, user_id, tenant_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (document_id, document_key) DO UPDATE SET status='open', last_callback_at=now()
	`, newID("osess"), document.ID, document.CurrentKey, file.OwnerID, file.TenantID)
	accessLabel := "view"
	if canWrite {
		accessLabel = "edit"
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.opened", file.Name, "Opened in Collabora Office with "+accessLabel+" access")
	writeJSON(w, http.StatusOK, map[string]any{
		"engine":    "collabora",
		"editorUrl": s.collaboraEditorURL(runtime, base, file.ID, token),
		"editable":  canWrite,
		"document":  officeDocumentPayload(document),
		"file":      s.publicFile(file.fileRecord, sess.User, nil),
		"aiEnabled": s.officeAIEnabled(r.Context(), sess.User),
	})
}

func (s *Server) openPublicShareOfficeCollabora(w http.ResponseWriter, r *http.Request, runtime officeRuntime, share publicShare, document officeDocumentRecord, editable bool, guestEmail, shareToken string) {
	base := s.officeWopiBase(r)
	if base == "" {
		writeError(w, http.StatusConflict, "OFFICE_URL", "The webmail public URL is not configured")
		return
	}
	guestName := guestEmail
	if guestName == "" {
		guestName = "Guest"
	}
	now := time.Now()
	token, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindWOPI, FileID: share.ID, UserID: share.OwnerID, TenantID: share.TenantID,
		ShareID: share.ShareID, ShareToken: shareToken, GuestEmail: guestEmail,
		DisplayName: guestName, CanWrite: editable, ExpiresAt: now.Add(12 * time.Hour).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the editor session")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		INSERT INTO office_edit_sessions (id, document_id, document_key, user_id, tenant_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (document_id, document_key) DO UPDATE SET status='open', last_callback_at=now()
	`, newID("osess"), document.ID, document.CurrentKey, share.OwnerID, share.TenantID)
	actor := "Guest link"
	if guestEmail != "" {
		actor = "Guest " + guestEmail
	}
	s.officeGuestAudit(r.Context(), share.TenantID, share.OwnerID, actor, "office.guest_opened", share.Name,
		"Opened a shared Office document through a "+share.Access+" link")
	writeJSON(w, http.StatusOK, map[string]any{
		"engine":    "collabora",
		"editorUrl": s.collaboraEditorURL(runtime, base, share.ID, token),
		"editable":  editable,
		"coediting": editable,
		"guest":     true,
		"document":  officeDocumentPayload(document),
		"file": map[string]any{
			"id": share.ID, "name": share.Name, "mime": share.MIME, "sizeBytes": share.SizeBytes,
		},
	})
}
