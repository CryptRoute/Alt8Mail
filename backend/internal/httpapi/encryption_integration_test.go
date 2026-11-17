package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/mailcrypto"
	"github.com/CryptRoute/Alt8Mail/backend/internal/store"
	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestClientEncryptionIntegration requires A8M_SCIM_TEST_DSN with all
// migrations applied. It exercises the real PostgreSQL paths for publishing a
// browser-held key, reading the directory and refusing to convert a
// server-managed identity into a browser one.
func TestClientEncryptionIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the client encryption integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4074-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4075-8000-%012d", time.Now().UnixNano()%1000000000000)
	serverUserID := fmt.Sprintf("00000000-0000-4076-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	clientEmail := "client-" + suffix + "@encryption.example.test"
	serverEmail := "server-" + suffix + "@encryption.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.tenants (id, slug, name, settings)
		VALUES ($1,$2,'Encryption integration',
		        '{"encryption":{"mode":"server_managed","protocol":"both","allowUserImport":true,"allowDelegatedDecrypt":false}}'::jsonb)
	`, tenant, "enc-"+suffix); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, email, name string }{
		{userID, clientEmail, "Client User"},
		{serverUserID, serverEmail, "Server User"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.users (id, tenant_id, email, name, role, status)
			VALUES ($1,$2,$3,$4,'owner','active')
		`, user.id, tenant, user.email, user.name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.user_aliases (tenant_id, user_id, email, label, kind, can_send)
			VALUES ($1,$2,$3,$4,'primary',true)
		`, tenant, user.id, user.email, user.name); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{
		store:      &store.Store{DB: pool},
		logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		stateLocks: map[string]*sync.Mutex{},
	}
	clientUser := userRecord{ID: userID, TenantID: tenant, Email: clientEmail, Name: "Client User"}
	serverUser := userRecord{ID: serverUserID, TenantID: tenant, Email: serverEmail, Name: "Server User"}
	withSession := func(req *http.Request, user userRecord) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), sessionContextKey{}, &sessionRecord{
			UserID: user.ID, TenantID: user.TenantID, User: user,
		}))
	}
	jsonRequest := func(method, target string, body []byte, user userRecord) *http.Request {
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return withSession(req, user)
	}

	// A server-managed key for another user must not leak into the browser
	// directory in client-side mode.
	serverKey, err := mailcrypto.GenerateOpenPGPKey("Server User", serverEmail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_crypto_identities
			(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, source, status)
		VALUES ($1,$2,$3,$4,'openpgp',$5,$6,$7,'generated','active')
	`, "ckey_"+suffix, tenant, serverUserID, serverEmail, serverKey.KeyID, serverKey.Fingerprint, serverKey.ArmoredPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_crypto_private_keys (identity_id, wrapped_key, wrap_version)
		VALUES ($1,'test-wrapped-key',1)
	`, "ckey_"+suffix); err != nil {
		t.Fatal(err)
	}

	// The legacy server key exists first; the guarded migration equivalent is
	// represented here by switching the policy, which then blocks new server
	// private keys through the 0105 trigger.
	if _, err := pool.Exec(ctx, `
		UPDATE public.tenants
		SET settings = jsonb_set(settings, '{encryption,mode}', '"client_side"'::jsonb)
		WHERE id=$1
	`, tenant); err != nil {
		t.Fatal(err)
	}
	clientKey, err := mailcrypto.GenerateOpenPGPKey("Client User", clientEmail)
	if err != nil {
		t.Fatal(err)
	}
	publishBody, _ := json.Marshal(map[string]string{"email": clientEmail, "publicKey": clientKey.ArmoredPublic})
	recorder := httptest.NewRecorder()
	server.publishClientIdentity(recorder, jsonRequest(http.MethodPost, "/api/encryption/client-identity", publishBody, clientUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var source, fingerprint string
	if err := pool.QueryRow(ctx, `
		SELECT source, fingerprint FROM public.user_crypto_identities
		WHERE user_id=$1 AND protocol='openpgp' AND status='active'
	`, userID).Scan(&source, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if source != "client" || fingerprint != clientKey.Fingerprint {
		t.Fatalf("identity source=%q fingerprint=%q", source, fingerprint)
	}
	var privateRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.user_crypto_private_keys p
		JOIN public.user_crypto_identities i ON i.id=p.identity_id
		WHERE i.user_id=$1 AND i.fingerprint=$2
	`, userID, clientKey.Fingerprint).Scan(&privateRows); err != nil {
		t.Fatal(err)
	}
	if privateRows != 0 {
		t.Fatal("browser-held identity unexpectedly has a server private key")
	}

	domain := "encryption.example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.mail_domains (id, tenant_id, domain, is_primary, status)
		VALUES ($1,$2,$3,false,'verified')
	`, fmt.Sprintf("00000000-0000-4077-8000-%012d", time.Now().UnixNano()%1000000000000), tenant, domain); err != nil {
		t.Fatal(err)
	}

	directoryRequest := withSession(httptest.NewRequest(http.MethodGet,
		"/api/encryption/directory?protocol=openpgp&emails="+clientEmail+","+serverEmail, nil), clientUser)
	recorder = httptest.NewRecorder()
	server.encryptionDirectory(recorder, directoryRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("directory status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var directory struct {
		Directory map[string][]struct {
			PublicKey string `json:"publicKey"`
			Source    string `json:"source"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if len(directory.Directory[clientEmail]) != 1 || directory.Directory[clientEmail][0].Source != "client" {
		t.Fatalf("client directory entry = %#v", directory.Directory[clientEmail])
	}
	if len(directory.Directory[serverEmail]) != 0 {
		t.Fatalf("server-managed key leaked into client directory: %#v", directory.Directory[serverEmail])
	}

	// Publishing the public half of a server-managed identity would prove only
	// public-key possession. It must be refused rather than deleting the server
	// private key.
	serverPublicBody, _ := json.Marshal(map[string]string{"email": serverEmail, "publicKey": serverKey.ArmoredPublic})
	recorder = httptest.NewRecorder()
	server.publishClientIdentity(recorder, jsonRequest(http.MethodPost, "/api/encryption/client-identity", serverPublicBody, serverUser))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "ENCRYPTION_SERVER_KEY") {
		t.Fatalf("server-managed publish status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	localPart := strings.SplitN(clientEmail, "@", 2)[0]
	wkdHash := mailcrypto.WKDLocalPartHash(localPart)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openpgpkey/hu/{hash}", server.wkdPublishedKey)
	mux.HandleFunc("GET /.well-known/openpgpkey/{domain}/hu/{hash}", server.wkdPublishedKeyDirect)
	wkdRequest := httptest.NewRequest(http.MethodGet,
		"https://"+domain+"/.well-known/openpgpkey/hu/"+wkdHash+"?l="+localPart, nil)
	wkdRequest.Host = domain
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, wkdRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("WKD status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	entities, err := mailcrypto.ParseWKDResponse(recorder.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || strings.ToUpper(fmt.Sprintf("%x", entities[0].PrimaryKey.Fingerprint)) != clientKey.Fingerprint {
		t.Fatalf("WKD returned the wrong key")
	}
	directRequest := httptest.NewRequest(http.MethodGet,
		"https://openpgpkey."+domain+"/.well-known/openpgpkey/"+domain+"/hu/"+wkdHash+"?l="+localPart, nil)
	directRequest.Host = "openpgpkey." + domain
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, directRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("direct WKD status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// Encrypted account backup: reject an unencrypted private key, accept a
	// passphrase-protected one, and round-trip it without exposing plaintext.
	unencryptedBody, _ := json.Marshal(map[string]string{
		"email": clientEmail, "fingerprint": clientKey.Fingerprint,
		"publicKey": clientKey.ArmoredPublic, "encryptedPrivateKey": clientKey.ArmoredPrivate,
	})
	recorder = httptest.NewRecorder()
	server.saveEncryptionKeyBackup(recorder, jsonRequest(http.MethodPost, "/api/encryption/key-backup", unencryptedBody, clientUser))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unencrypted backup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	entities, err = openpgp.ReadArmoredKeyRing(strings.NewReader(clientKey.ArmoredPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if err := entities[0].EncryptPrivateKeys([]byte("backup-passphrase"), nil); err != nil {
		t.Fatal(err)
	}
	var locked bytes.Buffer
	armorWriter, err := armor.Encode(&locked, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := entities[0].SerializePrivateWithoutSigning(armorWriter, nil); err != nil {
		t.Fatal(err)
	}
	if err := armorWriter.Close(); err != nil {
		t.Fatal(err)
	}
	encryptedBody, _ := json.Marshal(map[string]string{
		"email": clientEmail, "fingerprint": clientKey.Fingerprint,
		"publicKey": clientKey.ArmoredPublic, "encryptedPrivateKey": locked.String(),
	})
	recorder = httptest.NewRecorder()
	server.saveEncryptionKeyBackup(recorder, jsonRequest(http.MethodPost, "/api/encryption/key-backup", encryptedBody, clientUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("encrypted backup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.getEncryptionKeyBackup(recorder, withSession(httptest.NewRequest(http.MethodGet, "/api/encryption/key-backup", nil), clientUser))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), clientKey.Fingerprint) {
		t.Fatalf("backup read status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	server.deleteEncryptionKeyBackup(recorder, withSession(httptest.NewRequest(http.MethodDelete, "/api/encryption/key-backup", nil), clientUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("backup delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// A browser-held S/MIME certificate publishes only its public chain and
	// remains usable by the S/MIME directory in client-side mode.
	smimeKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	smimeTemplate := &x509.Certificate{
		SerialNumber:   big.NewInt(time.Now().UnixNano()),
		Subject:        pkix.Name{CommonName: "Client User"},
		NotBefore:      time.Now().Add(-time.Hour),
		NotAfter:       time.Now().Add(24 * time.Hour),
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses: []string{clientEmail},
	}
	smimeDER, err := x509.CreateCertificate(rand.Reader, smimeTemplate, smimeTemplate, &smimeKey.PublicKey, smimeKey)
	if err != nil {
		t.Fatal(err)
	}
	smimeCert, err := x509.ParseCertificate(smimeDER)
	if err != nil {
		t.Fatal(err)
	}
	smimePEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: smimeDER}))
	smimeFingerprint := mailcrypto.SMIMEFingerprint(smimeCert)
	smimeBody, _ := json.Marshal(map[string]string{"email": clientEmail, "protocol": "smime", "publicKey": smimePEM})
	recorder = httptest.NewRecorder()
	server.publishClientIdentity(recorder, jsonRequest(http.MethodPost, "/api/encryption/client-identity", smimeBody, clientUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("S/MIME publish status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var smimeSource, smimeFingerprintRow, smimeCertificate string
	if err := pool.QueryRow(ctx, `
		SELECT source, fingerprint, public_key FROM public.user_crypto_identities
		WHERE user_id=$1 AND protocol='smime' AND status='active' LIMIT 1
	`, userID).Scan(&smimeSource, &smimeFingerprintRow, &smimeCertificate); err != nil {
		t.Fatal(err)
	}
	if smimeSource != "client" || smimeFingerprintRow != smimeFingerprint || !strings.Contains(smimeCertificate, "BEGIN CERTIFICATE") {
		t.Fatalf("S/MIME identity source=%q fingerprint=%q", smimeSource, smimeFingerprintRow)
	}
	if privateRows := func() int {
		var count int
		_ = pool.QueryRow(ctx, `
			SELECT count(*) FROM public.user_crypto_private_keys p
			JOIN public.user_crypto_identities i ON i.id=p.identity_id
			WHERE i.user_id=$1 AND i.protocol='smime'
		`, userID).Scan(&count)
		return count
	}(); privateRows != 0 {
		t.Fatal("a browser S/MIME identity must not have a server private key")
	}
	smimeDirectoryRequest := withSession(httptest.NewRequest(http.MethodGet,
		"/api/encryption/directory?protocol=smime&emails="+clientEmail, nil), clientUser)
	recorder = httptest.NewRecorder()
	server.encryptionDirectory(recorder, smimeDirectoryRequest)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "BEGIN CERTIFICATE") {
		t.Fatalf("S/MIME directory status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	notCovered, _ := json.Marshal(map[string]string{"email": serverEmail, "protocol": "smime", "publicKey": smimePEM})
	recorder = httptest.NewRecorder()
	server.publishClientIdentity(recorder, jsonRequest(http.MethodPost, "/api/encryption/client-identity", notCovered, serverUser))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("S/MIME certificate for another address status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// A browser key with multiple owned UIDs can be published once per alias
	// under the same fingerprint.
	aliasEmail := "client-alias-" + suffix + "@encryption.example.test"
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_aliases (tenant_id, user_id, email, label, kind, can_send)
		VALUES ($1,$2,$3,'Client Alias','alias',true)
	`, tenant, userID, aliasEmail); err != nil {
		t.Fatal(err)
	}
	multiEntity, err := openpgp.NewEntity("Client User", "", clientEmail, &packet.Config{RSABits: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if err := multiEntity.AddUserId("Client Alias", "", aliasEmail, &packet.Config{RSABits: 2048}); err != nil {
		t.Fatal(err)
	}
	var publicArmor bytes.Buffer
	publicWriter, err := armor.Encode(&publicArmor, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := multiEntity.Serialize(publicWriter); err != nil {
		t.Fatal(err)
	}
	if err := publicWriter.Close(); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{clientEmail, aliasEmail} {
		body, _ := json.Marshal(map[string]string{"email": email, "publicKey": publicArmor.String()})
		recorder = httptest.NewRecorder()
		server.publishClientIdentity(recorder, jsonRequest(http.MethodPost, "/api/encryption/client-identity", body, clientUser))
		if recorder.Code != http.StatusOK {
			t.Fatalf("alias publish %s status=%d body=%s", email, recorder.Code, recorder.Body.String())
		}
	}
	var activeRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.user_crypto_identities
		WHERE user_id=$1 AND protocol='openpgp' AND status='active' AND source='client'
		  AND lower(email::text) IN (lower($2), lower($3))
	`, userID, clientEmail, aliasEmail).Scan(&activeRows); err != nil {
		t.Fatal(err)
	}
	if activeRows != 2 {
		t.Fatalf("alias publish active rows=%d, want 2", activeRows)
	}
	aliasDirectory := withSession(httptest.NewRequest(http.MethodGet,
		"/api/encryption/directory?protocol=openpgp&emails="+aliasEmail, nil), clientUser)
	recorder = httptest.NewRecorder()
	server.encryptionDirectory(recorder, aliasDirectory)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), aliasEmail) {
		t.Fatalf("alias directory status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestEncryptionMigrationIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the encryption migration integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4080-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4081-8000-%012d", time.Now().UnixNano()%1000000000000)
	email := "migration-" + suffix + "@encryption.example.test"
	password := "migration-admin-password"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.tenants (id, slug, name, settings)
		VALUES ($1,$2,'Migration integration',
		        '{"encryption":{"mode":"server_managed","protocol":"both","allowUserImport":true,"allowDelegatedDecrypt":false}}'::jsonb)
	`, tenant, "migration-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, tenant_id, email, name, role, status, password_hash)
		VALUES ($1,$2,$3,'Migration User','owner','active',$4)
	`, userID, tenant, email, passwordHash); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store:      &store.Store{DB: pool},
		logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
		stateLocks: map[string]*sync.Mutex{},
		cfg:        config.Config{CryptoSecret: strings.Repeat("migration-crypto-", 2)},
	}
	user := userRecord{ID: userID, TenantID: tenant, Email: email, Name: "Migration User", PasswordHash: passwordHash}
	withSession := func(req *http.Request) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), sessionContextKey{}, &sessionRecord{
			UserID: user.ID, TenantID: user.TenantID, User: user,
		}))
	}
	adminSess := &adminSessionRecord{ActiveTenantID: tenant, User: adminUser{
		ID: userID, TenantID: tenant, Email: email, Name: "Migration User", Role: "owner", PasswordHash: passwordHash,
	}}
	withAdmin := func(req *http.Request) *http.Request {
		return req.WithContext(context.WithValue(req.Context(), adminSessionContextKey{}, adminSess))
	}
	vault, err := server.encryptionVault()
	if err != nil {
		t.Fatal(err)
	}
	storeIdentity := func(id string, openpgp bool) {
		t.Helper()
		if openpgp {
			key, err := mailcrypto.GenerateOpenPGPKey("Migration User", email)
			if err != nil {
				t.Fatal(err)
			}
			wrapped, err := vault.seal(tenant, userID, id, key.ArmoredPrivate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO public.user_crypto_identities
					(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, source, status)
				VALUES ($1,$2,$3,$4,'openpgp',$5,$6,$7,'generated','active')
			`, id, tenant, userID, email, key.KeyID, key.Fingerprint, key.ArmoredPublic); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
				INSERT INTO public.user_crypto_private_keys (identity_id, wrapped_key, wrap_version) VALUES ($1,$2,1)
			`, id, wrapped); err != nil {
				t.Fatal(err)
			}
			return
		}
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber:   big.NewInt(time.Now().UnixNano()),
			Subject:        pkix.Name{CommonName: "Migration User"},
			NotBefore:      time.Now().Add(-time.Hour),
			NotAfter:       time.Now().Add(24 * time.Hour),
			KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
			EmailAddresses: []string{email},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)})
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		identity, err := mailcrypto.ParseSMIMEIdentity(certPEM, keyPEM, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := vault.seal(tenant, userID, id, string(identity.PrivateKeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.user_crypto_identities
				(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, cert_chain,
				 subject, issuer, not_before, not_after, source, status)
			VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,$8,$9,$10,$11,$12,'imported','active')
		`, id, tenant, userID, email, identity.Fingerprint, identity.Fingerprint,
			string(identity.CertificatePEM), string(identity.ChainPEM), identity.Subject, identity.Issuer,
			identity.NotBefore, identity.NotAfter); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.user_crypto_private_keys (identity_id, wrapped_key, wrap_version) VALUES ($1,$2,1)
		`, id, wrapped); err != nil {
			t.Fatal(err)
		}
	}
	storeIdentity("mig-openpgp-"+suffix, true)

	exportable := httptest.NewRecorder()
	server.listExportableCryptoIdentities(exportable, withSession(httptest.NewRequest(http.MethodGet, "/api/encryption/keys/exportable", nil)))
	if exportable.Code != http.StatusOK || !strings.Contains(exportable.Body.String(), "mig-openpgp-"+suffix) {
		t.Fatalf("exportable status=%d body=%s", exportable.Code, exportable.Body.String())
	}
	exportRequest := func(id, password, exportPassword string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"password": password, "exportPassword": exportPassword})
		req := httptest.NewRequest(http.MethodPost, "/api/encryption/keys/"+id+"/export", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		server.exportCryptoIdentity(recorder, withSession(req))
		return recorder
	}
	if recorder := exportRequest("mig-openpgp-"+suffix, "wrong-password", ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("wrong-password export status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	openpgpExport := exportRequest("mig-openpgp-"+suffix, password, "")
	if openpgpExport.Code != http.StatusOK || !strings.Contains(openpgpExport.Body.String(), "PGP PRIVATE KEY") {
		t.Fatalf("OpenPGP export status=%d body=%s", openpgpExport.Code, openpgpExport.Body.String())
	}

	reportRequest := withAdmin(httptest.NewRequest(http.MethodGet, "/admin/api/policies/encryption-migration", nil))
	reportRecorder := httptest.NewRecorder()
	server.adminEncryptionMigrationReport(reportRecorder, reportRequest)
	if reportRecorder.Code != http.StatusOK || !strings.Contains(reportRecorder.Body.String(), `"canMigrate":true`) {
		t.Fatalf("migration report status=%d body=%s", reportRecorder.Code, reportRecorder.Body.String())
	}

	// A key created after the export must block the migration until it is
	// exported too.
	storeIdentity("mig-smime-"+suffix, false)
	blockedRecorder := httptest.NewRecorder()
	migrationBody, _ := json.Marshal(map[string]any{"password": password, "confirm": "migrate to client-side"})
	migrationReq := httptest.NewRequest(http.MethodPost, "/admin/api/policies/encryption-migration", bytes.NewReader(migrationBody))
	migrationReq.Header.Set("Content-Type", "application/json")
	server.adminEncryptionMigration(blockedRecorder, withAdmin(migrationReq))
	if blockedRecorder.Code != http.StatusConflict || !strings.Contains(blockedRecorder.Body.String(), "MIGRATION_EXPORTS_REQUIRED") {
		t.Fatalf("unexported migration status=%d body=%s", blockedRecorder.Code, blockedRecorder.Body.String())
	}
	smimeExport := exportRequest("mig-smime-"+suffix, password, "smime-export-passphrase")
	if smimeExport.Code != http.StatusOK || !strings.Contains(smimeExport.Body.String(), "pkcs12Base64") {
		t.Fatalf("S/MIME export status=%d body=%s", smimeExport.Code, smimeExport.Body.String())
	}
	var exportedBundle struct {
		PKCS12 string `json:"pkcs12Base64"`
	}
	if err := json.Unmarshal(smimeExport.Body.Bytes(), &exportedBundle); err != nil {
		t.Fatal(err)
	}
	bundle, err := base64.StdEncoding.DecodeString(exportedBundle.PKCS12)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mailcrypto.ParsePKCS12Identity(bundle, []byte("smime-export-passphrase")); err != nil {
		t.Fatalf("exported S/MIME PKCS#12 did not open: %v", err)
	}

	migrationRecorder := httptest.NewRecorder()
	migrationReq = httptest.NewRequest(http.MethodPost, "/admin/api/policies/encryption-migration", bytes.NewReader(migrationBody))
	migrationReq.Header.Set("Content-Type", "application/json")
	server.adminEncryptionMigration(migrationRecorder, withAdmin(migrationReq))
	if migrationRecorder.Code != http.StatusOK {
		t.Fatalf("migration status=%d body=%s", migrationRecorder.Code, migrationRecorder.Body.String())
	}
	var mode string
	if err := pool.QueryRow(ctx, `
		SELECT settings->'encryption'->>'mode' FROM public.tenants WHERE id=$1
	`, tenant).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "client_side" {
		t.Fatalf("tenant mode = %q", mode)
	}
	var remainingKeys int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.user_crypto_private_keys k
		JOIN public.user_crypto_identities i ON i.id=k.identity_id WHERE i.tenant_id=$1
	`, tenant).Scan(&remainingKeys); err != nil {
		t.Fatal(err)
	}
	if remainingKeys != 0 {
		t.Fatalf("server-held private keys remaining = %d", remainingKeys)
	}
	var migrations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.tenant_encryption_migrations WHERE tenant_id=$1 AND status='completed'
	`, tenant).Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("migration records = %d err=%v", migrations, err)
	}
	if recorder := exportRequest("mig-openpgp-"+suffix, password, ""); recorder.Code != http.StatusConflict {
		t.Fatalf("export after migration status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_crypto_private_keys (identity_id, wrapped_key, wrap_version)
		VALUES ($1,'must-not-be-accepted',1)
	`, "mig-openpgp-"+suffix); err == nil {
		t.Fatal("a client-side tenant accepted a new server-held private key")
	}
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestServerSMIMEOutboundIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("A8M_SCIM_TEST_DSN"))
	if dsn == "" {
		t.Skip("set A8M_SCIM_TEST_DSN to run the server S/MIME outbound integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant := fmt.Sprintf("00000000-0000-4090-8000-%012d", time.Now().UnixNano()%1000000000000)
	userID := fmt.Sprintf("00000000-0000-4091-8000-%012d", (time.Now().UnixNano()+1)%1000000000000)
	senderEmail := "outbound-sender-" + suffix + "@encryption.example.test"
	recipientEmail := "outbound-recipient-" + suffix + "@encryption.example.test"
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM public.tenants WHERE id=$1`, tenant) }()
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.tenants (id, slug, name, settings)
		VALUES ($1,$2,'S/MIME outbound',
		        '{"encryption":{"mode":"server_managed","protocol":"smime","allowUserImport":true,"allowDelegatedDecrypt":false}}'::jsonb)
	`, tenant, "outbound-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.users (id, tenant_id, email, name, role, status)
		VALUES ($1,$2,$3,'Outbound Sender','owner','active')
	`, userID, tenant, senderEmail); err != nil {
		t.Fatal(err)
	}
	makeCertificate := func(email string) (string, []byte, *x509.Certificate) {
		t.Helper()
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber:   big.NewInt(time.Now().UnixNano()),
			Subject:        pkix.Name{CommonName: email},
			NotBefore:      time.Now().Add(-time.Hour),
			NotAfter:       time.Now().Add(24 * time.Hour),
			KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
			EmailAddresses: []string{email},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)})
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		return string(certPEM), keyPEM, certificate
	}
	senderCertPEM, senderKeyPEM, senderCert := makeCertificate(senderEmail)
	recipientCertPEM, _, recipientCert := makeCertificate(recipientEmail)
	server := &Server{
		store:      &store.Store{DB: pool},
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateLocks: map[string]*sync.Mutex{},
		cfg:        config.Config{CryptoSecret: strings.Repeat("outbound-smime-", 2)},
	}
	vault, err := server.encryptionVault()
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := vault.seal(tenant, userID, "outbound-smime-"+suffix, string(senderKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_crypto_identities
			(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, source, status)
		VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,'imported','active')
	`, "outbound-smime-"+suffix, tenant, userID, senderEmail,
		mailcrypto.SMIMEFingerprint(senderCert), mailcrypto.SMIMEFingerprint(senderCert), senderCertPEM); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.user_crypto_private_keys (identity_id, wrapped_key, wrap_version)
		VALUES ($1,$2,1)
	`, "outbound-smime-"+suffix, wrapped); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.contact_crypto_keys
			(id, tenant_id, user_id, email, protocol, key_id, fingerprint, public_key, trust, source)
		VALUES ($1,$2,$3,$4,'smime',$5,$6,$7,'enterprise','manual')
	`, "outbound-contact-"+suffix, tenant, userID, recipientEmail,
		mailcrypto.SMIMEFingerprint(recipientCert), mailcrypto.SMIMEFingerprint(recipientCert), recipientCertPEM); err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: " + senderEmail + "\r\nTo: " + recipientEmail + "\r\nSubject: Protected\r\n\r\nContent-Type: text/plain\r\nContent-Transfer-Encoding: 7bit\r\n\r\nserver outbound secret marker\r\n")
	item := map[string]any{
		"cryptoEncrypt": true, "cryptoSigned": true, "cryptoProtocol": "smime",
		"to": []any{map[string]any{"email": recipientEmail}},
	}
	output, err := server.encryptOutboundMIME(ctx, userID, tenant, item, raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output, []byte("server outbound secret marker")) || !bytes.Contains(output, []byte("application/pkcs7-mime")) {
		t.Fatalf("outbound S/MIME output was not ciphertext: %s", output)
	}
	identity, err := mailcrypto.ParseSMIMEIdentity([]byte(senderCertPEM), senderKeyPEM, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, status, _, err := mailcrypto.DecryptSMIMEMIME(output, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("server outbound secret marker")) {
		t.Fatalf("decrypted S/MIME body = %q", plain)
	}
	if status.State != mailcrypto.SignatureValidUntrusted {
		t.Fatalf("inner signature state = %q", status.State)
	}
}
