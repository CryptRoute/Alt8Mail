package mailcrypto

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.mozilla.org/pkcs7"
	"software.sslmate.com/src/go-pkcs12"
)

// SMIMECertificate is a parsed leaf certificate and its intermediates. The
// leaf is the certificate whose private key or public key the caller holds.
type SMIMECertificate struct {
	Certificate    *x509.Certificate
	Chain          []*x509.Certificate
	Fingerprint    string
	Email          string
	Subject        string
	Issuer         string
	NotBefore      time.Time
	NotAfter       time.Time
	CertificatePEM []byte
	ChainPEM       []byte
}

// SMIMEIdentity adds private-key material for server-managed signing and
// decryption. The private key is held only in memory by this package.
type SMIMEIdentity struct {
	SMIMECertificate
	Signer        crypto.Signer
	Decrypter     crypto.Decrypter
	PrivateKeyPEM []byte
}

// SMIMEUsage selects the certificate key-usage check.
type SMIMEUsage string

const (
	SMIMEUsageSign    SMIMEUsage = "sign"
	SMIMEUsageEncrypt SMIMEUsage = "encrypt"
)

// SMIMECertificateCheck separates identity validity from chain trust. A valid
// certificate with an unknown issuer can still be used for encryption, but it
// is reported as untrusted rather than silently treated as enterprise trust.
type SMIMECertificateCheck struct {
	Valid      bool
	ChainValid bool
	Reason     string
	Revocation string
	Warnings   []string
}

var smimeOnce sync.Once

func configureSMIMEAlgorithms() {
	smimeOnce.Do(func() {
		// RFC 5751 interop: AES-256-CBC is the broadly supported S/MIME
		// content cipher. The library exposes this as a package global.
		pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
	})
}

// ParseSMIMECertificates accepts PEM certificate blocks, a DER certificate
// bundle, or a certs-only PKCS#7 structure.
func ParseSMIMECertificates(data []byte) ([]*x509.Certificate, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("certificate data is empty")
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("certificate data is too large")
	}
	certs := []*x509.Certificate{}
	rest := data
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) > 0 {
		return certs, nil
	}
	if parsed, err := x509.ParseCertificates(data); err == nil && len(parsed) > 0 {
		return parsed, nil
	}
	if p7, err := pkcs7.Parse(data); err == nil && len(p7.Certificates) > 0 {
		return p7.Certificates, nil
	}
	return nil, fmt.Errorf("certificate data contains no X.509 certificate")
}

// ParseSMIMEPrivateKey accepts unencrypted PKCS#8, PKCS#1 RSA and SEC1 EC
// private keys. Legacy PEM encryption is supported; PKCS#8 encrypted keys
// require the operator to decrypt them before import.
func ParseSMIMEPrivateKey(data, passphrase []byte) (crypto.Signer, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("private key data is empty")
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("private key data is too large")
	}
	rest := data
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if strings.Contains(strings.ToUpper(block.Headers["Proc-Type"]), "ENCRYPTED") {
			return nil, fmt.Errorf("legacy encrypted PEM is not accepted; decrypt it or import a PKCS#12 bundle")
		}
		switch block.Type {
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			return parseSMIMEDerPrivateKey(block.Bytes)
		case "ENCRYPTED PRIVATE KEY":
			return nil, fmt.Errorf("PKCS#8 encrypted private keys are not accepted; import a PKCS#12 bundle instead")
		}
	}
	return parseSMIMEDerPrivateKey(data)
}

func parseSMIMEDerPrivateKey(raw []byte) (crypto.Signer, error) {
	if key, err := x509.ParsePKCS8PrivateKey(raw); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, fmt.Errorf("private key type does not support signing")
	}
	if key, err := x509.ParsePKCS1PrivateKey(raw); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(raw); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("private key is not PKCS#8, PKCS#1 RSA or EC")
}

