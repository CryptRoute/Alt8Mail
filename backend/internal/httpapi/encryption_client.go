package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

// publishClientIdentity records a public key or certificate chain held by the
// browser. No private-key material is accepted or stored by this endpoint.
func (s *Server) publishClientIdentity(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if policy.Mode != "client_side" {
		writeError(w, http.StatusForbidden, "ENCRYPTION_MODE", "Client-side key publication is available only in client-side mode")
		return
	}
	var body struct {
		Email     string `json:"email"`
		PublicKey string `json:"publicKey"`
		Protocol  string `json:"protocol"`
	}
	if err := decodeJSON(w, r, &body, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(body.Protocol))
	if protocol == "" {
		protocol = "openpgp"
	}
	if protocol != "openpgp" && protocol != "smime" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_PROTOCOL", "Choose OpenPGP or S/MIME")
		return
	}
	if !encryptionAllowsProtocol(policy, protocol) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "This encryption protocol is not enabled for this organization")
		return
	}
	if strings.TrimSpace(body.PublicKey) == "" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "Publish a public key or certificate")
		return
	}
	if len(body.PublicKey) > 128<<10 {
		writeError(w, http.StatusRequestEntityTooLarge, "ENCRYPTION_KEY", "The public key is too large")
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
	var fingerprint, keyID, publicKey, certChain, subject, issuer, serial string
	var notBefore, notAfter *time.Time
	switch protocol {
	case "openpgp":
		entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(body.PublicKey))
		if err != nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The public key could not be imported")
			return
		}
		info, err := mailcrypto.OpenPGPInfo(entity, email)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", err.Error())
			return
		}
		if !mailcrypto.OpenPGPKeyUsableForEncryption(entity, email, time.Now()) {
			writeError(w, http.StatusUnprocessableEntity, "ENCRYPTION_KEY", "This public key is revoked, expired or cannot encrypt mail")
			return
		}
		fingerprint, keyID, publicKey = info.Fingerprint, info.KeyID, info.Armored
	case "smime":
		certs, err := mailcrypto.ParseSMIMECertificates([]byte(body.PublicKey))
		if err != nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The S/MIME certificate could not be imported")
			return
		}
		leaf, chain := selectSMIMELeaf(certs, nil, email)
		if leaf == nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The certificate does not cover "+email)
			return
		}
		info, err := mailcrypto.CertificateInfoFor(leaf, chain)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The S/MIME certificate is invalid")
			return
		}
		check := mailcrypto.CheckSMIMECertificate(leaf, chain, nil, time.Now(), email, mailcrypto.SMIMEUsageEncrypt)
		if !check.Valid {
			writeError(w, http.StatusUnprocessableEntity, "ENCRYPTION_KEY", "This certificate cannot encrypt mail: "+check.Reason)
			return
		}
		fingerprint = info.Fingerprint
		keyID = info.Fingerprint
		publicKey = string(info.CertificatePEM) + string(info.ChainPEM)
		certChain = string(info.ChainPEM)
		subject = info.Subject
		issuer = info.Issuer
		serial = leaf.SerialNumber.Text(16)
		nb, na := leaf.NotBefore.UTC(), leaf.NotAfter.UTC()
		notBefore, notAfter = &nb, &na
	}
	var existingStatus string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT COALESCE((
			SELECT CASE WHEN bool_or(status='revoked') THEN 'revoked' ELSE max(status) END
			FROM user_crypto_identities
			WHERE user_id=$1 AND protocol=$2 AND fingerprint=$3
		), '')
	`, sess.User.ID, protocol, fingerprint).Scan(&existingStatus)
	if existingStatus == "revoked" {
		writeError(w, http.StatusConflict, "ENCRYPTION_REVOKED", "This identity has been revoked and cannot be republished")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not publish the client identity")
		return
	}
	defer tx.Rollback(r.Context())
	// Publishing only proves possession of public material. Never convert a
	// server-managed identity by fingerprint: a user could publish a public key
	// whose private half only the server holds, then lose access to old mail.
	var serverManagedKey bool
	if err := tx.QueryRow(r.Context(), `
		SELECT EXISTS(
			SELECT 1 FROM user_crypto_private_keys p
			JOIN user_crypto_identities i ON i.id=p.identity_id
			WHERE i.user_id=$1 AND i.protocol=$2 AND i.fingerprint=$3
		)
	`, sess.User.ID, protocol, fingerprint).Scan(&serverManagedKey); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not publish the client identity")
		return
	}
	if serverManagedKey {
		writeError(w, http.StatusConflict, "ENCRYPTION_SERVER_KEY", "This identity is managed by the server; create a browser identity instead")
		return
	}
	_, err = tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE user_id=$1 AND protocol=$2 AND status='active' AND fingerprint <> $3
	`, sess.User.ID, protocol, fingerprint)
	var identityID string
	if err == nil {
		err = tx.QueryRow(r.Context(), `
			INSERT INTO user_crypto_identities
				(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key,
				 cert_chain, subject, issuer, certificate_serial, not_before, not_after, source, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'client','active')
			ON CONFLICT (user_id, protocol, fingerprint, email) DO UPDATE
			SET key_id=EXCLUDED.key_id, public_key=EXCLUDED.public_key,
			    cert_chain=EXCLUDED.cert_chain, subject=EXCLUDED.subject,
			    issuer=EXCLUDED.issuer, certificate_serial=EXCLUDED.certificate_serial,
			    not_before=EXCLUDED.not_before, not_after=EXCLUDED.not_after,
			    source='client', status='active', updated_at=now()
			RETURNING id
		`, newID("ckey"), sess.User.TenantID, sess.User.ID, email, protocol, keyID, fingerprint, publicKey,
			certChain, subject, issuer, serial, notBefore, notAfter).Scan(&identityID)
	}
	if err == nil {
		// Defense in depth: a browser identity must never retain a server-side
		// private key, even if a row was created concurrently by another path.
		_, err = tx.Exec(r.Context(), `
			DELETE FROM user_crypto_private_keys WHERE identity_id=$1
		`, identityID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not publish the client identity")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not publish the client identity")
		return
	}
	s.audit(r, sess.User, "encryption.client_identity_published", email,
		"Published a browser-held "+strings.ToUpper(protocol)+" public identity · "+fingerprint)
	identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, protocol)
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity})
}

