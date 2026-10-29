package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
)

type webAuthnUser struct {
	user        userRecord
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte                         { return []byte(u.user.ID) }
func (u *webAuthnUser) WebAuthnName() string                       { return u.user.Email }
func (u *webAuthnUser) WebAuthnDisplayName() string                { return u.user.Name }
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

type ceremonySession struct {
	Session     webauthn.SessionData `json:"session"`
	UserID      string               `json:"userId"`
	ChallengeID string               `json:"challengeId,omitempty"`
	Kind        string               `json:"kind"`
	Remember    bool                 `json:"remember"`
}

func (s *Server) newWebAuthnUser(ctx context.Context, user userRecord) (*webAuthnUser, error) {
	_, stored := s.loadPasskeys(ctx, user.ID)
	credentials := make([]webauthn.Credential, 0, len(stored))
	for _, passkey := range stored {
		transports := make([]protocol.AuthenticatorTransport, 0, len(passkey.Transports))
		for _, transport := range passkey.Transports {
			transports = append(transports, protocol.AuthenticatorTransport(transport))
		}
		var aaguid []byte
		if passkey.AAGUID != "" {
			aaguid, _ = hex.DecodeString(passkey.AAGUID)
		}
		credentials = append(credentials, webauthn.Credential{
			ID:        passkey.Credential,
			PublicKey: passkey.PublicKey,
			Transport: transports,
			Authenticator: webauthn.Authenticator{
				AAGUID:    aaguid,
				SignCount: passkey.SignCount,
			},
		})
	}
	return &webAuthnUser{user: user, credentials: credentials}, nil
}

func (s *Server) requireWebAuthn(w http.ResponseWriter) bool {
	if s.webAuthn == nil {
		writeError(w, http.StatusServiceUnavailable, "WEBAUTHN_UNAVAILABLE", "Passkeys are not configured for this host")
		return false
	}
	return true
}

func (s *Server) storeCeremony(ctx context.Context, flow string, data ceremonySession) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.store.Redis.Set(ctx, "webauthn:"+flow, encoded, 5*time.Minute).Err()
}