// ParseSMIMEIdentity combines a leaf certificate, optional intermediates and a
// private key. The certificate's public key must match the private key.
func ParseSMIMEIdentity(certData, keyData, chainData, passphrase []byte) (*SMIMEIdentity, error) {
	certs, err := ParseSMIMECertificates(certData)
	if err != nil {
		return nil, err
	}
	chain, err := ParseSMIMECertificates(chainData)
	if err != nil && len(bytes.TrimSpace(chainData)) > 0 {
		return nil, err
	}
	signer, err := ParseSMIMEPrivateKey(keyData, passphrase)
	if err != nil {
		return nil, err
	}
	var leaf *x509.Certificate
	rest := []*x509.Certificate{}
	for _, cert := range certs {
		if leaf == nil && publicKeysEqual(cert.PublicKey, signer.Public()) {
			leaf = cert
			continue
		}
		rest = append(rest, cert)
	}
	if leaf == nil {
		return nil, fmt.Errorf("the certificate does not match the private key")
	}
	return newSMIMEIdentity(leaf, append(rest, chain...), signer)
}

// ParsePKCS12Identity imports a PKCS#12 bundle. PKCS#12 is the usual export
// format from enterprise certificate stores.
func ParsePKCS12Identity(data, passphrase []byte) (*SMIMEIdentity, error) {
	if len(data) == 0 || len(data) > 8<<20 {
		return nil, fmt.Errorf("PKCS#12 data is empty or too large")
	}
	key, cert, caCerts, err := pkcs12.DecodeChain(data, string(passphrase))
	if err != nil {
		return nil, fmt.Errorf("decode PKCS#12: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("PKCS#12 private key does not support signing")
	}
	return newSMIMEIdentity(cert, caCerts, signer)
}

// EncodePKCS12Identity creates a password-protected PKCS#12 bundle for a
// user who is moving from server-managed to browser-held S/MIME. The caller
// must deliver it only through an authenticated, non-cached response.
func EncodePKCS12Identity(identity *SMIMEIdentity, password string) ([]byte, error) {
	if identity == nil || identity.Certificate == nil || identity.Signer == nil {
		return nil, fmt.Errorf("an S/MIME identity is required")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("the export passphrase must be at least 8 characters")
	}
	bundle, err := pkcs12.Modern.Encode(identity.Signer, identity.Certificate, identity.Chain, password)
	if err != nil {
		return nil, fmt.Errorf("encode PKCS#12 bundle: %w", err)
	}
	return bundle, nil
}

func newSMIMEIdentity(cert *x509.Certificate, chain []*x509.Certificate, signer crypto.Signer) (*SMIMEIdentity, error) {
	if cert == nil || signer == nil {
		return nil, fmt.Errorf("certificate and private key are required")
	}
	if !publicKeysEqual(cert.PublicKey, signer.Public()) {
		return nil, fmt.Errorf("the certificate does not match the private key")
	}
	chain = orderCertificateChain(cert, chain)
	keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, fmt.Errorf("encode private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	identity := &SMIMEIdentity{
		SMIMECertificate: certificateInfo(cert, chain),
		Signer:           signer,
		PrivateKeyPEM:    keyPEM,
	}
	if decrypter, ok := signer.(crypto.Decrypter); ok {
		identity.Decrypter = decrypter
	}
	return identity, nil
}

// SMIMEIdentityForSigner builds an identity around a key that stays inside an
// HSM or smart-card token. The caller supplies the public certificate chain.
func SMIMEIdentityForSigner(cert *x509.Certificate, chain []*x509.Certificate, signer crypto.Signer) *SMIMEIdentity {
	if cert == nil || signer == nil {
		return nil
	}
	identity := &SMIMEIdentity{
		SMIMECertificate: certificateInfo(cert, orderCertificateChain(cert, chain)),
		Signer:           signer,
	}
	if decrypter, ok := signer.(crypto.Decrypter); ok {
		identity.Decrypter = decrypter
	}
	return identity
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	aDER, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	bDER, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aDER, bDER)
}

