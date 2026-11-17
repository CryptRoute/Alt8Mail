package httpapi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

// mailCryptoVault wraps OpenPGP private keys with a dedicated, versioned key.
// The wrapped key is the only form that reaches PostgreSQL.
type mailCryptoVault struct {
	keys [][]byte
}

func newMailCryptoVault(secret, previous string) *mailCryptoVault {
	if strings.TrimSpace(secret) == "" {
		return nil
	}
	vault := &mailCryptoVault{}
	for _, value := range []string{secret, previous} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		sum := sha256.Sum256([]byte("alt8mail-mail-crypto-v1|" + value))
		vault.keys = append(vault.keys, sum[:])
	}
	return vault
}

func (v *mailCryptoVault) seal(tenantID, userID, identityID, plaintext string) (string, error) {
	if v == nil || len(v.keys) == 0 {
		return "", fmt.Errorf("mail encryption key vault is not configured")
	}
	block, err := aes.NewCipher(v.keys[0])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), cryptoKeyAAD(tenantID, userID, identityID))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (v *mailCryptoVault) open(tenantID, userID, identityID, sealed string) (string, error) {
	if v == nil {
		return "", fmt.Errorf("mail encryption key vault is not configured")
	}
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("decode wrapped key: %w", err)
	}
	var lastErr error
	for _, key := range v.keys {
		block, err := aes.NewCipher(key)
		if err != nil {
			return "", err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return "", err
		}
		if len(raw) < gcm.NonceSize() {
			return "", fmt.Errorf("wrapped key is truncated")
		}
		nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
		plain, err := gcm.Open(nil, nonce, ciphertext, cryptoKeyAAD(tenantID, userID, identityID))
		if err == nil {
			return string(plain), nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("wrapped key could not be opened: %w", lastErr)
}

func cryptoKeyAAD(tenantID, userID, identityID string) []byte {
	return []byte("alt8mail-mail-crypto-v1|" + tenantID + "|" + userID + "|" + identityID)
}

type encryptionIdentityPayload struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Protocol         string     `json:"protocol"`
	KeyID            string     `json:"keyId"`
	Fingerprint      string     `json:"fingerprint"`
	PublicKey        string     `json:"publicKey"`
	CertificateChain string     `json:"certificateChain,omitempty"`
	Subject          string     `json:"subject,omitempty"`
	Issuer           string     `json:"issuer,omitempty"`
	NotBefore        *time.Time `json:"notBefore,omitempty"`
	NotAfter         *time.Time `json:"notAfter,omitempty"`
	Source           string     `json:"source"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type encryptionStatusPayload struct {
	Enabled               bool                       `json:"enabled"`
	Mode                  string                     `json:"mode"`
	Protocol              string                     `json:"protocol"`
	Protocols             []string                   `json:"protocols"`
	AllowUserImport       bool                       `json:"allowUserImport"`
	AllowDelegatedDecrypt bool                       `json:"allowDelegatedDecrypt"`
	SMIMEDirectory        bool                       `json:"smimeDirectory"`
	Identity              *encryptionIdentityPayload `json:"identity,omitempty"`
	SMIMEIdentity         *encryptionIdentityPayload `json:"smimeIdentity,omitempty"`
	Contacts              int                        `json:"contacts"`
	SMIMEContacts         int                        `json:"smimeContacts"`
	KeyBackup             bool                       `json:"keyBackup"`
	KeyBackupUpdatedAt    *time.Time                 `json:"keyBackupUpdatedAt,omitempty"`
}

func encryptionProtocols(policy adminEncryptionPolicy) []string {
	if policy.Mode != "client_side" && policy.Mode != "server_managed" {
		return []string{}
	}
	switch policy.Protocol {
	case "openpgp":
		return []string{"openpgp"}
	case "smime":
		return []string{"smime"}
	case "both":
		return []string{"openpgp", "smime"}
	default:
		return []string{}
	}
}

func encryptionAllowsProtocol(policy adminEncryptionPolicy, protocol string) bool {
	for _, item := range encryptionProtocols(policy) {
		if item == protocol {
			return true
		}
	}
	return false
}

func encryptionEnabled(policy adminEncryptionPolicy) bool {
	return len(encryptionProtocols(policy)) > 0
}

func (s *Server) requireMailCryptoProtocol(w http.ResponseWriter, r *http.Request, protocol string) (adminEncryptionPolicy, bool) {
	policy := s.tenantPolicyFor(r.Context(), s.requireSessionFromContext(r).User.TenantID).Encryption
	if !encryptionAllowsProtocol(policy, protocol) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", protocol+" encryption is not enabled for this organization")
		return policy, false
	}
	return policy, true
}

func (s *Server) requireServerManagedCryptoProtocol(w http.ResponseWriter, r *http.Request, protocol string) (adminEncryptionPolicy, bool) {
	policy, ok := s.requireMailCryptoProtocol(w, r, protocol)
	if !ok {
		return policy, false
	}
	if policy.Mode != "server_managed" {
		writeError(w, http.StatusForbidden, "ENCRYPTION_SERVER_MANAGED", "This operation requires server-managed encryption")
		return policy, false
	}
	return policy, true
}

func (s *Server) requireMailCrypto(w http.ResponseWriter, r *http.Request) (adminEncryptionPolicy, bool) {
	return s.requireMailCryptoProtocol(w, r, "openpgp")
}

func (s *Server) encryptionVault() (*mailCryptoVault, error) {
	vault := newMailCryptoVault(s.cfg.CryptoSecret, s.cfg.CryptoSecretPrevious)
	if vault == nil && s.cfg.Env == "production" {
		return nil, fmt.Errorf("ALT8MAIL_CRYPTO_SECRET is required in production")
	}
	if vault == nil {
		// Development fallback only; production must set ALT8MAIL_CRYPTO_SECRET.
		vault = newMailCryptoVault(s.cfg.SessionSecret, "")
	}
	if vault == nil {
		return nil, fmt.Errorf("mail encryption key vault is not configured")
	}
	return vault, nil
}

func encryptionIdentityMatchesMode(source, mode string) bool {
	switch mode {
	case "client_side":
		return source == "client"
	case "server_managed":
		return source != "client"
	default:
		return false
	}
}

func (s *Server) getEncryptionStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	response := encryptionStatusPayload{
		Enabled: encryptionEnabled(policy),
		Mode:    policy.Mode, Protocol: policy.Protocol, Protocols: encryptionProtocols(policy),
		AllowUserImport: policy.AllowUserImport, AllowDelegatedDecrypt: policy.AllowDelegatedDecrypt,
		SMIMEDirectory: s.smimeDirectoryConfigured(),
	}
	if response.Enabled {
		if encryptionAllowsProtocol(policy, "openpgp") {
			identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "openpgp")
			if identity != nil && encryptionIdentityMatchesMode(identity.Source, policy.Mode) {
				if policy.Mode == "client_side" {
					entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(identity.PublicKey))
					if err != nil || !mailcrypto.OpenPGPKeyUsableForEncryption(entity, identity.Email, time.Now()) {
						identity = nil
					}
				}
				response.Identity = identity
			}
			_ = s.store.DB.QueryRow(r.Context(), `
				SELECT count(*) FROM contact_crypto_keys WHERE user_id=$1 AND tenant_id=$2 AND protocol='openpgp'
			`, sess.User.ID, sess.User.TenantID).Scan(&response.Contacts)
			if policy.Mode == "client_side" {
				_ = s.store.DB.QueryRow(r.Context(), `
					SELECT updated_at FROM user_crypto_key_backups WHERE user_id=$1
				`, sess.User.ID).Scan(&response.KeyBackupUpdatedAt)
				response.KeyBackup = response.KeyBackupUpdatedAt != nil
			}
		}
		if encryptionAllowsProtocol(policy, "smime") {
			identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "smime")
			if identity != nil && encryptionIdentityMatchesMode(identity.Source, policy.Mode) {
				if policy.Mode == "client_side" {
					certs, err := mailcrypto.ParseSMIMECertificates([]byte(identity.PublicKey))
					if err != nil {
						identity = nil
					} else {
						leaf, chain := selectSMIMELeaf(certs, nil, identity.Email)
						if leaf == nil || !mailcrypto.CheckSMIMECertificate(leaf, chain, nil, time.Now(), identity.Email, mailcrypto.SMIMEUsageEncrypt).Valid {
							identity = nil
						}
					}
				}
				response.SMIMEIdentity = identity
			}
			_ = s.store.DB.QueryRow(r.Context(), `
				SELECT count(*) FROM contact_crypto_keys WHERE user_id=$1 AND tenant_id=$2 AND protocol='smime'
			`, sess.User.ID, sess.User.TenantID).Scan(&response.SMIMEContacts)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) loadActiveCryptoIdentity(ctx context.Context, userID, protocol string) (*encryptionIdentityPayload, error) {
	var payload encryptionIdentityPayload
	var notBefore, notAfter *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, email::text, protocol, key_id, fingerprint, public_key, cert_chain,
		       subject, issuer, not_before, not_after, source, status, created_at
		FROM user_crypto_identities
		WHERE user_id=$1 AND protocol=$2 AND status='active'
		ORDER BY created_at DESC LIMIT 1
	`, userID, protocol).Scan(&payload.ID, &payload.Email, &payload.Protocol, &payload.KeyID,
		&payload.Fingerprint, &payload.PublicKey, &payload.CertificateChain,
		&payload.Subject, &payload.Issuer, &notBefore, &notAfter,
		&payload.Source, &payload.Status, &payload.CreatedAt)
	if err != nil {
		return nil, err
	}
	payload.NotBefore, payload.NotAfter = notBefore, notAfter
	return &payload, nil
}

