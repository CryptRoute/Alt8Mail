package httpapi

import (
	"net/http"
	"strings"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

// verifyMessageSignature verifies a signed-but-not-encrypted message against
// the sender's saved contact key or certificate. Encrypted messages verify
// their inner signature during decryption instead.
func (s *Server) verifyMessageSignature(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID).Encryption
	if !encryptionEnabled(policy) {
		writeError(w, http.StatusForbidden, "ENCRYPTION_DISABLED", "Encryption is not enabled for this organization")
		return
	}
	messageID := strings.TrimSpace(r.PathValue("id"))
	if messageID == "" {
		writeError(w, http.StatusBadRequest, "MESSAGE", "Choose a message to verify")
		return
	}
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	var message map[string]any
	if loaded, _, err := s.loadMailMessagesByIDs(r.Context(), sess.User.ID, []string{messageID}); err == nil && len(loaded) > 0 {
		message = loaded[0]
	} else {
		message = findMessageIn(state, messageID)
	}
	if message == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Message not found")
		return
	}
	crypto := stateMap(message, "crypto")
	if stateBool(crypto, "encrypted") {
		writeError(w, http.StatusConflict, "ENCRYPTED_MESSAGE", "Decrypt this message to verify its inner signature")
		return
	}
	if !stateBool(crypto, "signed") {
		writeError(w, http.StatusConflict, "NOT_SIGNED", "This message is not signed")
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
	from := stateMap(message, "from")
	senderEmail := strings.ToLower(strings.TrimSpace(stateString(from, "email")))
	if senderEmail == "" {
		writeError(w, http.StatusBadRequest, "SENDER", "The message has no sender address")
		return
	}
	provider, err := cryptoProviderFor(protocol)
	if err != nil {
		writeError(w, http.StatusConflict, "ENCRYPTION_PROTOCOL", err.Error())
		return
	}
	rows, err := s.store.DB.Query(r.Context(), `
		SELECT public_key
		FROM contact_crypto_keys
		WHERE user_id=$1 AND tenant_id=$2 AND protocol=$3 AND lower(email::text)=lower($4)
		ORDER BY CASE trust
			WHEN 'enterprise' THEN 0 WHEN 'verified' THEN 1 WHEN 'tofu' THEN 2 ELSE 3 END,
			updated_at DESC
	`, sess.User.ID, sess.User.TenantID, protocol, senderEmail)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not load the sender's public key")
		return
	}
	type candidate struct{ publicKey string }
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if rows.Scan(&item.publicKey) == nil {
			candidates = append(candidates, item)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "ENCRYPTION_KEY", "Could not load the sender's public key")
		return
	}
	rows.Close()
	if len(candidates) == 0 {
		writeError(w, http.StatusConflict, "SIGNER_KEY", "Save the sender's public key or certificate in People before verifying this signature")
		return
	}
	raw, _, _ := s.fetchRawMessage(r.Context(), sess.User, state, message)
	var status mailcrypto.SignatureStatus
	var lastErr error
	for _, item := range candidates {
		request := mailcrypto.VerifyRequest{
			Protocol:  mailcrypto.Protocol(protocol),
			PublicKey: []byte(item.publicKey),
			Message:   raw,
		}
		if protocol == "smime" {
			request.PublicKey = []byte(item.publicKey)
		}
		candidateStatus, verifyErr := provider.Verify(r.Context(), request)
		if verifyErr != nil {
			lastErr = verifyErr
			continue
		}
		status = candidateStatus
		if status.State == mailcrypto.SignatureValid || status.State == mailcrypto.SignatureValidUntrusted {
			break
		}
		lastErr = nil
	}
	if status.State == "" {
		if lastErr != nil {
			writeError(w, http.StatusUnprocessableEntity, "VERIFY_FAILED", "The signature could not be verified")
			return
		}
		status = mailcrypto.SignatureStatus{State: mailcrypto.SignatureInvalid}
	}
	if status.SignerEmail != "" && !strings.EqualFold(status.SignerEmail, senderEmail) {
		status.State = mailcrypto.SignatureInvalid
		status.Reason = "signature signer does not match the message From address"
	}
	s.applyDecryptedSignatureTrust(r.Context(), sess.User, protocol, &status)
	s.audit(r, sess.User, "encryption.signature_verified", stateString(message, "subject"), "Verified a signed message signature")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "protocol": protocol, "signature": status,
	})
}