func certificateInfo(cert *x509.Certificate, chain []*x509.Certificate) SMIMECertificate {
	fingerprint := SMIMEFingerprint(cert)
	emails := CertificateEmails(cert)
	email := ""
	if len(emails) > 0 {
		email = emails[0]
	}
	return SMIMECertificate{
		Certificate: cert, Chain: chain, Fingerprint: fingerprint, Email: email,
		Subject: cert.Subject.String(), Issuer: cert.Issuer.String(),
		NotBefore: cert.NotBefore, NotAfter: cert.NotAfter,
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		ChainPEM:       certificatesPEM(chain),
	}
}

// CertificateInfoFor builds the public view of one certificate and chain.
func CertificateInfoFor(cert *x509.Certificate, chain []*x509.Certificate) (SMIMECertificate, error) {
	if cert == nil {
		return SMIMECertificate{}, fmt.Errorf("certificate is required")
	}
	return certificateInfo(cert, orderCertificateChain(cert, chain)), nil
}

func SMIMEFingerprint(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func CertificateEmails(cert *x509.Certificate) []string {
	if cert == nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, email := range cert.EmailAddresses {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" && !seen[email] {
			seen[email] = true
			out = append(out, email)
		}
	}
	return out
}

func certificatesPEM(certs []*x509.Certificate) []byte {
	var out bytes.Buffer
	for _, cert := range certs {
		if cert != nil {
			_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		}
	}
	return out.Bytes()
}

func orderCertificateChain(leaf *x509.Certificate, certs []*x509.Certificate) []*x509.Certificate {
	if leaf == nil {
		return nil
	}
	pool := map[string]*x509.Certificate{}
	for _, cert := range certs {
		if cert != nil && !bytes.Equal(cert.Raw, leaf.Raw) {
			pool[string(cert.RawSubject)] = cert
		}
	}
	out := []*x509.Certificate{}
	seen := map[string]bool{string(leaf.Raw): true}
	current := leaf
	for len(out) < 16 {
		next := pool[string(current.RawIssuer)]
		if next == nil || seen[string(next.Raw)] {
			break
		}
		seen[string(next.Raw)] = true
		out = append(out, next)
		current = next
	}
	return out
}

// LoadSMIMERoots starts with the platform trust store and appends an optional
// enterprise PEM bundle.
func LoadSMIMERoots(extraFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if strings.TrimSpace(extraFile) == "" {
		return pool, nil
	}
	data, err := os.ReadFile(extraFile)
	if err != nil {
		return nil, fmt.Errorf("read S/MIME CA bundle: %w", err)
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("S/MIME CA bundle contains no certificates")
	}
	return pool, nil
}

// CheckSMIMECertificate validates validity, email SAN, EKU and key usage, then
// attempts chain validation against roots. Chain failure is reported separately
// so a caller can distinguish untrusted from malformed.
func CheckSMIMECertificate(cert *x509.Certificate, chain []*x509.Certificate, roots *x509.CertPool, now time.Time, email string, usage SMIMEUsage) SMIMECertificateCheck {
	check := SMIMECertificateCheck{}
	if cert == nil {
		check.Reason = "certificate is missing"
		return check
	}
	if cert.IsCA {
		check.Reason = "certificate is a CA certificate, not an end-entity identity"
		return check
	}
	if now.Before(cert.NotBefore) {
		check.Reason = "certificate is not valid yet"
		return check
	}
	if now.After(cert.NotAfter) {
		check.Reason = "certificate has expired"
		return check
	}
	emails := CertificateEmails(cert)
	if len(emails) == 0 {
		check.Reason = "certificate has no email SAN"
		return check
	}
	matched := false
	for _, candidate := range emails {
		if email == "" || strings.EqualFold(candidate, email) {
			matched = true
			break
		}
	}
	if !matched {
		check.Reason = "certificate does not cover " + email
		return check
	}
	if len(cert.ExtKeyUsage) > 0 {
		hasEmailProtection := false
		for _, usage := range cert.ExtKeyUsage {
			if usage == x509.ExtKeyUsageEmailProtection || usage == x509.ExtKeyUsageAny {
				hasEmailProtection = true
				break
			}
		}
		if !hasEmailProtection {
			check.Reason = "certificate lacks the emailProtection extended key usage"
			return check
		}
	}
	switch cert.PublicKey.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
	default:
		check.Reason = "certificate uses an unsupported S/MIME key type"
		return check
	}
	if usage == SMIMEUsageEncrypt {
		if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
			check.Reason = "S/MIME encryption requires an RSA certificate"
			return check
		}
	}
	if cert.KeyUsage != 0 {
		allowed := false
		switch usage {
		case SMIMEUsageSign:
			allowed = cert.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment) != 0
		case SMIMEUsageEncrypt:
			allowed = cert.KeyUsage&(x509.KeyUsageKeyEncipherment|x509.KeyUsageKeyAgreement) != 0
		default:
			allowed = true
		}
		if !allowed {
			check.Reason = "certificate key usage does not allow " + string(usage)
			return check
		}
	}
	check.Valid = true
	if roots == nil {
		if system, err := x509.SystemCertPool(); err == nil {
			roots = system
		}
	}
	intermediates := x509.NewCertPool()
	for _, item := range orderCertificateChain(cert, chain) {
		intermediates.AddCert(item)
	}
	if roots == nil {
		check.Warnings = append(check.Warnings, "S/MIME certificate chain could not be checked")
		return check
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}); err != nil {
		check.Warnings = append(check.Warnings, "certificate chain is not trusted: "+err.Error())
		return check
	}
	check.ChainValid = true
	return check
}