func (s *Server) generateEncryptionKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if _, ok := s.requireServerManagedCryptoProtocol(w, r, "openpgp"); !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &body, 32*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		email = sess.User.Email
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = sess.User.Name
	}
	if !s.userOwnsEmail(r.Context(), sess.User, email) {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose one of your own email identities")
		return
	}
	key, err := mailcrypto.GenerateOpenPGPKey(name, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not generate the encryption key")
		return
	}
	vault, err := s.encryptionVault()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_VAULT", "The encryption key vault is not configured")
		return
	}
	identityID := newID("ckey")
	wrapped, err := vault.seal(sess.User.TenantID, sess.User.ID, identityID, key.ArmoredPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not protect the encryption key")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE user_id=$1 AND protocol='openpgp' AND status='active'
	`, sess.User.ID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO user_crypto_identities
				(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, source, status)
			VALUES ($1,$2,$3,$4,'openpgp',$5,$6,$7,'generated','active')
		`, identityID, sess.User.TenantID, sess.User.ID, email, key.KeyID, key.Fingerprint, key.ArmoredPublic)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO user_crypto_private_keys (identity_id, wrapped_key, wrap_version)
			VALUES ($1,$2,1)
		`, identityID, wrapped)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	s.audit(r, sess.User, "encryption.key_generated", email, "Generated a server-managed OpenPGP key")
	identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "openpgp")
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity})
}

type encryptionImportBody struct {
	Protocol     string `json:"protocol"`
	PrivateKey   string `json:"privateKey"`
	Certificate  string `json:"certificate"`
	Chain        string `json:"chain"`
	PKCS12Base64 string `json:"pkcs12Base64"`
	Passphrase   string `json:"passphrase"`
	Email        string `json:"email"`
}

func (s *Server) importEncryptionKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body encryptionImportBody
	if err := decodeJSON(w, r, &body, 8<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(body.Protocol))
	if protocol == "" {
		if body.PKCS12Base64 != "" || body.Certificate != "" {
			protocol = "smime"
		} else {
			protocol = "openpgp"
		}
	}
	if protocol != "openpgp" && protocol != "smime" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_PROTOCOL", "Choose OpenPGP or S/MIME")
		return
	}
	policy, ok := s.requireServerManagedCryptoProtocol(w, r, protocol)
	if !ok {
		return
	}
	hsmCertificateOnly := protocol == "smime" &&
		strings.TrimSpace(body.Certificate) != "" &&
		strings.TrimSpace(body.PrivateKey) == "" &&
		strings.TrimSpace(body.PKCS12Base64) == ""
	if !policy.AllowUserImport && !hsmCertificateOnly {
		writeError(w, http.StatusForbidden, "ENCRYPTION_IMPORT", "Importing an encryption key is disabled by organization policy")
		return
	}
	if protocol == "smime" {
		s.importSMIMEIdentity(w, r, sess, body)
		return
	}
	if strings.TrimSpace(body.PrivateKey) == "" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "Paste an armored OpenPGP private key")
		return
	}
	key, err := mailcrypto.ParseOpenPGPPrivateKey([]byte(body.PrivateKey), []byte(body.Passphrase))
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The private key could not be imported")
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		email = key.Email
	}
	if email == "" {
		email = sess.User.Email
	}
	if !s.userOwnsEmail(r.Context(), sess.User, email) {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose one of your own email identities")
		return
	}
	vault, err := s.encryptionVault()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_VAULT", "The encryption key vault is not configured")
		return
	}
	var existingID, existingStatus string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT COALESCE((
			SELECT id FROM user_crypto_identities
			WHERE user_id=$1 AND protocol='openpgp' AND fingerprint=$2
			LIMIT 1
		), ''), COALESCE((
			SELECT status FROM user_crypto_identities
			WHERE user_id=$1 AND protocol='openpgp' AND fingerprint=$2
			LIMIT 1
		), '')
	`, sess.User.ID, key.Fingerprint).Scan(&existingID, &existingStatus)
	if existingStatus == "revoked" {
		writeError(w, http.StatusConflict, "ENCRYPTION_REVOKED", "This OpenPGP identity has been revoked and cannot be reactivated by import")
		return
	}
	identityID := existingID
	if identityID == "" {
		identityID = newID("ckey")
	}
	wrapped, err := vault.seal(sess.User.TenantID, sess.User.ID, identityID, key.ArmoredPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not protect the encryption key")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE user_id=$1 AND protocol='openpgp' AND status='active' AND id <> $2
	`, sess.User.ID, identityID)
	if err == nil {
		if existingID != "" {
			_, err = tx.Exec(r.Context(), `
				UPDATE user_crypto_identities
				SET email=$3, key_id=$4, public_key=$5, source='imported', status='active', updated_at=now()
				WHERE id=$1 AND user_id=$2
			`, identityID, sess.User.ID, email, key.KeyID, key.ArmoredPublic)
		} else {
			_, err = tx.Exec(r.Context(), `
				INSERT INTO user_crypto_identities
					(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, source, status)
				VALUES ($1,$2,$3,$4,'openpgp',$5,$6,$7,'imported','active')
			`, identityID, sess.User.TenantID, sess.User.ID, email, key.KeyID, key.Fingerprint, key.ArmoredPublic)
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO user_crypto_private_keys (identity_id, wrapped_key, wrap_version)
			VALUES ($1,$2,1)
			ON CONFLICT (identity_id) DO UPDATE
			SET wrapped_key=EXCLUDED.wrapped_key, wrap_version=EXCLUDED.wrap_version, retired_at=NULL
		`, identityID, wrapped)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the encryption key")
		return
	}
	s.audit(r, sess.User, "encryption.key_imported", email, "Imported a server-managed OpenPGP key")
	identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "openpgp")
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity})
}

