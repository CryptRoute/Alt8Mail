package httpapi

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

func decodeSMIMEBase64(value string) ([]byte, error) {
	value = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func (s *Server) importSMIMEIdentity(w http.ResponseWriter, r *http.Request, sess *sessionRecord, body encryptionImportBody) {
	passphrase := []byte(body.Passphrase)
	var identity *mailcrypto.SMIMEIdentity
	var err error
	if strings.TrimSpace(body.PKCS12Base64) != "" {
		pfx, decodeErr := decodeSMIMEBase64(body.PKCS12Base64)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "The PKCS#12 data is not valid base64")
			return
		}
		identity, err = mailcrypto.ParsePKCS12Identity(pfx, passphrase)
	} else if strings.TrimSpace(body.Certificate) != "" && strings.TrimSpace(body.PrivateKey) != "" {
		identity, err = mailcrypto.ParseSMIMEIdentity(
			[]byte(body.Certificate), []byte(body.PrivateKey), []byte(body.Chain), passphrase,
		)
	} else if strings.TrimSpace(body.Certificate) != "" {
		s.importSMIMEHSMCertificate(w, r, sess, body)
		return
	} else {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", "Provide a PKCS#12 bundle or a certificate and private key")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_KEY", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		email = identity.Email
	}
	if email == "" {
		email = strings.ToLower(sess.User.Email)
	}
	covered := false
	for _, candidate := range mailcrypto.CertificateEmails(identity.Certificate) {
		if strings.EqualFold(candidate, email) {
			covered = true
			break
		}
	}
	if !covered {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "The certificate does not cover that email identity")
		return
	}
	if !s.userOwnsEmail(r.Context(), sess.User, email) {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose one of your own email identities")
		return
	}
	roots, err := mailcrypto.LoadSMIMERoots(s.cfg.SMIMECAFile)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_CA", err.Error())
		return
	}
	check := mailcrypto.CheckSMIMECertificate(identity.Certificate, identity.Chain, roots, time.Now(), email, mailcrypto.SMIMEUsageSign)
	s.applySMIMERevocation(r.Context(), &check, identity.Certificate, identity.Chain)
	if !check.Valid {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_CERTIFICATE", check.Reason)
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
			WHERE user_id=$1 AND protocol='smime' AND fingerprint=$2
			LIMIT 1
		), ''), COALESCE((
			SELECT status FROM user_crypto_identities
			WHERE user_id=$1 AND protocol='smime' AND fingerprint=$2
			LIMIT 1
		), '')
	`, sess.User.ID, identity.Fingerprint).Scan(&existingID, &existingStatus)
	if existingStatus == "revoked" {
		writeError(w, http.StatusConflict, "ENCRYPTION_REVOKED", "This S/MIME identity has been revoked and cannot be reactivated by import")
		return
	}
	identityID := existingID
	if identityID == "" {
		identityID = newID("ckey")
	}
	wrapped, err := vault.seal(sess.User.TenantID, sess.User.ID, identityID, string(identity.PrivateKeyPEM))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not protect the S/MIME private key")
		return
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE user_id=$1 AND protocol='smime' AND status='active' AND id <> $2
	`, sess.User.ID, identityID)
	if err == nil {
		if existingID != "" {
			_, err = tx.Exec(r.Context(), `
				UPDATE user_crypto_identities
				SET email=$3, key_id=$4, public_key=$5, cert_chain=$6, subject=$7, issuer=$8,
				    certificate_serial=$9, not_before=$10, not_after=$11, source='imported',
				    status='active', updated_at=now()
				WHERE id=$1 AND user_id=$2
			`, identityID, sess.User.ID, email, identity.Fingerprint, string(identity.CertificatePEM),
				string(identity.ChainPEM), identity.Subject, identity.Issuer,
				identity.Certificate.SerialNumber.String(), identity.NotBefore, identity.NotAfter)
		} else {
			_, err = tx.Exec(r.Context(), `
				INSERT INTO user_crypto_identities
					(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, cert_chain,
					 subject, issuer, certificate_serial, not_before, not_after, source, status)
				VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,$8,$9,$10,$11,$12,$13,'imported','active')
			`, identityID, sess.User.TenantID, sess.User.ID, email,
				identity.Fingerprint, identity.Fingerprint, string(identity.CertificatePEM), string(identity.ChainPEM),
				identity.Subject, identity.Issuer, identity.Certificate.SerialNumber.String(),
				identity.NotBefore, identity.NotAfter)
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
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	detail := "Imported a server-managed S/MIME identity · " + identity.Fingerprint
	if len(check.Warnings) > 0 {
		detail += " · " + strings.Join(check.Warnings, " · ")
	}
	s.audit(r, sess.User, "encryption.smime_identity_imported", email, detail)
	payload, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "smime")
	writeJSON(w, http.StatusOK, map[string]any{"identity": payload})
}