// SMIMECertificateFromRecipient parses a recipient's certificate material.
func SMIMECertificateFromRecipient(recipient RecipientKey) (*x509.Certificate, []*x509.Certificate, error) {
	certs, err := ParseSMIMECertificates(recipient.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	leaf := certs[0]
	chain := append(append([]*x509.Certificate{}, certs[1:]...), x509ChainFromBytes(recipient.CertChain)...)
	return leaf, orderCertificateChain(leaf, chain), nil
}

func x509ChainFromBytes(chains [][]byte) []*x509.Certificate {
	out := []*x509.Certificate{}
	for _, raw := range chains {
		certs, err := ParseSMIMECertificates(raw)
		if err == nil {
			out = append(out, certs...)
		}
	}
	return out
}

// EncryptSMIMEMIME wraps a MIME entity in CMS EnvelopedData. If signer is
// non-nil and sign is true, the entity is signed first (sign-then-encrypt).
func EncryptSMIMEMIME(plaintext []byte, recipients []*x509.Certificate, identity *SMIMEIdentity, sign, encryptToSelf bool) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no S/MIME recipient certificates")
	}
	if sign {
		if identity == nil {
			return nil, fmt.Errorf("signing was requested without an S/MIME identity")
		}
		signed, err := SignSMIMEMIME(plaintext, identity)
		if err != nil {
			return nil, err
		}
		plaintext = signed
	}
	certs := append([]*x509.Certificate{}, recipients...)
	if encryptToSelf {
		if identity == nil {
			return nil, fmt.Errorf("encryption to self requires an S/MIME identity")
		}
		certs = append(certs, identity.Certificate)
	}
	configureSMIMEAlgorithms()
	der, err := pkcs7.Encrypt(plaintext, certs)
	if err != nil {
		return nil, fmt.Errorf("encrypt S/MIME content: %w", err)
	}
	var output bytes.Buffer
	output.WriteString("MIME-Version: 1.0\r\n")
	output.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n")
	output.WriteString("Content-Transfer-Encoding: base64\r\n")
	output.WriteString("Content-Disposition: attachment; filename=\"smime.p7m\"\r\n")
	output.WriteString("\r\n")
	output.WriteString(base64Lines(der))
	return output.Bytes(), nil
}