func (s *Server) revokeEncryptionKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "Choose a key to revoke")
		return
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='revoked', updated_at=now()
		WHERE user_id=$2 AND status <> 'revoked'
		  AND fingerprint = COALESCE((
			SELECT fingerprint FROM user_crypto_identities WHERE id=$1 AND user_id=$2
		  ), '')
	`, id, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not revoke the encryption key")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Encryption key not found")
		return
	}
	s.audit(r, sess.User, "encryption.key_revoked", sess.User.Email, "Revoked an encryption identity")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listContactCryptoKeys(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT id, email::text, protocol, key_id, fingerprint, trust, source, updated_at,
		       verified_at, key_changed_at, not_before, not_after, revoked_at,
		       cert_chain, subject, issuer, certificate_serial
		FROM contact_crypto_keys WHERE user_id=$1 AND tenant_id=$2 ORDER BY email, updated_at DESC
	`, sess.User.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not load recipient keys")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, email, protocol, keyID, fingerprint, trust, source string
		var updated time.Time
		var certChain, subject, issuer, serial string
		var verifiedAt, keyChangedAt, notBefore, notAfter, revokedAt *time.Time
		if err := rows.Scan(&id, &email, &protocol, &keyID, &fingerprint, &trust, &source, &updated,
			&verifiedAt, &keyChangedAt, &notBefore, &notAfter, &revokedAt,
			&certChain, &subject, &issuer, &serial); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"id": id, "email": email, "protocol": protocol, "keyId": keyID,
			"fingerprint": fingerprint, "trust": trust, "source": source, "updatedAt": updated,
			"verifiedAt": verifiedAt, "keyChangedAt": keyChangedAt,
			"notBefore": notBefore, "notAfter": notAfter, "revokedAt": revokedAt,
			"certificateChain": certChain, "subject": subject, "issuer": issuer,
			"certificateSerial": serial,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": items})
}

func (s *Server) saveContactCryptoKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if !encryptionEnabled(policy) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "Encryption is not enabled for this organization")
		return
	}
	email := strings.TrimSpace(r.PathValue("email"))
	if email == "" {
		writeError(w, http.StatusBadRequest, "RECIPIENT", "Choose a recipient email")
		return
	}
	var body struct {
		Protocol    string `json:"protocol"`
		PublicKey   string `json:"publicKey"`
		Certificate string `json:"certificate"`
		Chain       string `json:"chain"`
		Trust       string `json:"trust"`
	}
	if err := decodeJSON(w, r, &body, 4<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(body.Protocol))
	if protocol == "" {
		if body.Certificate != "" {
			protocol = "smime"
		} else {
			protocol = "openpgp"
		}
	}
	if !encryptionAllowsProtocol(policy, protocol) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "This encryption protocol is not enabled by policy")
		return
	}
	var key storedContactCryptoKey
	var keyChanged bool
	var err error
	if protocol == "smime" {
		certificate := body.Certificate
		if certificate == "" {
			certificate = body.PublicKey
		}
		key, keyChanged, err = s.storeContactSMIMECertificate(r.Context(), sess, email, certificate, body.Chain, body.Trust, "manual")
	} else {
		key, keyChanged, err = s.storeContactCryptoKey(r.Context(), sess, email, body.PublicKey, body.Trust, "manual")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", err.Error())
		return
	}
	detail := "Saved a recipient OpenPGP key"
	if protocol == "smime" {
		detail = "Saved a recipient S/MIME certificate"
	}
	s.audit(r, sess.User, "encryption.contact_key_saved", email, detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": key.ID, "protocol": protocol, "fingerprint": key.Fingerprint,
		"keyId": key.KeyID, "trust": key.Trust, "keyChanged": keyChanged,
	})
}

