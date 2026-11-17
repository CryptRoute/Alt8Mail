package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type encryptionKeyBackup struct {
	Email               string    `json:"email"`
	Fingerprint         string    `json:"fingerprint"`
	PublicKey           string    `json:"publicKey"`
	EncryptedPrivateKey string    `json:"encryptedPrivateKey,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (s *Server) requireClientSideBackup(w http.ResponseWriter, r *http.Request) (*sessionRecord, bool) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if policy.Mode != "client_side" || !encryptionAllowsProtocol(policy, "openpgp") {
		writeError(w, http.StatusForbidden, "ENCRYPTION_MODE", "Encrypted key backups are available only in client-side mode")
		return nil, false
	}
	return sess, true
}

func (s *Server) saveEncryptionKeyBackup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireClientSideBackup(w, r)
	if !ok {
		return
	}
	var body struct {
		Email               string `json:"email"`
		Fingerprint         string `json:"fingerprint"`
		PublicKey           string `json:"publicKey"`
		EncryptedPrivateKey string `json:"encryptedPrivateKey"`
	}
	if err := decodeJSON(w, r, &body, 2<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		email = strings.ToLower(sess.User.Email)
	}
	if !s.userOwnsEmail(r.Context(), sess.User, email) {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose one of your own email identities")
		return
	}
	if len(body.EncryptedPrivateKey) == 0 || len(body.EncryptedPrivateKey) > 1<<20 ||
		!strings.Contains(body.EncryptedPrivateKey, "BEGIN PGP PRIVATE KEY BLOCK") {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_BACKUP", "The encrypted OpenPGP private key is missing or too large")
		return
	}
	entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(body.PublicKey))
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_BACKUP", "The public key could not be parsed")
		return
	}
	info, err := mailcrypto.OpenPGPInfo(entity, email)
	if err != nil || !strings.EqualFold(info.Fingerprint, strings.TrimSpace(body.Fingerprint)) {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_BACKUP", "The public key does not match the fingerprint")
		return
	}
	privateFingerprint, err := mailcrypto.OpenPGPPrivateKeyFingerprint([]byte(body.EncryptedPrivateKey))
	if err != nil || !strings.EqualFold(privateFingerprint, info.Fingerprint) {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_BACKUP", "The encrypted private key does not match the public key")
		return
	}
	protected, err := mailcrypto.OpenPGPPrivateKeyProtected([]byte(body.EncryptedPrivateKey))
	if err != nil || !protected {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_BACKUP", "The private key must be passphrase-encrypted before upload")
		return
	}
	if !mailcrypto.OpenPGPKeyUsableForEncryption(entity, email, time.Now()) {
		writeError(w, http.StatusUnprocessableEntity, "ENCRYPTION_BACKUP", "The key is revoked, expired or cannot encrypt mail")
		return
	}
	var updatedAt time.Time
	err = s.store.DB.QueryRow(r.Context(), `
		INSERT INTO public.user_crypto_key_backups
			(user_id, tenant_id, email, fingerprint, public_key, encrypted_private_key, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (user_id) DO UPDATE SET
			tenant_id=EXCLUDED.tenant_id, email=EXCLUDED.email,
			fingerprint=EXCLUDED.fingerprint, public_key=EXCLUDED.public_key,
			encrypted_private_key=EXCLUDED.encrypted_private_key, updated_at=now()
		RETURNING updated_at
	`, sess.User.ID, sess.User.TenantID, email, info.Fingerprint, info.Armored, body.EncryptedPrivateKey).Scan(&updatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_BACKUP", "Could not store the encrypted key backup")
		return
	}
	s.audit(r, sess.User, "encryption.key_backup_saved", email, "Stored an encrypted browser key backup")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updatedAt": updatedAt.UTC().Format(time.RFC3339)})
}

func (s *Server) getEncryptionKeyBackup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireClientSideBackup(w, r)
	if !ok {
		return
	}
	var backup encryptionKeyBackup
	err := s.store.DB.QueryRow(r.Context(), `
		SELECT email::text, fingerprint, public_key, encrypted_private_key, updated_at
		FROM public.user_crypto_key_backups WHERE user_id=$1
	`, sess.User.ID).Scan(&backup.Email, &backup.Fingerprint, &backup.PublicKey, &backup.EncryptedPrivateKey, &backup.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "ENCRYPTION_BACKUP", "No encrypted key backup is stored for this account")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"backup": backup})
}

func (s *Server) deleteEncryptionKeyBackup(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.requireClientSideBackup(w, r)
	if !ok {
		return
	}
	if _, err := s.store.DB.Exec(r.Context(), `DELETE FROM public.user_crypto_key_backups WHERE user_id=$1`, sess.User.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_BACKUP", "Could not remove the key backup")
		return
	}
	s.audit(r, sess.User, "encryption.key_backup_deleted", sess.User.Email, "Removed the encrypted browser key backup")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