// encryptionDirectory returns active public keys for tenant addresses. It is a
// public-key directory only; private keys never enter this path.
func (s *Server) encryptionDirectory(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if !encryptionEnabled(policy) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "Encryption is not enabled for this organization")
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol == "" {
		protocol = "openpgp"
	}
	if protocol != "openpgp" && protocol != "smime" {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_PROTOCOL", "Choose OpenPGP or S/MIME")
		return
	}
	if !encryptionAllowsProtocol(policy, protocol) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "This encryption protocol is not enabled for this organization")
		return
	}
	emails := []string{}
	seen := map[string]bool{}
	for _, raw := range strings.Split(r.URL.Query().Get("emails"), ",") {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		emails = append(emails, email)
		if len(emails) >= 50 {
			break
		}
	}
	response := map[string][]map[string]any{}
	if len(emails) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"directory": response})
		return
	}
	// In client-side mode the directory must contain only keys held by a
	// browser. A server-managed key left over from an earlier policy would
	// otherwise let the server read a message the user believes is zero-access.
	sourceFilter := ""
	switch policy.Mode {
	case "client_side":
		sourceFilter = " AND i.source='client'"
	case "server_managed":
		// Browser-held keys cannot be used by the server-managed pipeline: the
		// server has no private key and the ciphertext endpoint is disabled.
		sourceFilter = " AND i.source <> 'client'"
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT i.email::text, i.key_id, i.fingerprint, i.public_key, i.source
		FROM user_crypto_identities i
		JOIN users u ON u.id=i.user_id AND u.status='active'
		WHERE i.tenant_id=$1 AND i.protocol=$2 AND i.status='active'
		  AND lower(i.email::text) = ANY($3)`+sourceFilter+`
		ORDER BY i.email, i.created_at DESC
	`, sess.User.TenantID, protocol, emails)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not read the encryption directory")
		return
	}
	defer rows.Close()
	seenEmails := map[string]bool{}
	for rows.Next() {
		var email, keyID, fingerprint, publicKey, source string
		if rows.Scan(&email, &keyID, &fingerprint, &publicKey, &source) != nil {
			continue
		}
		email = strings.ToLower(email)
		if seenEmails[email] || len(publicKey) > 128<<10 {
			continue
		}
		usable := false
		switch protocol {
		case "openpgp":
			entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(publicKey))
			usable = err == nil && mailcrypto.OpenPGPKeyUsableForEncryption(entity, email, time.Now())
		case "smime":
			certs, err := mailcrypto.ParseSMIMECertificates([]byte(publicKey))
			if err == nil {
				leaf, chain := selectSMIMELeaf(certs, nil, email)
				if leaf != nil {
					usable = mailcrypto.CheckSMIMECertificate(leaf, chain, nil, time.Now(), email, mailcrypto.SMIMEUsageEncrypt).Valid
				}
			}
		}
		if !usable {
			continue
		}
		seenEmails[email] = true
		response[email] = []map[string]any{{
			"email": email, "keyId": keyID, "fingerprint": fingerprint,
			"publicKey": publicKey, "source": source,
		}}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not read the encryption directory")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directory": response, "protocol": protocol})
}