type storedContactCryptoKey struct {
	ID          string
	Email       string
	KeyID       string
	Fingerprint string
	Trust       string
	KeyChanged  bool
}

// storeContactCryptoKey inserts or refreshes a recipient key. A new
// fingerprint never silently replaces a verified key: the new key is stored as
// unknown and marked key_changed_at so the composer can require confirmation.
func (s *Server) storeContactCryptoKey(ctx context.Context, sess *sessionRecord, email, publicKey, trust, source string) (storedContactCryptoKey, bool, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || parsed == nil {
		return storedContactCryptoKey{}, false, fmt.Errorf("Choose a valid recipient email")
	}
	email = strings.ToLower(parsed.Address)
	entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(publicKey))
	if err != nil {
		return storedContactCryptoKey{}, false, fmt.Errorf("The public key could not be imported")
	}
	info, err := mailcrypto.OpenPGPInfo(entity, email)
	if err != nil {
		return storedContactCryptoKey{}, false, fmt.Errorf("The key does not belong to %s", email)
	}
	requestedTrust := strings.ToLower(strings.TrimSpace(trust))
	if requestedTrust != "" && requestedTrust != "unknown" && requestedTrust != "tofu" && requestedTrust != "verified" && requestedTrust != "enterprise" && requestedTrust != "revoked" {
		return storedContactCryptoKey{}, false, fmt.Errorf("Choose a valid trust level")
	}
	if requestedTrust == "" {
		requestedTrust = "unknown"
		if source == "wkd" {
			requestedTrust = "tofu"
		}
	}

	rows, err := s.store.DB.Query(ctx, `
		SELECT id, fingerprint, trust, verified_at
		FROM contact_crypto_keys
		WHERE user_id=$1 AND tenant_id=$2 AND protocol='openpgp' AND lower(email::text)=lower($3)
	`, sess.User.ID, sess.User.TenantID, email)
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	sameID := ""
	sameTrust := ""
	keyChanged := false
	for rows.Next() {
		var id, fingerprint, existingTrust string
		var verifiedAt *time.Time
		if rows.Scan(&id, &fingerprint, &existingTrust, &verifiedAt) != nil {
			continue
		}
		if strings.EqualFold(fingerprint, info.Fingerprint) {
			sameID = id
			sameTrust = existingTrust
		} else {
			keyChanged = true
		}
	}
	rows.Close()

	if sameID != "" {
		trustValue := sameTrust
		if strings.TrimSpace(trust) != "" {
			trustValue = requestedTrust
		}
		verifiedAt := any(nil)
		if trustValue == "verified" || trustValue == "enterprise" {
			verifiedAt = time.Now().UTC()
		}
		_, err := s.store.DB.Exec(ctx, `
			UPDATE contact_crypto_keys
			SET public_key=$4, trust=$5, verified_at=COALESCE(verified_at,$6), last_seen_at=now(), updated_at=now()
			WHERE id=$1 AND user_id=$2 AND tenant_id=$3
		`, sameID, sess.User.ID, sess.User.TenantID, info.Armored, trustValue, verifiedAt)
		if err != nil {
			return storedContactCryptoKey{}, false, err
		}
		return storedContactCryptoKey{ID: sameID, Email: email, KeyID: info.KeyID, Fingerprint: info.Fingerprint, Trust: trustValue}, keyChanged, nil
	}

	trustValue := requestedTrust
	if keyChanged && trustValue != "verified" && trustValue != "enterprise" {
		trustValue = "unknown"
	}
	verifiedAt := any(nil)
	if trustValue == "verified" || trustValue == "enterprise" {
		verifiedAt = time.Now().UTC()
	}
	id := newID("ctkey")
	_, err = s.store.DB.Exec(ctx, `
		INSERT INTO contact_crypto_keys
			(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, trust, source,
			 verified_at, key_changed_at, last_seen_at)
		VALUES ($1,$2,$3,$4,'openpgp',$5,$6,$7,$8,$9,$10,
		        CASE WHEN $11 THEN now() ELSE NULL END, now())
	`, id, sess.User.TenantID, sess.User.ID, email, info.KeyID, info.Fingerprint, info.Armored,
		trustValue, source, verifiedAt, keyChanged)
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	return storedContactCryptoKey{ID: id, Email: email, KeyID: info.KeyID, Fingerprint: info.Fingerprint, Trust: trustValue, KeyChanged: keyChanged}, keyChanged, nil
}