func (s *Server) activeHSMSMIMEIdentity(ctx context.Context, user userRecord) (*mailcrypto.SMIMEIdentity, error) {
	if s.smimeHSM == nil {
		return nil, fmt.Errorf("no PKCS#11 S/MIME identity is configured")
	}
	hsm := s.smimeHSM.IdentityFor(user.Email)
	if hsm == nil {
		return nil, fmt.Errorf("no PKCS#11 S/MIME identity is configured for %s", user.Email)
	}
	var fingerprint, status string
	if err := s.store.DB.QueryRow(ctx, `
		SELECT fingerprint, status FROM user_crypto_identities
		WHERE user_id=$1 AND tenant_id=$2 AND protocol='smime'
		ORDER BY CASE status WHEN 'active' THEN 0 ELSE 1 END, created_at DESC LIMIT 1
	`, user.ID, user.TenantID).Scan(&fingerprint, &status); err != nil {
		return nil, err
	}
	if status != "active" || !strings.EqualFold(fingerprint, hsm.Fingerprint) {
		return nil, fmt.Errorf("the PKCS#11 certificate is not the active S/MIME identity")
	}
	return hsm.MailCryptoIdentity(), nil
}

func (s *Server) loadActiveSMIMEIdentity(ctx context.Context, user userRecord) (*mailcrypto.SMIMEIdentity, error) {
	var identityID, wrapped, certPEM, chainPEM string
	err := s.store.DB.QueryRow(ctx, `
		SELECT i.id, k.wrapped_key, i.public_key, i.cert_chain
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		WHERE i.user_id=$1 AND i.tenant_id=$2 AND i.protocol='smime' AND i.status='active'
		ORDER BY i.created_at DESC LIMIT 1
	`, user.ID, user.TenantID).Scan(&identityID, &wrapped, &certPEM, &chainPEM)
	if err != nil {
		if hsmIdentity, hsmErr := s.activeHSMSMIMEIdentity(ctx, user); hsmErr == nil {
			return hsmIdentity, nil
		}
		return nil, err
	}
	vault, err := s.encryptionVault()
	if err != nil {
		return nil, err
	}
	privateKey, err := vault.open(user.TenantID, user.ID, identityID, wrapped)
	if err != nil {
		return nil, err
	}
	return mailcrypto.ParseSMIMEIdentity([]byte(certPEM), []byte(privateKey), []byte(chainPEM), nil)
}

