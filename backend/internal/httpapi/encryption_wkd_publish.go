package httpapi

import (
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
)

// wkdPublishedKey serves the advanced-method Web Key Directory endpoint for
// browser-held keys:
//
//	https://<email-domain>/.well-known/openpgpkey/hu/<hash>?l=<local-part>
func (s *Server) wkdPublishedKey(w http.ResponseWriter, r *http.Request) {
	host := requestHostname(r)
	s.servePublishedWKDKey(w, r, host, r.PathValue("hash"))
}

// wkdPublishedKeyDirect serves the direct-method URL:
//
//	https://openpgpkey.<email-domain>/.well-known/openpgpkey/<domain>/hu/<hash>?l=<local-part>
func (s *Server) wkdPublishedKeyDirect(w http.ResponseWriter, r *http.Request) {
	host := requestHostname(r)
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.PathValue("domain")), "."))
	if host != "openpgpkey."+domain {
		http.NotFound(w, r)
		return
	}
	s.servePublishedWKDKey(w, r, domain, r.PathValue("hash"))
}

func requestHostname(r *http.Request) string {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if index := strings.LastIndex(host, ":"); index > 0 && !strings.Contains(host, "]") {
		host = host[:index]
	}
	return strings.TrimSuffix(host, ".")
}

func (s *Server) servePublishedWKDKey(w http.ResponseWriter, r *http.Request, domain, hash string) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	local := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("l")))
	if domain == "" || local == "" || mailcrypto.WKDLocalPartHash(local) != strings.ToLower(strings.TrimSpace(hash)) {
		http.NotFound(w, r)
		return
	}
	address, err := mail.ParseAddress(local + "@" + domain)
	if err != nil || address == nil {
		http.NotFound(w, r)
		return
	}
	var tenantID, publicKey string
	err = s.store.DB.QueryRow(r.Context(), `
		SELECT i.tenant_id::text, i.public_key
		FROM public.user_crypto_identities i
		JOIN public.users u ON u.id=i.user_id AND u.status='active'
		JOIN public.tenants t ON t.id=i.tenant_id
		JOIN public.mail_domains d ON d.tenant_id=i.tenant_id AND lower(d.domain::text)=lower($2)
		WHERE i.protocol='openpgp' AND i.status='active' AND i.source='client'
		  AND lower(i.email::text)=lower($1)
		  AND d.status IN ('verified','manual')
		ORDER BY i.created_at DESC LIMIT 1
	`, address.Address, domain).Scan(&tenantID, &publicKey)
	if err != nil || tenantID == "" || len(publicKey) > 128<<10 {
		http.NotFound(w, r)
		return
	}
	if !s.publishedWKDKeyAllowed(r, tenantID) {
		http.NotFound(w, r)
		return
	}
	entity, err := mailcrypto.ParseOpenPGPPublicKey([]byte(publicKey))
	if err != nil || !mailcrypto.OpenPGPKeyUsableForEncryption(entity, address.Address, time.Now()) {
		http.NotFound(w, r)
		return
	}
	binary, err := mailcrypto.PublicKeyBinary(entity)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(binary)
}

func (s *Server) publishedWKDKeyAllowed(r *http.Request, tenantID string) bool {
	policy := s.tenantPolicyFor(r.Context(), tenantID).Encryption
	return policy.Mode == "client_side" && encryptionAllowsProtocol(policy, "openpgp")
}