func (s *Server) loadCeremony(ctx context.Context, flow string) (*ceremonySession, error) {
	raw, err := s.store.Redis.Get(ctx, "webauthn:"+flow).Bytes()
	if err != nil {
		return nil, err
	}
	var data ceremonySession
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (s *Server) deleteCeremony(ctx context.Context, flow string) {
	_, _ = s.store.Redis.Del(ctx, "webauthn:"+flow).Result()
}

func (s *Server) beginWebAuthnRegistration(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !s.requireWebAuthn(w) {
		return
	}
	user, err := s.newWebAuthnUser(r.Context(), sess.User)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not load passkey state")
		return
	}
	exclusions := []protocol.CredentialDescriptor{}
	for _, credential := range user.credentials {
		exclusions = append(exclusions, credential.Descriptor())
	}
	creation, session, err := s.webAuthn.BeginRegistration(user, webauthn.WithExclusions(exclusions))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not start passkey registration")
		return
	}
	flow := randomToken(24)
	if err := s.storeCeremony(r.Context(), flow, ceremonySession{
		Session: *session, UserID: sess.User.ID, Kind: "registration",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not store passkey ceremony")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId": flow, "publicKey": creation.Response,
		"expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	})
}

func (s *Server) finishWebAuthnRegistration(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	s.finishRegistrationCommon(w, r, sess.User, "settings", "")
}

func (s *Server) finishRegistrationCommon(w http.ResponseWriter, r *http.Request, user userRecord, kind, challengeID string) {
	if !s.requireWebAuthn(w) {
		return
	}
	flow := r.URL.Query().Get("flow")
	ceremony, err := s.loadCeremony(r.Context(), flow)
	if err != nil || ceremony.UserID != user.ID || ceremony.Kind != "registration" {
		writeError(w, http.StatusGone, "WEBAUTHN_FLOW", "This passkey request has expired")
		return
	}
	webUser, err := s.newWebAuthnUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not load passkey state")
		return
	}
	credential, err := s.webAuthn.FinishRegistration(webUser, ceremony.Session, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "WEBAUTHN_REGISTRATION", "Passkey registration could not be verified")
		return
	}
	transports := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		transports = append(transports, string(transport))
	}
	name := "Passkey · " + describeSessionDevice(r.UserAgent())
	aaguid := hex.EncodeToString(credential.Authenticator.AAGUID)
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO webauthn_credentials
			(user_id, credential_id, public_key, sign_count, transports, aaguid, name, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
	`, user.ID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, transports, aaguid, name)
	if err != nil {
		s.logger.Warn("store passkey", "err", err)
		writeError(w, http.StatusConflict, "WEBAUTHN_STORE", "That passkey is already registered")
		return
	}
	s.revokeTrustedDevices(r.Context(), user.ID)
	s.deleteCeremony(r.Context(), flow)
	_ = s.updateUserSecurity(r.Context(), user.ID, func(security map[string]any) error {
		twoFactor, _ := security["twoFactor"].(map[string]any)
		if twoFactor == nil {
			twoFactor = map[string]any{}
		}
		if stateString(twoFactor, "enabledAt") == "" {
			twoFactor["enabledAt"] = nowISO()
		}
		security["twoFactor"] = twoFactor
		return nil
	})
	var generatedCodes []string
	if s.unusedRecoveryCodeCount(r.Context(), user.ID) == 0 {
		generatedCodes, _ = s.replaceRecoveryCodes(r.Context(), user.ID)
	}
	if kind == "settings" {
		s.audit(r, user, "security.passkey_added", name, "Registered a WebAuthn passkey")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "codes": generatedCodes, "security": s.mustSecuritySummary(r.Context(), user)})
		return
	}
	// First-time enrollment.
	if challengeID != "" {
		s.deleteAuthChallenge(r.Context(), challengeID)
	}
	s.audit(r, user, "auth.enrollment_completed", user.Email, "First-time WebAuthn enrollment completed")
	s.completeLogin(w, r, user, ceremony.Remember, "Password + WebAuthn enrollment", map[string]any{
		"twoFactorMethod": "passkey", "enrollmentCompleted": true,
	})
}

func (s *Server) beginWebAuthnLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string `json:"challengeId"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), body.ChallengeID, "two_factor")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "TWO_FACTOR_CHALLENGE", "This verification request has expired")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "TWO_FACTOR_CHALLENGE", "This verification request has expired")
		return
	}
	webUser, err := s.newWebAuthnUser(r.Context(), user)
	if err != nil || len(webUser.credentials) == 0 {
		writeError(w, http.StatusConflict, "PASSKEY_NOT_ENROLLED", "No passkey is registered for this account")
		return
	}
	assertion, session, err := s.webAuthn.BeginLogin(webUser)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not start passkey verification")
		return
	}
	flow := randomToken(24)
	if err := s.storeCeremony(r.Context(), flow, ceremonySession{
		Session: *session, UserID: user.ID, ChallengeID: challenge.ID, Kind: "login", Remember: challenge.Remember,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not store passkey ceremony")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId": flow, "publicKey": assertion.Response,
		"expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	})
}

func (s *Server) finishWebAuthnLogin(w http.ResponseWriter, r *http.Request) {
	flow := r.URL.Query().Get("flow")
	ceremony, err := s.loadCeremony(r.Context(), flow)
	if err != nil || ceremony.Kind != "login" || ceremony.ChallengeID == "" {
		writeError(w, http.StatusGone, "WEBAUTHN_FLOW", "This passkey request has expired")
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), ceremony.ChallengeID, "two_factor")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusGone, "TWO_FACTOR_EXPIRED", "This verification request has expired")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "TWO_FACTOR_CHALLENGE", "This verification request has expired")
		return
	}
	webUser, err := s.newWebAuthnUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not load passkey state")
		return
	}
	credential, err := s.webAuthn.FinishLogin(webUser, ceremony.Session, r)
	if err != nil {
		remaining := s.failAuthChallenge(r.Context(), challenge)
		s.audit(r, user, "auth.2fa.failed", user.Email, "Failed WebAuthn passkey verification")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{
			"code": "TWO_FACTOR_INVALID", "message": "That passkey could not be verified.",
			"details": map[string]any{"attemptsRemaining": remaining},
		}})
		return
	}
	transports := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		transports = append(transports, string(transport))
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		UPDATE webauthn_credentials
		SET sign_count = $2, transports = $3, last_used_at = now()
		WHERE user_id = $1 AND credential_id = $4
	`, user.ID, credential.Authenticator.SignCount, transports, credential.ID)
	s.deleteCeremony(r.Context(), flow)
	s.deleteAuthChallenge(r.Context(), challenge.ID)
	trustDevice := r.URL.Query().Get("trustDevice") == "1"
	if trustDevice {
		s.issueTrustedDevice(w, r, user)
	}
	detail := "Password + WebAuthn passkey"
	if trustDevice {
		detail += " · trusted device"
	}
	s.audit(r, user, "auth.2fa", user.Email, detail)
	s.completeLogin(w, r, user, challenge.Remember, detail, map[string]any{
		"twoFactorMethod": "passkey", "trustedDevice": trustDevice,
	})
}

func (s *Server) beginEnrollmentWebAuthn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), body.Token, "enrollment")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	if !s.requireWebAuthn(w) {
		return
	}
	webUser, err := s.newWebAuthnUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not start passkey enrollment")
		return
	}
	exclusions := []protocol.CredentialDescriptor{}
	for _, credential := range webUser.credentials {
		exclusions = append(exclusions, credential.Descriptor())
	}
	creation, session, err := s.webAuthn.BeginRegistration(webUser, webauthn.WithExclusions(exclusions))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not start passkey enrollment")
		return
	}
	flow := randomToken(24)
	if err := s.storeCeremony(r.Context(), flow, ceremonySession{
		Session: *session, UserID: user.ID, ChallengeID: challenge.ID, Kind: "registration", Remember: challenge.Remember,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not store passkey ceremony")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId": flow, "publicKey": creation.Response,
		"expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	})
}

func (s *Server) finishEnrollmentWebAuthn(w http.ResponseWriter, r *http.Request) {
	flow := r.URL.Query().Get("flow")
	ceremony, err := s.loadCeremony(r.Context(), flow)
	if err != nil || ceremony.Kind != "registration" || ceremony.ChallengeID == "" {
		writeError(w, http.StatusGone, "WEBAUTHN_FLOW", "This passkey request has expired")
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), ceremony.ChallengeID, "enrollment")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	// Reconstruct a request with the session data user and finish via the
	// shared registration helper.
	s.finishRegistrationWithUser(w, r, user, "enrollment", challenge.ID, ceremony)
}

func (s *Server) finishRegistrationWithUser(w http.ResponseWriter, r *http.Request, user userRecord, kind, challengeID string, ceremony *ceremonySession) {
	if !s.requireWebAuthn(w) {
		return
	}
	webUser, err := s.newWebAuthnUser(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not load passkey state")
		return
	}
	credential, err := s.webAuthn.FinishRegistration(webUser, ceremony.Session, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "WEBAUTHN_REGISTRATION", "Passkey registration could not be verified")
		return
	}
	transports := make([]string, 0, len(credential.Transport))
	for _, transport := range credential.Transport {
		transports = append(transports, string(transport))
	}
	name := "Passkey · " + describeSessionDevice(r.UserAgent())
	aaguid := hex.EncodeToString(credential.Authenticator.AAGUID)
	_, err = s.store.DB.Exec(r.Context(), `
		INSERT INTO webauthn_credentials
			(user_id, credential_id, public_key, sign_count, transports, aaguid, name, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
	`, user.ID, credential.ID, credential.PublicKey, credential.Authenticator.SignCount, transports, aaguid, name)
	if err != nil {
		writeError(w, http.StatusConflict, "WEBAUTHN_STORE", "That passkey is already registered")
		return
	}
	s.revokeTrustedDevices(r.Context(), user.ID)
	s.deleteCeremony(r.Context(), r.URL.Query().Get("flow"))
	if challengeID != "" {
		s.deleteAuthChallenge(r.Context(), challengeID)
	}
	var generatedCodes []string
	if s.unusedRecoveryCodeCount(r.Context(), user.ID) == 0 {
		generatedCodes, _ = s.replaceRecoveryCodes(r.Context(), user.ID)
	}
	s.audit(r, user, "auth.enrollment_completed", user.Email, "First-time WebAuthn enrollment completed")
	s.completeLogin(w, r, user, ceremony.Remember, "Password + WebAuthn enrollment", map[string]any{
		"twoFactorMethod": "passkey", "enrollmentCompleted": true, "recoveryCodes": generatedCodes,
	})
}

func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "PASSKEY_ID", "Passkey id is required")
		return
	}
	tag, err := s.store.DB.Exec(r.Context(), `DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2`, id, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "WEBAUTHN", "Could not remove that passkey")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Passkey not found")
		return
	}
	refreshed, err := s.userByID(r.Context(), sess.User.ID)
	if err != nil {
		refreshed = sess.User
	}
	if _, stored := s.loadPasskeys(r.Context(), sess.User.ID); len(stored) == 0 {
		security := map[string]any{}
		_ = json.Unmarshal(refreshed.Security, &security)
		twoFactor, _ := security["twoFactor"].(map[string]any)
		if stateString(twoFactor, "totpSecret") == "" {
			_, _ = s.store.DB.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id = $1`, sess.User.ID)
		}
	}
	s.auditRequest(r, sess, "security.passkey_removed", "WebAuthn passkey", "Removed a registered passkey")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "security": s.mustSecuritySummary(r.Context(), refreshed)})
}