// SignSMIMEMIME creates a detached CMS signature over the exact MIME entity
// bytes and returns an RFC 1847 multipart/signed entity.
func SignSMIMEMIME(plaintext []byte, identity *SMIMEIdentity) ([]byte, error) {
	if identity == nil || identity.Signer == nil || identity.Certificate == nil {
		return nil, fmt.Errorf("an S/MIME identity is required")
	}
	if _, ok := identity.Signer.(*rsa.PrivateKey); !ok {
		return SignSMIMEMIMEWithSigner(plaintext, identity.Certificate, identity.Chain, identity.Signer)
	}
	sd, err := pkcs7.NewSignedData(plaintext)
	if err != nil {
		return nil, err
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSignerChain(identity.Certificate, identity.Signer, identity.Chain, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("sign S/MIME entity: %w", err)
	}
	sd.Detach()
	signature, err := sd.Finish()
	if err != nil {
		return nil, err
	}
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("MIME-Version: 1.0\r\n")
	output.WriteString(`Content-Type: multipart/signed; protocol="application/pkcs7-signature"; micalg=sha-256; boundary="` + boundary + `"` + "\r\n")
	output.WriteString("\r\n")
	output.WriteString("--" + boundary + "\r\n")
	output.Write(plaintext)
	output.WriteString("\r\n--" + boundary + "\r\n")
	output.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n")
	output.WriteString("Content-Transfer-Encoding: base64\r\n")
	output.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n")
	output.WriteString("\r\n")
	output.WriteString(base64Lines(signature))
	output.WriteString("\r\n--" + boundary + "--\r\n")
	return output.Bytes(), nil
}

// DecryptSMIMEMIME decrypts a message and verifies an inner signed entity.
func DecryptSMIMEMIME(message []byte, identity *SMIMEIdentity) ([]byte, SignatureStatus, []string, error) {
	if identity == nil || identity.Certificate == nil || identity.Signer == nil {
		return nil, SignatureStatus{State: SignatureUnknown}, nil, fmt.Errorf("an S/MIME identity is required")
	}
	analysis, err := Analyze(message)
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, nil, err
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Protocol != ProtocolSMIME || analysis.Protection.EncryptedPart == nil {
		return nil, SignatureStatus{State: SignatureNone}, nil, fmt.Errorf("message is not an S/MIME enveloped message")
	}
	der, err := DecodeTransferEncoding(analysis.Protection.EncryptedPart)
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, nil, err
	}
	var plaintext []byte
	if rsaKey, ok := identity.Signer.(*rsa.PrivateKey); ok {
		p7, err := pkcs7.Parse(der)
		if err != nil {
			return nil, SignatureStatus{State: SignatureUnknown}, nil, fmt.Errorf("parse S/MIME envelope: %w", err)
		}
		plaintext, err = p7.Decrypt(identity.Certificate, rsaKey)
		if err != nil {
			return nil, SignatureStatus{State: SignatureUnknown}, nil, fmt.Errorf("decrypt S/MIME content: %w", err)
		}
	} else {
		decrypter := identity.Decrypter
		if decrypter == nil {
			if candidate, ok := identity.Signer.(crypto.Decrypter); ok {
				decrypter = candidate
			}
		}
		if decrypter == nil {
			return nil, SignatureStatus{State: SignatureUnknown}, nil, fmt.Errorf("the S/MIME identity cannot decrypt mail")
		}
		plaintext, err = DecryptSMIMEEnvelopeWithDecrypter(der, identity.Certificate, decrypter)
		if err != nil {
			return nil, SignatureStatus{State: SignatureUnknown}, nil, fmt.Errorf("decrypt S/MIME content: %w", err)
		}
	}
	status := SignatureStatus{State: SignatureNone}
	warnings := []string{}
	if inner, err := Analyze(plaintext); err == nil && inner.Protection.Signed && inner.Protection.Protocol == ProtocolSMIME {
		innerStatus, innerWarnings, verifyErr := VerifySMIMESignature(plaintext, nil, time.Now())
		if verifyErr == nil {
			status = innerStatus
		} else {
			status = SignatureStatus{State: SignatureInvalid, Reason: verifyErr.Error()}
		}
		warnings = append(warnings, innerWarnings...)
	}
	return plaintext, status, warnings, nil
}

