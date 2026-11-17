//go:build pkcs11 && cgo

package smimehsm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/miekg/pkcs11"
)

type pkcs11Session struct {
	mu      sync.Mutex
	ctx     *pkcs11.Ctx
	session pkcs11.SessionHandle
	key     pkcs11.ObjectHandle
}

func openBackend(cfg Config, identity IdentityConfig) (tokenSession, error) {
	if strings.TrimSpace(identity.KeyLabel) == "" && strings.TrimSpace(identity.KeyID) == "" {
		return nil, fmt.Errorf("a PKCS#11 key label or key id is required")
	}
	ctx := pkcs11.New(cfg.Module)
	if ctx == nil {
		return nil, fmt.Errorf("PKCS#11 module %q could not be loaded", cfg.Module)
	}
	if err := ctx.Initialize(); err != nil && err != pkcs11.Error(pkcs11.CKR_CRYPTOKI_ALREADY_INITIALIZED) {
		ctx.Destroy()
		return nil, fmt.Errorf("initialize PKCS#11 module: %w", err)
	}
	slots, err := ctx.GetSlotList(true)
	if err != nil {
		ctx.Finalize()
		ctx.Destroy()
		return nil, fmt.Errorf("list PKCS#11 slots: %w", err)
	}
	pin := strings.TrimSpace(identity.PIN)
	if pin == "" {
		pin = cfg.PIN
	}
	if pin == "" {
		ctx.Finalize()
		ctx.Destroy()
		return nil, fmt.Errorf("a PKCS#11 user PIN is required")
	}
	template := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PRIVATE_KEY),
	}
	if label := strings.TrimSpace(identity.KeyLabel); label != "" {
		template = append(template, pkcs11.NewAttribute(pkcs11.CKA_LABEL, label))
	}
	if keyID := strings.TrimSpace(identity.KeyID); keyID != "" {
		template = append(template, pkcs11.NewAttribute(pkcs11.CKA_ID, []byte(keyID)))
	}
	for _, slot := range slots {
		info, err := ctx.GetTokenInfo(slot)
		if err != nil {
			continue
		}
		if tokenLabel := strings.TrimSpace(cfg.TokenLabel); tokenLabel != "" && strings.TrimSpace(info.Label) != tokenLabel {
			continue
		}
		session, err := ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION)
		if err != nil {
			continue
		}
		if err := ctx.Login(session, pkcs11.CKU_USER, pin); err != nil && err != pkcs11.Error(pkcs11.CKR_USER_ALREADY_LOGGED_IN) {
			ctx.CloseSession(session)
			continue
		}
		if err := ctx.FindObjectsInit(session, template); err != nil {
			ctx.Logout(session)
			ctx.CloseSession(session)
			continue
		}
		objects, _, err := ctx.FindObjects(session, 2)
		_ = ctx.FindObjectsFinal(session)
		if err != nil || len(objects) == 0 {
			ctx.Logout(session)
			ctx.CloseSession(session)
			continue
		}
		return &pkcs11Session{ctx: ctx, session: session, key: objects[0]}, nil
	}
	ctx.Finalize()
	ctx.Destroy()
	return nil, fmt.Errorf("no matching private key was found on the configured PKCS#11 token")
}

func (s *pkcs11Session) Sign(digestInfo []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.SignInit(s.session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS, nil)}, s.key); err != nil {
		return nil, fmt.Errorf("PKCS#11 SignInit: %w", err)
	}
	signature, err := s.ctx.Sign(s.session, digestInfo)
	if err != nil {
		return nil, fmt.Errorf("PKCS#11 Sign: %w", err)
	}
	return signature, nil
}

func (s *pkcs11Session) Decrypt(ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.DecryptInit(s.session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS, nil)}, s.key); err != nil {
		return nil, fmt.Errorf("PKCS#11 DecryptInit: %w", err)
	}
	plaintext, err := s.ctx.Decrypt(s.session, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("PKCS#11 Decrypt: %w", err)
	}
	return plaintext, nil
}

func (s *pkcs11Session) Close() {
	if s == nil || s.ctx == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.ctx.Logout(s.session)
	_ = s.ctx.CloseSession(s.session)
	_ = s.ctx.Finalize()
	_ = s.ctx.Destroy()
	s.ctx = nil
}