// loadSMIMEIdentities returns active and retired identities so rotated
// certificates can still decrypt older mail. Revoked identities are excluded.
func (s *Server) loadSMIMEIdentities(ctx context.Context, user userRecord) ([]*mailcrypto.SMIMEIdentity, error) {
	rows, err := s.store.DB.Query(ctx, `
		SELECT i.id, k.wrapped_key, i.public_key, i.cert_chain
		FROM user_crypto_identities i
		JOIN user_crypto_private_keys k ON k.identity_id=i.id
		WHERE i.user_id=$1 AND i.tenant_id=$2 AND i.protocol='smime'
		  AND i.status IN ('active','retired')
		ORDER BY CASE i.status WHEN 'active' THEN 0 ELSE 1 END, i.created_at DESC
	`, user.ID, user.TenantID)
	if err != nil {
		return nil, err
	}
	type row struct{ id, wrapped, cert, chain string }
	items := []row{}
	for rows.Next() {
		var item row
		if rows.Scan(&item.id, &item.wrapped, &item.cert, &item.chain) == nil {
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
	identities := []*mailcrypto.SMIMEIdentity{}
	if hsmIdentity, hsmErr := s.activeHSMSMIMEIdentity(ctx, user); hsmErr == nil {
		identities = append(identities, hsmIdentity)
	}
	var lastErr error
	for _, item := range items {
		privateKey, err := vault.open(user.TenantID, user.ID, item.id, item.wrapped)
		if err != nil {
			lastErr = err
			continue
		}
		identity, err := mailcrypto.ParseSMIMEIdentity([]byte(item.cert), []byte(privateKey), []byte(item.chain), nil)
		if err != nil {
			lastErr = err
			continue
		}
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no S/MIME identity is available")
	}
	return identities, nil
}

func (s *Server) importSMIMEHSMCertificate(w http.ResponseWriter, r *http.Request, sess *sessionRecord, body encryptionImportBody) {
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose the email address covered by the certificate")
		return
	}
	if !s.userOwnsEmail(r.Context(), sess.User, email) {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "Choose one of your own email identities")
		return
	}
	if s.smimeHSM == nil {
		writeError(w, http.StatusConflict, "HSM_NOT_CONFIGURED", "A private key is required unless this server is configured with a PKCS#11 S/MIME key")
		return
	}
	hsm := s.smimeHSM.IdentityFor(email)
	if hsm == nil {
		writeError(w, http.StatusConflict, "HSM_NOT_CONFIGURED", "No PKCS#11 S/MIME key is configured for "+email)
		return
	}
	certificates, err := mailcrypto.ParseSMIMECertificates([]byte(body.Certificate + "\n" + body.Chain))
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_CERTIFICATE", "The certificate could not be parsed")
		return
	}
	leaf, chain := selectSMIMELeaf(certificates, nil, email)
	if leaf == nil {
		writeError(w, http.StatusBadRequest, "BAD_IDENTITY", "The certificate does not cover that email identity")
		return
	}
	info, err := mailcrypto.CertificateInfoFor(leaf, chain)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_CERTIFICATE", "The certificate is invalid")
		return
	}
	if !strings.EqualFold(info.Fingerprint, hsm.Fingerprint) {
		writeError(w, http.StatusConflict, "HSM_CERTIFICATE_MISMATCH", "The imported certificate does not match the configured PKCS#11 key")
		return
	}
	roots, err := mailcrypto.LoadSMIMERoots(s.cfg.SMIMECAFile)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ENCRYPTION_CA", err.Error())
		return
	}
	check := mailcrypto.CheckSMIMECertificate(leaf, chain, roots, time.Now(), email, mailcrypto.SMIMEUsageSign)
	s.applySMIMERevocation(r.Context(), &check, leaf, chain)
	if !check.Valid {
		writeError(w, http.StatusBadRequest, "ENCRYPTION_CERTIFICATE", check.Reason)
		return
	}
	var existingID, existingStatus string
	_ = s.store.DB.QueryRow(r.Context(), `
		SELECT COALESCE((SELECT id FROM user_crypto_identities WHERE user_id=$1 AND protocol='smime' AND fingerprint=$2 LIMIT 1), ''),
		       COALESCE((SELECT status FROM user_crypto_identities WHERE user_id=$1 AND protocol='smime' AND fingerprint=$2 LIMIT 1), '')
	`, sess.User.ID, info.Fingerprint).Scan(&existingID, &existingStatus)
	if existingStatus == "revoked" {
		writeError(w, http.StatusConflict, "ENCRYPTION_REVOKED", "This S/MIME identity has been revoked and cannot be reactivated by import")
		return
	}
	identityID := existingID
	if identityID == "" {
		identityID = newID("ckey")
	}
	tx, err := s.store.DB.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE user_crypto_identities SET status='retired', updated_at=now()
		WHERE user_id=$1 AND protocol='smime' AND status='active' AND id <> $2
	`, sess.User.ID, identityID); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	if existingID != "" {
		_, err = tx.Exec(r.Context(), `
			UPDATE user_crypto_identities
			SET email=$3, key_id=$4, public_key=$5, cert_chain=$6, subject=$7, issuer=$8,
			    certificate_serial=$9, not_before=$10, not_after=$11, source='imported', status='active', updated_at=now()
			WHERE id=$1 AND user_id=$2
		`, identityID, sess.User.ID, email, info.Fingerprint, string(info.CertificatePEM), string(info.ChainPEM),
			info.Subject, info.Issuer, leaf.SerialNumber.String(), info.NotBefore, info.NotAfter)
	} else {
		_, err = tx.Exec(r.Context(), `
			INSERT INTO user_crypto_identities
				(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, cert_chain,
				 subject, issuer, certificate_serial, not_before, not_after, source, status)
			VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,$8,$9,$10,$11,$12,$13,'imported','active')
		`, identityID, sess.User.TenantID, sess.User.ID, email, info.Fingerprint, info.Fingerprint,
			string(info.CertificatePEM), string(info.ChainPEM), info.Subject, info.Issuer,
			leaf.SerialNumber.String(), info.NotBefore, info.NotAfter)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not store the S/MIME identity")
		return
	}
	s.audit(r, sess.User, "encryption.smime_hsm_certificate_imported", email,
		"Imported a PKCS#11-backed S/MIME certificate · "+info.Fingerprint)
	identity, _ := s.loadActiveCryptoIdentity(r.Context(), sess.User.ID, "smime")
	writeJSON(w, http.StatusOK, map[string]any{"identity": identity, "hsm": true})
}

func (s *Server) smimeRoots() (*x509.CertPool, error) {
	return mailcrypto.LoadSMIMERoots(s.cfg.SMIMECAFile)
}

func (s *Server) storeContactSMIMECertificate(ctx context.Context, sess *sessionRecord, email, certificate, chainData, trust, source string) (storedContactCryptoKey, bool, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || parsed == nil {
		return storedContactCryptoKey{}, false, fmt.Errorf("Choose a valid recipient email")
	}
	email = strings.ToLower(parsed.Address)
	certs, err := mailcrypto.ParseSMIMECertificates([]byte(certificate))
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	chain := []*x509.Certificate{}
	if strings.TrimSpace(chainData) != "" {
		chain, err = mailcrypto.ParseSMIMECertificates([]byte(chainData))
		if err != nil {
			return storedContactCryptoKey{}, false, err
		}
	}
	leaf, ordered := selectSMIMELeaf(certs, chain, email)
	if leaf == nil {
		return storedContactCryptoKey{}, false, fmt.Errorf("the certificate does not cover %s", email)
	}
	roots, err := s.smimeRoots()
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	check := mailcrypto.CheckSMIMECertificate(leaf, ordered, roots, time.Now(), email, mailcrypto.SMIMEUsageEncrypt)
	s.applySMIMERevocation(ctx, &check, leaf, ordered)
	if !check.Valid {
		return storedContactCryptoKey{}, false, fmt.Errorf("%s", check.Reason)
	}
	requestedTrust := strings.ToLower(strings.TrimSpace(trust))
	if requestedTrust != "" && requestedTrust != "unknown" && requestedTrust != "tofu" && requestedTrust != "verified" && requestedTrust != "enterprise" && requestedTrust != "revoked" {
		return storedContactCryptoKey{}, false, fmt.Errorf("Choose a valid trust level")
	}
	if requestedTrust == "" {
		requestedTrust = "unknown"
		if source == "ldap" {
			requestedTrust = "enterprise"
		}
	}
	info, err := mailcrypto.CertificateInfoFor(leaf, ordered)
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT id, fingerprint, trust, verified_at
		FROM contact_crypto_keys
		WHERE user_id=$1 AND tenant_id=$2 AND protocol='smime' AND lower(email::text)=lower($3)
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
	if err := rows.Err(); err != nil {
		rows.Close()
		return storedContactCryptoKey{}, false, err
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
			SET public_key=$4, cert_chain=$5, trust=$6, verified_at=COALESCE(verified_at,$7),
			    subject=$8, issuer=$9, certificate_serial=$10, not_before=$11, not_after=$12,
			    last_seen_at=now(), updated_at=now()
			WHERE id=$1 AND user_id=$2 AND tenant_id=$3
		`, sameID, sess.User.ID, sess.User.TenantID, string(info.CertificatePEM), string(info.ChainPEM),
			trustValue, verifiedAt, info.Subject, info.Issuer, leaf.SerialNumber.String(),
			info.NotBefore, info.NotAfter)
		if err != nil {
			return storedContactCryptoKey{}, false, err
		}
		return storedContactCryptoKey{ID: sameID, Email: email, KeyID: info.Fingerprint, Fingerprint: info.Fingerprint, Trust: trustValue}, keyChanged, nil
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
			(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, cert_chain,
			 trust, source, verified_at, key_changed_at, last_seen_at, subject, issuer,
			 certificate_serial, not_before, not_after)
		VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,$8,$9,$10,$11,
		        CASE WHEN $12 THEN now() ELSE NULL END, now(), $13,$14,$15,$16,$17)
	`, id, sess.User.TenantID, sess.User.ID, email, info.Fingerprint, info.Fingerprint,
		string(info.CertificatePEM), string(info.ChainPEM), trustValue, source, verifiedAt, keyChanged,
		info.Subject, info.Issuer, leaf.SerialNumber.String(), info.NotBefore, info.NotAfter)
	if err != nil {
		return storedContactCryptoKey{}, false, err
	}
	return storedContactCryptoKey{ID: id, Email: email, KeyID: info.Fingerprint, Fingerprint: info.Fingerprint, Trust: trustValue, KeyChanged: keyChanged}, keyChanged, nil
}

func selectSMIMELeaf(certs, chain []*x509.Certificate, email string) (*x509.Certificate, []*x509.Certificate) {
	all := append(append([]*x509.Certificate{}, certs...), chain...)
	for _, cert := range certs {
		for _, candidate := range cert.EmailAddresses {
			if strings.EqualFold(strings.TrimSpace(candidate), email) {
				return cert, otherCertificates(all, cert)
			}
		}
	}
	return nil, nil
}

func otherCertificates(all []*x509.Certificate, leaf *x509.Certificate) []*x509.Certificate {
	out := make([]*x509.Certificate, 0, len(all))
	for _, cert := range all {
		if cert != nil && !bytes.Equal(cert.Raw, leaf.Raw) {
			out = append(out, cert)
		}
	}
	return out
}