// BeginEnrollmentTOTP and FinishEnrollmentTOTP complete the first-login flow
// without creating a full session until a second factor exists.
func (s *Server) beginEnrollmentTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), body.Token, "enrollment")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not start authenticator setup")
		return
	}
	if err := s.store.Redis.Set(r.Context(), "totp-enroll:"+challenge.ID, secret, totpPendingTTL).Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not store authenticator setup state")
		return
	}
	issuer := "Alt8Mail"
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": secret, "issuer": issuer, "account": user.Email,
		"otpauth":   auth.TOTPURI(secret, user.Email, issuer),
		"expiresAt": time.Now().Add(totpPendingTTL).UTC().Format(time.RFC3339),
	})
}

func (s *Server) finishEnrollmentTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if !s.decodeBody(w, r, &body, 64*1024) {
		return
	}
	challenge, err := s.authChallengeByID(r.Context(), body.Token, "enrollment")
	if err != nil || time.Now().After(challenge.ExpiresAt) {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	user, err := s.userByID(r.Context(), challenge.UserID)
	if err != nil {
		writeError(w, http.StatusGone, "ENROLLMENT", "The enrollment session expired. Sign in again.")
		return
	}
	secret, err := s.store.Redis.Get(r.Context(), "totp-enroll:"+challenge.ID).Result()
	if err != nil || secret == "" {
		writeError(w, http.StatusGone, "TOTP_EXPIRED", "Authenticator setup expired. Start again.")
		return
	}
	if !auth.ValidateTOTP(secret, body.Code, time.Now(), 1) {
		writeError(w, http.StatusUnauthorized, "TOTP_INVALID", "That authenticator code did not match")
		return
	}
	if err := s.enableTOTP(r.Context(), user.ID, secret); err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not save authenticator settings")
		return
	}
	s.revokeTrustedDevices(r.Context(), user.ID)
	codes, err := s.replaceRecoveryCodes(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOTP", "Could not generate recovery codes")
		return
	}
	_ = s.store.Redis.Del(r.Context(), "totp-enroll:"+challenge.ID).Err()
	s.deleteAuthChallenge(r.Context(), challenge.ID)
	s.audit(r, user, "auth.enrollment_completed", user.Email, "First-time TOTP enrollment completed")
	refreshed, err := s.userByID(r.Context(), user.ID)
	if err == nil {
		user = refreshed
	}
	s.completeLogin(w, r, user, challenge.Remember, "Password + authenticator enrollment", map[string]any{
		"twoFactorMethod": "totp", "enrollmentCompleted": true, "recoveryCodes": codes,
	})
}