func (s *Server) trustContactCryptoKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Trust string `json:"trust"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	trust := strings.ToLower(strings.TrimSpace(body.Trust))
	switch trust {
	case "unknown", "tofu", "verified", "enterprise", "revoked":
	default:
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "Choose a valid trust level")
		return
	}
	verifiedAt := any(nil)
	revokedAt := any(nil)
	if trust == "verified" || trust == "enterprise" {
		verifiedAt = time.Now().UTC()
	}
	if trust == "revoked" {
		revokedAt = time.Now().UTC()
	}
	result, err := s.store.DB.Exec(r.Context(), `
		UPDATE contact_crypto_keys
		SET trust=$3,
		    verified_at=CASE WHEN $3 IN ('verified','enterprise') THEN COALESCE($4, verified_at) ELSE verified_at END,
		    revoked_at=CASE WHEN $3='revoked' THEN COALESCE($5, revoked_at) ELSE NULL END,
		    updated_at=now()
		WHERE id=$1 AND user_id=$2 AND tenant_id=$6
	`, id, sess.User.ID, trust, verifiedAt, revokedAt, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not update the recipient key trust")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Recipient key not found")
		return
	}
	s.audit(r, sess.User, "encryption.contact_key_trusted", id, "Updated a recipient key or certificate trust")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "trust": trust})
}

func (s *Server) deleteContactCryptoKey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := strings.TrimSpace(r.PathValue("id"))
	result, err := s.store.DB.Exec(r.Context(), `
		DELETE FROM contact_crypto_keys WHERE id=$1 AND user_id=$2 AND tenant_id=$3
	`, id, sess.User.ID, sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not remove the recipient key")
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Recipient key not found")
		return
	}
	s.audit(r, sess.User, "encryption.contact_key_deleted", sess.User.Email, "Removed a recipient key or certificate")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) lookupRecipientCryptoKeys(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol == "" {
		protocol = "openpgp"
	}
	if protocol != "openpgp" && protocol != "smime" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_PROTOCOL", "Choose OpenPGP or S/MIME")
		return
	}
	emails := strings.Split(r.URL.Query().Get("emails"), ",")
	response := map[string]any{}
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" {
			continue
		}
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT email::text, key_id, fingerprint, public_key, trust,
			       verified_at, key_changed_at, not_before, not_after, revoked_at,
			       cert_chain, subject, issuer, certificate_serial
			FROM contact_crypto_keys
			WHERE user_id=$1 AND tenant_id=$2 AND protocol=$3 AND lower(email::text)=lower($4)
			ORDER BY CASE trust
				WHEN 'enterprise' THEN 0 WHEN 'verified' THEN 1 WHEN 'tofu' THEN 2 ELSE 3 END,
				updated_at DESC
		`, sess.User.ID, sess.User.TenantID, protocol, email)
		if err != nil {
			continue
		}
		keys := []map[string]any{}
		for rows.Next() {
			var address, keyID, fingerprint, publicKey, trust string
			var certChain, subject, issuer, serial string
			var verifiedAt, keyChangedAt, notBefore, notAfter, revokedAt *time.Time
			if rows.Scan(&address, &keyID, &fingerprint, &publicKey, &trust,
				&verifiedAt, &keyChangedAt, &notBefore, &notAfter, &revokedAt,
				&certChain, &subject, &issuer, &serial) == nil {
				keys = append(keys, map[string]any{
					"email": address, "keyId": keyID, "fingerprint": fingerprint,
					"publicKey": publicKey, "trust": trust,
					"verifiedAt": verifiedAt, "keyChangedAt": keyChangedAt,
					"notBefore": notBefore, "notAfter": notAfter, "revokedAt": revokedAt,
					"certificateChain": certChain, "subject": subject, "issuer": issuer,
					"certificateSerial": serial,
				})
			}
		}
		rows.Close()
		response[email] = keys
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipients": response, "protocol": protocol})
}

func recipientEmailsFromPeople(lists ...[]any) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, raw := range list {
			person, _ := raw.(map[string]any)
			email := strings.ToLower(strings.TrimSpace(stateString(person, "email")))
			if email == "" || seen[email] {
				continue
			}
			seen[email] = true
			out = append(out, email)
		}
	}
	return out
}

func (s *Server) decryptMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if !encryptionEnabled(policy) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "Encryption is not enabled for this organization")
		return
	}
	if policy.Mode != "server_managed" {
		writeError(w, http.StatusForbidden, "ENCRYPTION_CLIENT_SIDE", "This message must be decrypted in the browser")
		return
	}
	messageID := strings.TrimSpace(r.PathValue("id"))
	if messageID == "" {
		writeError(w, http.StatusBadRequest, "MESSAGE", "Choose a message to decrypt")
		return
	}
	owner := sess.User
	delegated := false
	if ownerID, originalID, ok := parseDelegatedMessageID(messageID); ok {
		if !policy.AllowDelegatedDecrypt {
			writeError(w, http.StatusForbidden, "ENCRYPTION_DELEGATION", "Encrypted delegated mail is disabled by organization policy")
			return
		}
		if _, allowed := s.delegationFor(r.Context(), sess.User.TenantID, ownerID, sess.User.ID); !allowed {
			writeError(w, http.StatusForbidden, "DELEGATE_FORBIDDEN", "You do not have access to this mailbox")
			return
		}
		ownerUser, err := s.userByID(r.Context(), ownerID)
		if err != nil || ownerUser.TenantID != sess.User.TenantID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Delegated mailbox not found")
			return
		}
		owner = ownerUser
		messageID = originalID
		delegated = true
	}
	state, ok := s.loadStateFor(r.Context(), owner.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	var message map[string]any
	if loaded, _, err := s.loadMailMessagesByIDs(r.Context(), owner.ID, []string{messageID}); err == nil && len(loaded) > 0 {
		message = loaded[0]
	} else {
		message = findMessageIn(state, messageID)
	}
	if message == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Message not found")
		return
	}
	crypto := stateMap(message, "crypto")
	if !stateBool(crypto, "encrypted") {
		writeError(w, http.StatusConflict, "NOT_ENCRYPTED", "This message is not encrypted")
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(stateString(crypto, "protocol")))
	if protocol == "" {
		protocol = "openpgp"
	}
	if !encryptionAllowsProtocol(policy, protocol) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "This encryption protocol is not enabled for this organization")
		return
	}
	provider, err := cryptoProviderFor(protocol)
	if err != nil {
		writeError(w, http.StatusConflict, "ENCRYPTION_PROTOCOL", err.Error())
		return
	}
	raw, _, _ := s.fetchRawMessage(r.Context(), owner, state, message)
	var result mailcrypto.DecryptResult
	var decryptErr error
	if protocol == "openpgp" {
		keys, err := s.loadCryptoPrivateKeysForUser(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusConflict, "ENCRYPTION_KEY", "Set up an encryption key in Settings before reading this message")
			return
		}
		for _, key := range keys {
			result, decryptErr = provider.Decrypt(r.Context(), mailcrypto.DecryptRequest{
				Identity:   mailcrypto.IdentityRef{UserID: owner.ID, Email: owner.Email, KeyID: key.KeyID},
				PrivateKey: []byte(key.ArmoredPrivate), Message: raw,
			})
			if decryptErr == nil {
				break
			}
		}
	} else {
		identities, err := s.loadSMIMEIdentities(r.Context(), owner)
		if err != nil {
			writeError(w, http.StatusConflict, "ENCRYPTION_KEY", "Import an S/MIME certificate and key in Settings before reading this message")
			return
		}
		for _, identity := range identities {
			// Use the identity object directly so a token-backed key can run
			// through crypto.Decrypter without exporting key bytes.
			mime, status, warnings, err := mailcrypto.DecryptSMIMEMIME(raw, identity)
			if err != nil {
				decryptErr = err
				continue
			}
			result = mailcrypto.DecryptResult{Protocol: mailcrypto.ProtocolSMIME, MIME: mime, Signature: status, Warnings: warnings}
			decryptErr = nil
			break
		}
	}
	if decryptErr != nil {
		writeError(w, http.StatusUnprocessableEntity, "DECRYPT_FAILED", "This message could not be decrypted with your available encryption keys")
		return
	}
	from := stateMap(message, "from")
	fromEmail := strings.ToLower(strings.TrimSpace(stateString(from, "email")))
	if fromEmail != "" && result.Signature.SignerEmail != "" &&
		!strings.EqualFold(fromEmail, result.Signature.SignerEmail) {
		result.Signature.State = mailcrypto.SignatureInvalid
		result.Signature.Reason = "signature signer does not match the message From address"
	}
	s.applyDecryptedSignatureTrust(r.Context(), owner, protocol, &result.Signature)
	text, htmlBody := parseDecryptedMIME(result.MIME)
	w.Header().Set("Cache-Control", "no-store")
	label := "OpenPGP"
	if protocol == "smime" {
		label = "S/MIME"
	}
	detail := "Decrypted a server-managed " + label + " message"
	if delegated {
		detail = "Decrypted a delegated server-managed " + label + " message for " + owner.Email
	}
	s.audit(r, sess.User, "encryption.message_decrypted", stateString(message, "subject"), detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "bodyText": text, "bodyHtml": htmlBody,
		"signature": result.Signature, "warnings": result.Warnings,
	})
}