// VerifySMIMESignature verifies a multipart/signed entity cryptographically and
// returns certificate identity information. Chain trust is reported through
// warnings; the caller decides how to map that to a contact trust level.
func VerifySMIMESignature(message []byte, roots *x509.CertPool, now time.Time) (SignatureStatus, []string, error) {
	analysis, err := Analyze(message)
	if err != nil {
		return SignatureStatus{State: SignatureUnknown}, nil, err
	}
	if !analysis.Protection.Signed || analysis.Protection.Protocol != ProtocolSMIME ||
		analysis.Protection.SignedContent == nil || analysis.Protection.Signature == nil {
		return SignatureStatus{State: SignatureNone}, nil, fmt.Errorf("message is not an S/MIME signed entity")
	}
	der, err := DecodeTransferEncoding(analysis.Protection.Signature)
	if err != nil {
		return SignatureStatus{State: SignatureInvalid}, nil, err
	}
	p7, err := pkcs7.Parse(der)
	if err != nil {
		return SignatureStatus{State: SignatureInvalid}, nil, fmt.Errorf("parse S/MIME signature: %w", err)
	}
	p7.Content = analysis.Protection.SignedContent.Raw
	cert := p7.GetOnlySigner()
	if cert == nil {
		return SignatureStatus{State: SignatureInvalid}, nil, fmt.Errorf("S/MIME signature has no signer certificate")
	}
	check := CheckSMIMECertificate(cert, certificatesExcept(p7.Certificates, cert), roots, now, "", SMIMEUsageSign)
	if !check.Valid {
		state := SignatureInvalid
		if strings.Contains(strings.ToLower(check.Reason), "expired") {
			state = SignatureExpired
		}
		return SignatureStatus{State: state, Reason: check.Reason, Fingerprint: SMIMEFingerprint(cert)}, check.Warnings, nil
	}
	if err := p7.VerifyWithChainAtTime(nil, now); err != nil {
		return SignatureStatus{State: SignatureInvalid, Reason: err.Error(), Fingerprint: SMIMEFingerprint(cert)}, check.Warnings, nil
	}
	status := SignatureStatus{
		State: SignatureValidUntrusted, Fingerprint: SMIMEFingerprint(cert),
		Trust: TrustUnknown, SignerEmail: certificateEmail(cert),
	}
	if signedAt := smimeSigningTime(p7); signedAt != nil {
		status.SignedAt = signedAt
	}
	return status, check.Warnings, nil
}

func certificatesExcept(certs []*x509.Certificate, exclude *x509.Certificate) []*x509.Certificate {
	out := make([]*x509.Certificate, 0, len(certs))
	for _, cert := range certs {
		if cert != nil && (exclude == nil || !bytes.Equal(cert.Raw, exclude.Raw)) {
			out = append(out, cert)
		}
	}
	return out
}

func certificateEmail(cert *x509.Certificate) string {
	emails := CertificateEmails(cert)
	if len(emails) > 0 {
		return emails[0]
	}
	return ""
}

func smimeSigningTime(p7 *pkcs7.PKCS7) *time.Time {
	if p7 == nil {
		return nil
	}
	var signedAt time.Time
	if err := p7.UnmarshalSignedAttribute(pkcs7.OIDAttributeSigningTime, &signedAt); err != nil {
		return nil
	}
	return &signedAt
}

// SMIMEProvider implements Provider for CMS/S-MIME.
type SMIMEProvider struct{}

func (p SMIMEProvider) Protocol() Protocol { return ProtocolSMIME }

func (p SMIMEProvider) Capabilities() Capabilities {
	return Capabilities{
		CanEncrypt: true, CanSign: true, CanDecrypt: true, CanVerify: true,
		Algorithms: []string{"rsa", "ecdsa", "aes-256-cbc"},
	}
}

