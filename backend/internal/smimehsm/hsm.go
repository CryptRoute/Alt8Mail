// Package smimehsm supplies server-managed S/MIME private operations from a
// PKCS#11 token. The certificate and chain stay in configuration; the private
// key never leaves the token. Build with -tags pkcs11 on a platform with cgo
// to enable the token backend.
package smimehsm

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

type IdentityConfig struct {
	Email          string `json:"email"`
	CertificatePEM string `json:"certificatePem"`
	ChainPEM       string `json:"chainPem"`
	KeyLabel       string `json:"keyLabel"`
	KeyID          string `json:"keyId"`
	PIN            string `json:"pin"`
}

type Config struct {
	Module     string           `json:"module"`
	TokenLabel string           `json:"tokenLabel"`
	PIN        string           `json:"pin"`
	Identities []IdentityConfig `json:"identities"`
}

type tokenSession interface {
	Sign(digestInfo []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
	Close()
}

// Identity is one certificate paired with a token-backed private key.
type Identity struct {
	Email          string
	Fingerprint    string
	CertificatePEM string
	ChainPEM       string
	Certificate    *x509.Certificate
	Chain          []*x509.Certificate
	signer         *tokenSigner
}

type tokenSigner struct {
	certificate *x509.Certificate
	session     tokenSession
}

func (s *tokenSigner) Public() crypto.PublicKey {
	return s.certificate.PublicKey
}

func (s *tokenSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil {
		return nil, fmt.Errorf("a digest algorithm is required for S/MIME signing")
	}
	hash := opts.HashFunc()
	info, err := digestInfo(hash, digest)
	if err != nil {
		return nil, err
	}
	return s.session.Sign(info)
}

func (s *tokenSigner) Decrypt(_ io.Reader, ciphertext []byte, _ crypto.DecrypterOpts) ([]byte, error) {
	return s.session.Decrypt(ciphertext)
}

var digestPrefixes = map[crypto.Hash]string{
	crypto.MD5:    "3020300c06082a864886f70d020505000410",
	crypto.SHA1:   "3021300906052b0e03021a05000414",
	crypto.SHA224: "302d300d06096086480165030402040500041c",
	crypto.SHA256: "3031300d060960864801650304020105000420",
	crypto.SHA384: "3041300d060960864801650304020205000430",
	crypto.SHA512: "3051300d060960864801650304020305000440",
}

func digestInfo(hash crypto.Hash, digest []byte) ([]byte, error) {
	if !hash.Available() {
		return nil, fmt.Errorf("S/MIME hash %v is not available", hash)
	}
	if len(digest) != hash.Size() {
		return nil, fmt.Errorf("S/MIME digest has %d bytes, expected %d", len(digest), hash.Size())
	}
	prefix, ok := digestPrefixes[hash]
	if !ok {
		return nil, fmt.Errorf("S/MIME hash %v is not supported by the PKCS#11 signer", hash)
	}
	prefixBytes, err := hex.DecodeString(prefix)
	if err != nil {
		return nil, err
	}
	return append(prefixBytes, digest...), nil
}

// Provider holds configured HSM identities. It is safe for concurrent use.
type Provider struct {
	mu         sync.RWMutex
	identities map[string]*Identity
	sessions   []tokenSession
}

func OpenJSON(raw string) (*Provider, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("parse S/MIME PKCS#11 configuration: %w", err)
	}
	return Open(cfg)
}

func Open(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.Module) == "" {
		return nil, fmt.Errorf("S/MIME PKCS#11 module path is required")
	}
	if len(cfg.Identities) == 0 {
		return nil, fmt.Errorf("S/MIME PKCS#11 configuration has no identities")
	}
	provider := &Provider{identities: map[string]*Identity{}}
	for _, item := range cfg.Identities {
		email := strings.ToLower(strings.TrimSpace(item.Email))
		if email == "" {
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 identity is missing its email")
		}
		if _, exists := provider.identities[email]; exists {
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 identity %s is configured twice", email)
		}
		certificates, err := mailcrypto.ParseSMIMECertificates([]byte(item.CertificatePEM + item.ChainPEM))
		if err != nil || len(certificates) == 0 {
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 identity %s has no usable certificate", email)
		}
		var leaf *x509.Certificate
		for _, certificate := range certificates {
			for _, candidate := range certificate.EmailAddresses {
				if strings.EqualFold(strings.TrimSpace(candidate), email) {
					leaf = certificate
					break
				}
			}
			if leaf != nil {
				break
			}
		}
		if leaf == nil {
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 certificate for %s has no matching email SAN", email)
		}
		if _, ok := leaf.PublicKey.(*rsa.PublicKey); !ok {
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 identity %s must use an RSA certificate", email)
		}
		session, err := openBackend(cfg, item)
		if err != nil {
			provider.Close()
			return nil, fmt.Errorf("open S/MIME PKCS#11 identity %s: %w", email, err)
		}
		chain := make([]*x509.Certificate, 0, len(certificates)-1)
		for _, certificate := range certificates {
			if !strings.EqualFold(mailcrypto.SMIMEFingerprint(certificate), mailcrypto.SMIMEFingerprint(leaf)) {
				chain = append(chain, certificate)
			}
		}
		info, err := mailcrypto.CertificateInfoFor(leaf, chain)
		if err != nil {
			session.Close()
			provider.Close()
			return nil, fmt.Errorf("read S/MIME PKCS#11 certificate for %s: %w", email, err)
		}
		signer := &tokenSigner{certificate: leaf, session: session}
		selfTest := sha256.Sum256([]byte("alt8mail-pkcs11-selftest|" + email))
		signature, err := signer.Sign(rand.Reader, selfTest[:], crypto.SHA256)
		if err != nil || rsa.VerifyPKCS1v15(leaf.PublicKey.(*rsa.PublicKey), crypto.SHA256, selfTest[:], signature) != nil {
			session.Close()
			provider.Close()
			return nil, fmt.Errorf("S/MIME PKCS#11 key for %s does not match its certificate", email)
		}
		provider.sessions = append(provider.sessions, session)
		provider.identities[email] = &Identity{
			Email: email, Fingerprint: info.Fingerprint,
			CertificatePEM: string(info.CertificatePEM), ChainPEM: string(info.ChainPEM),
			Certificate: leaf, Chain: chain,
			signer: signer,
		}
	}
	return provider, nil
}

func (p *Provider) IdentityFor(email string) *Identity {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.identities[strings.ToLower(strings.TrimSpace(email))]
}

func (p *Provider) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, session := range p.sessions {
		session.Close()
	}
	p.sessions = nil
	p.identities = map[string]*Identity{}
}

// MailCryptoIdentity adapts the token-backed key to the mailcrypto S/MIME
// pipeline. Signing and decryption both go through the token.
func (i *Identity) MailCryptoIdentity() *mailcrypto.SMIMEIdentity {
	if i == nil {
		return nil
	}
	return mailcrypto.SMIMEIdentityForSigner(i.Certificate, i.Chain, i.signer)
}