// applyDecryptedSignatureTrust upgrades a cryptographically valid signature
// from "valid_untrusted" only when the exact signer fingerprint is explicitly
// verified for this mailbox. A changed, revoked or unknown key never receives
// a trusted badge.
func (s *Server) applyDecryptedSignatureTrust(ctx context.Context, owner userRecord, protocol string, signature *mailcrypto.SignatureStatus) {
	if signature == nil || signature.Fingerprint == "" ||
		(signature.State != mailcrypto.SignatureValidUntrusted && signature.State != mailcrypto.SignatureValid) {
		return
	}
	var trust string
	var keyChangedAt, revokedAt *time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT trust, key_changed_at, revoked_at
		FROM contact_crypto_keys
		WHERE user_id=$1 AND tenant_id=$2 AND protocol=$3
		  AND upper(fingerprint)=upper($4)
		ORDER BY CASE trust
			WHEN 'enterprise' THEN 0 WHEN 'verified' THEN 1 WHEN 'tofu' THEN 2 ELSE 3 END,
			updated_at DESC
		LIMIT 1
	`, owner.ID, owner.TenantID, protocol, signature.Fingerprint).Scan(&trust, &keyChangedAt, &revokedAt)
	if err != nil {
		return
	}
	signature.Trust = mailcrypto.TrustLevel(trust)
	if trust == "revoked" || revokedAt != nil {
		signature.State = mailcrypto.SignatureRevoked
		return
	}
	if keyChangedAt != nil && trust != "verified" && trust != "enterprise" {
		signature.State = mailcrypto.SignatureKeyChanged
		return
	}
	if trust == "verified" || trust == "enterprise" {
		signature.State = mailcrypto.SignatureValid
	}
}

func parseDecryptedMIME(raw []byte) (string, string) {
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}
	body := &parsedBody{}
	_ = walkMIMEPart(textproto.MIMEHeader(parsed.Header), parsed.Body, body, 0)
	return sanitizeMailText(body.text.String()), sanitizeMailText(body.html.String())
}

func (s *Server) userOwnsEmail(ctx context.Context, user userRecord, email string) bool {
	if strings.EqualFold(strings.TrimSpace(user.Email), strings.TrimSpace(email)) {
		return true
	}
	var exists bool
	if err := s.store.DB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_aliases
			WHERE user_id=$1 AND lower(email::text)=lower($2)
		)
	`, user.ID, strings.TrimSpace(email)).Scan(&exists); err != nil {
		return false
	}
	return exists
}

func (s *Server) loadActiveCryptoPrivateKey(ctx context.Context, sess *sessionRecord) (*mailcrypto.OpenPGPKey, error) {
	return s.loadActiveCryptoPrivateKeyForUser(ctx, sess.User)
}

func (s *Server) loadActiveCryptoPrivateKeyForUser(ctx context.Context, user userRecord) (*mailcrypto.OpenPGPKey, error) {
	var identityID, wrapped string
	err := s.store.DB.QueryRow(ctx, `
		SELECT i.id, k.wrapped_key
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		WHERE i.user_id=$1 AND i.tenant_id=$2 AND i.protocol='openpgp' AND i.status='active'
		ORDER BY i.created_at DESC LIMIT 1
	`, user.ID, user.TenantID).Scan(&identityID, &wrapped)
	if err != nil {
		return nil, err
	}
	vault, err := s.encryptionVault()
	if err != nil {
		return nil, err
	}
	armored, err := vault.open(user.TenantID, user.ID, identityID, wrapped)
	if err != nil {
		return nil, err
	}
	return mailcrypto.ParseOpenPGPPrivateKey([]byte(armored), nil)
}

