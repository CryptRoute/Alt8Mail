package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

// publicShareOfficeOpen opens a shared Office document for a guest. It reuses
// the existing share link, its expiry, lock, recipient verification and
// view/edit permission; no Alt8Mail account is required.
func (s *Server) publicShareOfficeOpen(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	share, err := s.loadPublicShare(r.Context(), token, false)
	if err != nil {
		writeError(w, http.StatusNotFound, "SHARE_NOT_FOUND", "This share link is invalid or has expired")
		return
	}
	if share.Locked {
		writeError(w, http.StatusForbidden, "SHARE_LOCKED", "This file is locked until the owner approves access")
		return
	}
	guestEmail := ""
	if share.Recipient != "" {
		email, ok := s.shareAccessEmail(r, token, share)
		if !ok {
			writeError(w, http.StatusForbidden, "SHARE_EMAIL_REQUIRED", "Confirm the recipient email on this device first")
			return
		}
		guestEmail = email
	}
	if share.ProviderID != "a8m" {
		writeError(w, http.StatusBadRequest, "OFFICE_PROVIDER", "Office is available for files in Alt8Mail Drive")
		return
	}
	format, ok := office.Lookup(share.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "OFFICE_FORMAT", "This file type is not supported by Office")
		return
	}
	policy, policyAllowed := s.officeGuestAllowed(r.Context(), share.TenantID, guestEmail)
	if !policyAllowed {
		writeError(w, http.StatusForbidden, "OFFICE_POLICY", "Office is not enabled for this share by organization policy")
		return
	}
	runtime, err := s.officeRuntime(r.Context())
	if err != nil || !runtime.Ready {
		writeError(w, http.StatusConflict, "OFFICE_NOT_READY", "Office is not enabled, healthy and reachable yet")
		return
	}
	document, err := s.ensureOfficeDocument(r.Context(), share.OwnerID, share.TenantID, share.contentFile, format)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE", "Could not prepare the shared document")
		return
	}
	editable := share.Access == "edit" && format.Editable && policy.ExternalEditing
	mode := "view"
	if editable {
		mode = "edit"
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
	base := s.officePublicBase()
	if base == "" {
		writeError(w, http.StatusConflict, "OFFICE_URL", "The webmail public URL is not configured")
		return
	}
	now := time.Now()
	contentToken, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindContent, FileID: share.ID, UserID: share.OwnerID,
		TenantID: share.TenantID, ShareID: share.ShareID, ExpiresAt: now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the document URL")
		return
	}
	callbackToken, err := office.SignURLToken(runtime.Secret, office.URLToken{
		Kind: office.TokenKindCallback, DocumentID: document.ID, Key: document.CurrentKey,
		UserID: share.OwnerID, TenantID: share.TenantID, ShareID: share.ShareID,
		GuestEmail: guestEmail, ExpiresAt: now.Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the callback URL")
		return
	}
	contentURL := base + "/api/office/content?token=" + url.QueryEscape(contentToken)
	callbackURL := base + "/api/office/callback?token=" + url.QueryEscape(callbackToken)
	guestName := guestEmail
	if guestName == "" {
		guestName = "Guest"
	}
	guestID := officeGuestUserID(share.ShareID, guestEmail)
	config, err := s.officeEditorConfig(runtime, share.Name, format, document.CurrentKey, mode, theme, guestID, guestName, contentURL, callbackURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OFFICE_TOKEN", "Could not sign the editor configuration")
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
		"config":            config,
		"documentServerUrl": runtime.PublicURL,
		"editable":          editable,
		"coediting":         editable,
		"guest":             true,
		"document":          officeDocumentPayload(document),
		"file": map[string]any{
			"id": share.ID, "name": share.Name, "mime": share.MIME, "sizeBytes": share.SizeBytes,
		},
	})
}

func (s *Server) officeShareAction(ctx context.Context, share publicShare, token string) (bool, string, string) {
	if share.ProviderID != "a8m" {
		return false, "", ""
	}
	format, ok := office.Lookup(share.Name)
	if !ok {
		return false, "", ""
	}
	policy := s.tenantPolicyFor(ctx, share.TenantID).Office
	if !policy.Enabled {
		return false, "", ""
	}
	if len(policy.AllowedDomains) > 0 {
		domain := splitEmailDomain(share.Recipient)
		if domain == "" {
			return false, "", ""
		}
		allowed := false
		for _, candidate := range policy.AllowedDomains {
			if strings.EqualFold(strings.TrimSpace(candidate), domain) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, "", ""
		}
	}
	runtime, err := s.officeRuntime(ctx)
	if err != nil || !runtime.Ready {
		return false, "", ""
	}
	label := "View in Office"
	if share.Access == "edit" && format.Editable && policy.ExternalEditing {
		label = "Edit in Office"
	}
	return true, label, "/office-editor?share=" + url.QueryEscape(token)
}

func (s *Server) officeGuestAudit(ctx context.Context, tenantID, ownerID, actor, action, target, detail string) {
	actor = truncate(actor, 200)
	target = truncate(target, 240)
	detail = truncate(detail, 500)
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO workspace_audit (tenant_id, user_id, actor, action, target, detail)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, tenantID, ownerID, actor, action, target, detail)
	s.insertAudit(userRecord{ID: ownerID, TenantID: tenantID, Name: actor}, action, target, detail, "", "")
}

func officeGuestUserID(shareID, email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return newID("guest")
	}
	sum := sha256.Sum256([]byte("a8m-office-guest\x00" + shareID + "\x00" + email))
	return "guest_" + hex.EncodeToString(sum[:8])
}