func (p SMIMEProvider) Encrypt(_ context.Context, req EncryptRequest) (EncryptResult, error) {
	identity, err := smimeIdentityFromRequest(req.PrivateKey, req.Certificate, req.CertificateChain)
	if err != nil {
		return EncryptResult{}, err
	}
	if req.Sign {
		check := CheckSMIMECertificate(identity.Certificate, identity.Chain, nil, time.Now(), identity.Email, SMIMEUsageSign)
		if !check.Valid {
			return EncryptResult{}, fmt.Errorf("S/MIME signing certificate is not usable: %s", check.Reason)
		}
	}
	if req.EncryptToSelf {
		check := CheckSMIMECertificate(identity.Certificate, identity.Chain, nil, time.Now(), identity.Email, SMIMEUsageEncrypt)
		if !check.Valid {
			return EncryptResult{}, fmt.Errorf("S/MIME identity cannot be an encryption recipient: %s", check.Reason)
		}
	}
	recipients := []*x509.Certificate{}
	keyIDs := []string{}
	for _, item := range req.Recipients {
		leaf, chain, err := SMIMECertificateFromRecipient(item)
		if err != nil {
			return EncryptResult{}, fmt.Errorf("%s: %w", item.Email, err)
		}
		check := CheckSMIMECertificate(leaf, chain, nil, time.Now(), item.Email, SMIMEUsageEncrypt)
		if !check.Valid {
			return EncryptResult{}, fmt.Errorf("%s: %s", item.Email, check.Reason)
		}
		recipients = append(recipients, leaf)
		keyIDs = append(keyIDs, SMIMEFingerprint(leaf))
	}
	mime, err := EncryptSMIMEMIME(req.MIME, recipients, identity, req.Sign, req.EncryptToSelf)
	if err != nil {
		return EncryptResult{}, err
	}
	return EncryptResult{Protocol: ProtocolSMIME, MIME: mime, RecipientKeyIDs: keyIDs, Signed: req.Sign}, nil
}

func (p SMIMEProvider) Sign(_ context.Context, req SignRequest) (SignResult, error) {
	identity, err := smimeIdentityFromRequest(req.PrivateKey, req.Certificate, req.CertificateChain)
	if err != nil {
		return SignResult{}, err
	}
	check := CheckSMIMECertificate(identity.Certificate, identity.Chain, nil, time.Now(), identity.Email, SMIMEUsageSign)
	if !check.Valid {
		return SignResult{}, fmt.Errorf("%s", check.Reason)
	}
	mime, err := SignSMIMEMIME(req.MIME, identity)
	if err != nil {
		return SignResult{}, err
	}
	return SignResult{Protocol: ProtocolSMIME, MIME: mime, KeyID: identity.Fingerprint}, nil
}

func (p SMIMEProvider) Decrypt(_ context.Context, req DecryptRequest) (DecryptResult, error) {
	identity, err := smimeIdentityFromRequest(req.PrivateKey, req.Certificate, req.CertificateChain)
	if err != nil {
		return DecryptResult{}, err
	}
	mime, status, warnings, err := DecryptSMIMEMIME(req.Message, identity)
	if err != nil {
		return DecryptResult{}, err
	}
	return DecryptResult{Protocol: ProtocolSMIME, MIME: mime, Signature: status, Warnings: warnings}, nil
}

func (p SMIMEProvider) Verify(_ context.Context, req VerifyRequest) (SignatureStatus, error) {
	certs, err := ParseSMIMECertificates(req.PublicKey)
	if err != nil {
		return SignatureStatus{State: SignatureUnknown}, err
	}
	roots, _ := LoadSMIMERoots("")
	status, _, err := VerifySMIMESignature(req.Message, roots, time.Now())
	if err != nil {
		return SignatureStatus{State: SignatureInvalid}, err
	}
	// The caller usually supplies the counterpart certificate. Make sure the
	// embedded signer matches it when the request carries a specific cert.
	if len(certs) > 0 && !strings.EqualFold(SMIMEFingerprint(certs[0]), status.Fingerprint) {
		status.State = SignatureInvalid
		status.Reason = "signer certificate does not match the supplied certificate"
	}
	return status, nil
}

func smimeIdentityFromRequest(privateKeyPEM, certPEM []byte, chainPEM [][]byte) (*SMIMEIdentity, error) {
	if len(privateKeyPEM) == 0 || len(certPEM) == 0 {
		return nil, fmt.Errorf("S/MIME private key and certificate are required")
	}
	chain := []byte{}
	for _, item := range chainPEM {
		chain = append(chain, item...)
	}
	return ParseSMIMEIdentity(certPEM, privateKeyPEM, chain, nil)
}