// loadCryptoPrivateKeysForUser returns active and retired identities so a
// message encrypted before a key rotation remains readable. Revoked keys are
// deliberately excluded.
func (s *Server) loadCryptoPrivateKeysForUser(ctx context.Context, user userRecord) ([]*mailcrypto.OpenPGPKey, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT i.id, k.wrapped_key
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		WHERE i.user_id=$1 AND i.tenant_id=$2 AND i.protocol='openpgp'
		  AND i.status IN ('active','retired')
		ORDER BY CASE i.status WHEN 'active' THEN 0 ELSE 1 END, i.created_at DESC
	`, user.ID, user.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type wrappedIdentity struct{ id, wrapped string }
	items := []wrappedIdentity{}
	for rows.Next() {
		var item wrappedIdentity
		if rows.Scan(&item.id, &item.wrapped) == nil {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	vault, err := s.encryptionVault()
	if err != nil {
		return nil, err
	}
	keys := []*mailcrypto.OpenPGPKey{}
	var lastErr error
	for _, item := range items {
		armored, err := vault.open(user.TenantID, user.ID, item.id, item.wrapped)
		if err != nil {
			lastErr = err
			continue
		}
		key, err := mailcrypto.ParseOpenPGPPrivateKey([]byte(armored), nil)
		if err != nil {
			lastErr = err
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no OpenPGP private key is available")
	}
	return keys, nil
}

type storedRecipientKey struct {
	publicKey, keyID, fingerprint, trust, certChain string
	keyChangedAt, notBefore, notAfter               *time.Time
}

// selectRecipientKey receives candidates in trust preference order. A changed
// key blocks the address unless the selected key is explicitly verified or an
// enterprise-managed key. Expired keys are never selected.
func selectRecipientKey(email string, candidates []storedRecipientKey, now time.Time) (storedRecipientKey, error) {
	var selected *storedRecipientKey
	changedUnverified := false
	expired := false
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.notAfter != nil && now.After(*candidate.notAfter) {
			expired = true
			continue
		}
		if candidate.keyChangedAt != nil && candidate.trust != "verified" && candidate.trust != "enterprise" {
			changedUnverified = true
		}
		if selected == nil {
			selected = candidate
		}
	}
	if selected == nil {
		if expired {
			return storedRecipientKey{}, fmt.Errorf("the encryption key for %s has expired", email)
		}
		return storedRecipientKey{}, fmt.Errorf("no encryption key is saved for %s", email)
	}
	if changedUnverified && selected.trust != "verified" && selected.trust != "enterprise" {
		return storedRecipientKey{}, fmt.Errorf("the encryption key for %s changed; verify the new fingerprint before sending", email)
	}
	return *selected, nil
}

func (s *Server) recipientCryptoKeys(ctx context.Context, userID, tenantID, protocol string, emails []string) ([]mailcrypto.RecipientKey, error) {
	if protocol != "openpgp" && protocol != "smime" {
		return nil, fmt.Errorf("unsupported encryption protocol")
	}
	seen := map[string]bool{}
	out := []mailcrypto.RecipientKey{}
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		rows, err := s.store.DB.Query(ctx, `
			SELECT public_key, key_id, fingerprint, trust, cert_chain,
			       key_changed_at, not_before, not_after
			FROM contact_crypto_keys
			WHERE user_id=$1 AND tenant_id=$2 AND protocol=$3
			  AND lower(email::text)=lower($4) AND trust <> 'revoked' AND revoked_at IS NULL
			ORDER BY CASE trust
				WHEN 'enterprise' THEN 0 WHEN 'verified' THEN 1 WHEN 'tofu' THEN 2 ELSE 3 END,
				updated_at DESC
		`, userID, tenantID, protocol, email)
		if err != nil {
			return nil, fmt.Errorf("no encryption key is saved for %s", email)
		}
		candidates := []storedRecipientKey{}
		for rows.Next() {
			var candidate storedRecipientKey
			if rows.Scan(&candidate.publicKey, &candidate.keyID, &candidate.fingerprint,
				&candidate.trust, &candidate.certChain, &candidate.keyChangedAt,
				&candidate.notBefore, &candidate.notAfter) == nil {
				candidates = append(candidates, candidate)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("no encryption key is saved for %s", email)
		}
		rows.Close()
		selected, err := selectRecipientKey(email, candidates, time.Now())
		if err != nil {
			return nil, err
		}
		recipient := mailcrypto.RecipientKey{
			Email: email, Protocol: mailcrypto.Protocol(protocol), KeyID: selected.keyID,
			Fingerprint: selected.fingerprint, PublicKey: []byte(selected.publicKey),
			Trust: mailcrypto.TrustLevel(selected.trust), Source: "contact",
		}
		if selected.certChain != "" {
			recipient.CertChain = [][]byte{[]byte(selected.certChain)}
		}
		if selected.notBefore != nil {
			recipient.NotBefore = *selected.notBefore
		}
		if selected.notAfter != nil {
			recipient.NotAfter = *selected.notAfter
		}
		if protocol == "smime" {
			leaf, chain, err := mailcrypto.SMIMECertificateFromRecipient(recipient)
			if err != nil {
				return nil, fmt.Errorf("the S/MIME certificate for %s is invalid", email)
			}
			roots, err := s.smimeRoots()
			if err != nil {
				return nil, err
			}
			check := mailcrypto.CheckSMIMECertificate(leaf, chain, roots, time.Now(), email, mailcrypto.SMIMEUsageEncrypt)
			s.applySMIMERevocation(ctx, &check, leaf, chain)
			if !check.Valid {
				return nil, fmt.Errorf("the S/MIME certificate for %s is not usable: %s", email, check.Reason)
			}
		}
		out = append(out, recipient)
	}
	return out, nil
}

func encryptionProtocolFromItem(item map[string]any) string {
	protocol := strings.ToLower(strings.TrimSpace(stateString(item, "cryptoProtocol")))
	if protocol == "" {
		protocol = "openpgp"
	}
	return protocol
}

func normalizedEncryptionProtocol(policy adminEncryptionPolicy, requested string) string {
	protocol := strings.ToLower(strings.TrimSpace(requested))
	if protocol != "" {
		return protocol
	}
	protocols := encryptionProtocols(policy)
	if len(protocols) > 0 {
		return protocols[0]
	}
	return "openpgp"
}

func (s *Server) ensureEncryptionIdentity(ctx context.Context, user userRecord, protocol string) error {
	return s.ensureEncryptionIdentityFor(ctx, user, protocol, mailcrypto.SMIMEUsageSign)
}

func (s *Server) ensureEncryptionIdentityFor(ctx context.Context, user userRecord, protocol string, usage mailcrypto.SMIMEUsage) error {
	switch protocol {
	case "openpgp":
		_, err := s.loadActiveCryptoPrivateKeyForUser(ctx, user)
		return err
	case "smime":
		identity, err := s.loadActiveSMIMEIdentity(ctx, user)
		if err != nil {
			return err
		}
		roots, err := s.smimeRoots()
		if err != nil {
			return err
		}
		check := mailcrypto.CheckSMIMECertificate(identity.Certificate, identity.Chain, roots, time.Now(), identity.Email, usage)
		s.applySMIMERevocation(ctx, &check, identity.Certificate, identity.Chain)
		if !check.Valid {
			return fmt.Errorf("S/MIME identity is not usable: %s", check.Reason)
		}
		return nil
	default:
		return fmt.Errorf("unsupported encryption protocol")
	}
}

func cryptoProviderFor(protocol string) (mailcrypto.Provider, error) {
	switch protocol {
	case "openpgp":
		return mailcrypto.OpenPGPProvider{}, nil
	case "smime":
		return mailcrypto.SMIMEProvider{}, nil
	default:
		return nil, fmt.Errorf("unsupported encryption protocol")
	}
}

func (s *Server) encryptOutboundMIME(ctx context.Context, userID, tenantID string, item map[string]any, raw []byte) ([]byte, error) {
	encrypt := stateBool(item, "cryptoEncrypt")
	sign := stateBool(item, "cryptoSigned")
	if !encrypt && !sign {
		return raw, nil
	}
	protocol := encryptionProtocolFromItem(item)
	policy := s.tenantPolicyFor(ctx, tenantID).Encryption
	if !encryptionAllowsProtocol(policy, protocol) {
		return nil, fmt.Errorf("encrypted sending is not enabled for this organization")
	}
	user, err := s.userByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	headers, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		return nil, fmt.Errorf("outbound message has no header/body separator")
	}
	plaintext := body
	if protocol == "openpgp" {
		provider, err := cryptoProviderFor(protocol)
		if err != nil {
			return nil, err
		}
		key, err := s.loadActiveCryptoPrivateKeyForUser(ctx, user)
		if err != nil {
			return nil, fmt.Errorf("set up an encryption key before sending protected mail")
		}
		request := mailcrypto.EncryptRequest{
			Identity:   mailcrypto.IdentityRef{UserID: user.ID, Email: user.Email, KeyID: key.KeyID},
			PrivateKey: []byte(key.ArmoredPrivate),
		}
		signRequest := mailcrypto.SignRequest{
			Identity:   mailcrypto.IdentityRef{UserID: user.ID, Email: user.Email, KeyID: key.KeyID},
			PrivateKey: []byte(key.ArmoredPrivate),
		}
		if encrypt {
			recipients, err := s.recipientCryptoKeys(ctx, userID, tenantID, protocol, recipientEmailsForItem(item))
			if err != nil {
				return nil, err
			}
			request.Recipients = recipients
			request.MIME = plaintext
			request.Sign = sign
			request.EncryptToSelf = true
			result, err := provider.Encrypt(ctx, request)
			if err != nil {
				return nil, err
			}
			plaintext = result.MIME
		} else {
			signRequest.MIME = plaintext
			result, err := provider.Sign(ctx, signRequest)
			if err != nil {
				return nil, err
			}
			plaintext = result.MIME
		}
	} else {
		identity, err := s.loadActiveSMIMEIdentity(ctx, user)
		if err != nil {
			return nil, fmt.Errorf("set up an S/MIME certificate before sending protected mail")
		}
		now := time.Now()
		if sign {
			check := mailcrypto.CheckSMIMECertificate(identity.Certificate, identity.Chain, nil, now, identity.Email, mailcrypto.SMIMEUsageSign)
			if !check.Valid {
				return nil, fmt.Errorf("S/MIME signing certificate is not usable: %s", check.Reason)
			}
		}
		if encrypt {
			check := mailcrypto.CheckSMIMECertificate(identity.Certificate, identity.Chain, nil, now, identity.Email, mailcrypto.SMIMEUsageEncrypt)
			if !check.Valid {
				return nil, fmt.Errorf("S/MIME identity cannot encrypt a copy: %s", check.Reason)
			}
			recipients, err := s.recipientCryptoKeys(ctx, userID, tenantID, protocol, recipientEmailsForItem(item))
			if err != nil {
				return nil, err
			}
			certificates := make([]*x509.Certificate, 0, len(recipients))
			for _, recipient := range recipients {
				leaf, chain, err := mailcrypto.SMIMECertificateFromRecipient(recipient)
				if err != nil {
					return nil, fmt.Errorf("the S/MIME certificate for %s is invalid", recipient.Email)
				}
				check := mailcrypto.CheckSMIMECertificate(leaf, chain, nil, now, recipient.Email, mailcrypto.SMIMEUsageEncrypt)
				if !check.Valid {
					return nil, fmt.Errorf("%s: %s", recipient.Email, check.Reason)
				}
				certificates = append(certificates, leaf)
			}
			mime, err := mailcrypto.EncryptSMIMEMIME(plaintext, certificates, identity, sign, true)
			if err != nil {
				return nil, err
			}
			plaintext = mime
		} else {
			mime, err := mailcrypto.SignSMIMEMIME(plaintext, identity)
			if err != nil {
				return nil, err
			}
			plaintext = mime
		}
	}
	return assembleProtectedMIMEMessage(headers, plaintext), nil
}

// assembleProtectedMIMEMessage merges the ordinary message headers with the
// MIME headers of an encrypted entity. The protected entity supplies its own
// MIME-Version/Content-Type; the original content headers are replaced rather
// than duplicated after a blank line.
func assembleProtectedMIMEMessage(outerHeaders, entity []byte) []byte {
	entityHeaders, entityBody, found := bytes.Cut(entity, []byte("\r\n\r\n"))
	if !found {
		entityHeaders = entity
		entityBody = nil
	}
	cleanHeaders := bytes.TrimRight(stripMIMEContentHeaders(outerHeaders), "\r\n")
	var output bytes.Buffer
	output.Write(cleanHeaders)
	output.WriteString("\r\n")
	output.Write(entityHeaders)
	output.WriteString("\r\n\r\n")
	output.Write(entityBody)
	return output.Bytes()
}

func recipientEmailsForItem(item map[string]any) []string {
	out := []string{}
	for _, list := range []string{"to", "cc", "bcc"} {
		for _, rawPerson := range stateSlice(item, list) {
			person, _ := rawPerson.(map[string]any)
			if email := stateString(person, "email"); email != "" {
				out = append(out, email)
			}
		}
	}
	return out
}

func stripMIMEContentHeaders(headers []byte) []byte {
	lines := strings.Split(strings.ReplaceAll(string(headers), "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "content-type:") ||
			strings.HasPrefix(lower, "content-transfer-encoding:") ||
			strings.HasPrefix(lower, "mime-version:") {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\r\n"))
}
