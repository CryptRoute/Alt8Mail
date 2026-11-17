package mailcrypto

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// OpenPGPKey is a parsed OpenPGP identity. PrivateKeyMaterial is the armored
// private key and is only placed in memory after the key service has unwrapped
// it. It must never be persisted by this package.
type OpenPGPKey struct {
	Entity         *openpgp.Entity
	ArmoredPrivate string
	ArmoredPublic  string
	Fingerprint    string
	KeyID          string
	Email          string
	Name           string
}

func openPGPConfig() *packet.Config {
	return &packet.Config{
		RSABits:     3072,
		DefaultHash: crypto.SHA256,
		Time:        time.Now,
	}
}

// GenerateOpenPGPKey creates a server-managed RSA/OpenPGP identity. The key is
// unencrypted in memory; the key service wraps it before storage.
func GenerateOpenPGPKey(name, email string) (*OpenPGPKey, error) {
	entity, err := openpgp.NewEntity(name, "", email, openPGPConfig())
	if err != nil {
		return nil, err
	}
	return openPGPKeyFromEntity(entity, email, name)
}

// ParseOpenPGPPrivateKey parses an armored private key. If passphrase is
// non-empty, encrypted primary keys and subkeys are decrypted in memory.
func ParseOpenPGPPrivateKey(armored []byte, passphrase []byte) (*OpenPGPKey, error) {
	entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("read OpenPGP private key: %w", err)
	}
	for _, entity := range entities {
		if entity.PrivateKey == nil {
			continue
		}
		if err := decryptEntityKeys(entity, passphrase); err != nil {
			return nil, err
		}
		return openPGPKeyFromEntity(entity, "", "")
	}
	return nil, fmt.Errorf("armored key contains no private key")
}

// OpenPGPPrivateKeyFingerprint reads the primary fingerprint from an armored
// private key without decrypting it. It is used to validate an encrypted
// browser backup against the public key before storing the opaque blob.
func OpenPGPPrivateKeyFingerprint(armored []byte) (string, error) {
	entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return "", fmt.Errorf("read OpenPGP private key: %w", err)
	}
	for _, entity := range entities {
		if entity == nil || entity.PrimaryKey == nil || entity.PrivateKey == nil {
			continue
		}
		return strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint)), nil
	}
	return "", fmt.Errorf("armored key contains no private key")
}

// OpenPGPPrivateKeyProtected reports whether every private key packet in the
// armored keyring is passphrase-encrypted. A key backup must never place an
// unencrypted private key on the server.
func OpenPGPPrivateKeyProtected(armored []byte) (bool, error) {
	entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return false, fmt.Errorf("read OpenPGP private key: %w", err)
	}
	found := false
	for _, entity := range entities {
		if entity == nil {
			continue
		}
		keys := []*packet.PrivateKey{entity.PrivateKey}
		for index := range entity.Subkeys {
			keys = append(keys, entity.Subkeys[index].PrivateKey)
		}
		for _, key := range keys {
			if key == nil {
				continue
			}
			found = true
			if !key.Encrypted {
				return false, nil
			}
		}
	}
	if !found {
		return false, fmt.Errorf("armored key contains no private key")
	}
	return true, nil
}

// ParseOpenPGPPublicKey parses an armored public key.
func ParseOpenPGPPublicKey(armored []byte) (*openpgp.Entity, error) {
	entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("read OpenPGP public key: %w", err)
	}
	if len(entities) == 0 {
		return nil, fmt.Errorf("armored key contains no OpenPGP entity")
	}
	return entities[0], nil
}

func decryptEntityKeys(entity *openpgp.Entity, passphrase []byte) error {
	keys := []*packet.PrivateKey{entity.PrivateKey}
	for index := range entity.Subkeys {
		keys = append(keys, entity.Subkeys[index].PrivateKey)
	}
	for _, key := range keys {
		if key == nil || !key.Encrypted {
			continue
		}
		if len(passphrase) == 0 {
			return fmt.Errorf("OpenPGP private key is passphrase protected")
		}
		if err := key.Decrypt(passphrase); err != nil {
			return fmt.Errorf("OpenPGP passphrase did not unlock the private key: %w", err)
		}
	}
	return nil
}

func openPGPKeyFromEntity(entity *openpgp.Entity, email, name string) (*OpenPGPKey, error) {
	if entity == nil || entity.PrimaryKey == nil {
		return nil, fmt.Errorf("OpenPGP entity has no primary key")
	}
	fingerprint := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	keyID := fingerprint
	if len(keyID) > 16 {
		keyID = keyID[len(keyID)-16:]
	}
	if email == "" || name == "" {
		for _, identity := range entity.Identities {
			if identity == nil || identity.UserId == nil {
				continue
			}
			if email == "" {
				email = identity.UserId.Email
			}
			if name == "" {
				name = identity.UserId.Name
			}
			if email != "" && name != "" {
				break
			}
		}
	}
	publicArmor, err := armoredEntity(entity, false)
	if err != nil {
		return nil, err
	}
	privateArmor, err := armoredEntity(entity, true)
	if err != nil {
		return nil, err
	}
	return &OpenPGPKey{
		Entity: entity, ArmoredPrivate: privateArmor, ArmoredPublic: publicArmor,
		Fingerprint: fingerprint, KeyID: keyID, Email: email, Name: name,
	}, nil
}

func armoredEntity(entity *openpgp.Entity, private bool) (string, error) {
	var buffer bytes.Buffer
	blockType := "PGP PUBLIC KEY BLOCK"
	if private {
		blockType = "PGP PRIVATE KEY BLOCK"
	}
	writer, err := armor.Encode(&buffer, blockType, nil)
	if err != nil {
		return "", err
	}
	if private {
		err = entity.SerializePrivate(writer, nil)
	} else {
		err = entity.Serialize(writer)
	}
	if err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

// EncryptPGPMIME wraps a MIME entity in multipart/encrypted. The MIME entity is
// the complete plaintext entity, including its own headers. When sign is true
// the signature is inside the ciphertext (sign-then-encrypt).
func EncryptPGPMIME(plaintext []byte, recipients []*openpgp.Entity, signer *openpgp.Entity, sign bool) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no OpenPGP recipients")
	}
	for _, recipient := range recipients {
		if recipient == nil {
			return nil, fmt.Errorf("an OpenPGP recipient is empty")
		}
		if _, ok := recipient.EncryptionKey(time.Now()); !ok {
			return nil, fmt.Errorf("an OpenPGP recipient has no usable encryption key")
		}
	}
	if sign && signer == nil {
		return nil, fmt.Errorf("signing was requested without a signer key")
	}
	var ciphertext bytes.Buffer
	writer, err := openpgp.Encrypt(&ciphertext, recipients, signer, nil, openPGPConfig())
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(plaintext); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("MIME-Version: 1.0\r\n")
	output.WriteString(`Content-Type: multipart/encrypted; protocol="application/pgp-encrypted"; boundary="` + boundary + `"` + "\r\n")
	output.WriteString("\r\n")
	output.WriteString("--" + boundary + "\r\n")
	output.WriteString("Content-Type: application/pgp-encrypted\r\n")
	output.WriteString("\r\n")
	output.WriteString("Version: 1\r\n")
	output.WriteString("--" + boundary + "\r\n")
	output.WriteString("Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\n")
	output.WriteString("Content-Disposition: inline; filename=\"encrypted.asc\"\r\n")
	output.WriteString("Content-Transfer-Encoding: base64\r\n")
	output.WriteString("\r\n")
	output.WriteString(base64Lines(ciphertext.Bytes()))
	output.WriteString("\r\n--" + boundary + "--\r\n")
	return output.Bytes(), nil
}

// SignPGPMIME creates multipart/signed around a complete plaintext MIME entity.
func SignPGPMIME(plaintext []byte, signer *openpgp.Entity) ([]byte, error) {
	if signer == nil {
		return nil, fmt.Errorf("no OpenPGP signer")
	}
	var signature bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&signature, signer, bytes.NewReader(plaintext), openPGPConfig()); err != nil {
		return nil, err
	}
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.WriteString("MIME-Version: 1.0\r\n")
	output.WriteString(`Content-Type: multipart/signed; protocol="application/pgp-signature"; micalg=pgp-sha256; boundary="` + boundary + `"` + "\r\n")
	output.WriteString("\r\n")
	output.WriteString("--" + boundary + "\r\n")
	output.Write(plaintext)
	output.WriteString("\r\n--" + boundary + "\r\n")
	output.WriteString("Content-Type: application/pgp-signature; name=\"signature.asc\"\r\n")
	output.WriteString("Content-Disposition: attachment; filename=\"signature.asc\"\r\n")
	output.WriteString("\r\n")
	output.Write(signature.Bytes())
	output.WriteString("\r\n--" + boundary + "--\r\n")
	return output.Bytes(), nil
}

// DecryptPGPMIME decrypts a message and verifies the inner signature. The
// returned MIME entity has not been parsed or sanitised by this function.
func DecryptPGPMIME(message []byte, identity *OpenPGPKey) ([]byte, SignatureStatus, error) {
	if identity == nil || identity.Entity == nil {
		return nil, SignatureStatus{State: SignatureUnknown}, fmt.Errorf("no OpenPGP identity")
	}
	analysis, err := Analyze(message)
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, err
	}
	if !analysis.Protection.Encrypted || analysis.Protection.Protocol != ProtocolOpenPGP || analysis.Protection.EncryptedPart == nil {
		return nil, SignatureStatus{State: SignatureNone}, fmt.Errorf("message is not a PGP/MIME encrypted entity")
	}
	ciphertext, err := DecodeTransferEncoding(analysis.Protection.EncryptedPart)
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, err
	}
	keyring := openpgp.EntityList{identity.Entity}
	details, err := openpgp.ReadMessage(bytes.NewReader(ciphertext), keyring, nil, openPGPConfig())
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, err
	}
	plaintext, err := io.ReadAll(io.LimitReader(details.UnverifiedBody, 32<<20))
	if err != nil {
		return nil, SignatureStatus{State: SignatureUnknown}, err
	}
	status := SignatureStatus{State: SignatureNone}
	if details.IsSigned {
		status = SignatureStatus{
			State:       SignatureValidUntrusted,
			KeyID:       fmt.Sprintf("%016X", details.SignedByKeyId),
			Fingerprint: strings.ToUpper(hex.EncodeToString(details.SignedByFingerprint)),
			Trust:       TrustUnknown,
		}
		if details.SignedBy != nil {
			status.SignerEmail = firstEntityEmail(details.SignedBy.Entity)
		}
		if details.SignatureError != nil {
			status.State = SignatureInvalid
			status.Reason = details.SignatureError.Error()
		}
	}
	return plaintext, status, nil
}

// VerifyPGPMIMESignature verifies a detached PGP/MIME signature against the
// exact preserved signed entity.
func VerifyPGPMIMESignature(message []byte, publicKeys []*openpgp.Entity) (SignatureStatus, error) {
	analysis, err := Analyze(message)
	if err != nil {
		return SignatureStatus{State: SignatureUnknown}, err
	}
	if !analysis.Protection.Signed || analysis.Protection.Protocol != ProtocolOpenPGP ||
		analysis.Protection.SignedContent == nil || analysis.Protection.Signature == nil {
		return SignatureStatus{State: SignatureNone}, fmt.Errorf("message is not a PGP/MIME signed entity")
	}
	signer, err := openpgp.CheckArmoredDetachedSignature(
		openpgp.EntityList(publicKeys),
		bytes.NewReader(analysis.Protection.SignedContent.Raw),
		bytes.NewReader(analysis.Protection.Signature.RawBody),
		openPGPConfig(),
	)
	if err != nil {
		return SignatureStatus{State: SignatureInvalid, Reason: err.Error()}, nil
	}
	status := SignatureStatus{
		State:       SignatureValidUntrusted,
		SignerEmail: firstEntityEmail(signer),
		Fingerprint: strings.ToUpper(hex.EncodeToString(signer.PrimaryKey.Fingerprint)),
		KeyID:       fmt.Sprintf("%016X", signer.PrimaryKey.KeyId),
		Trust:       TrustUnknown,
	}
	return status, nil
}

func firstEntityEmail(entity *openpgp.Entity) string {
	if entity == nil {
		return ""
	}
	for _, identity := range entity.Identities {
		if identity != nil && identity.UserId != nil {
			return identity.UserId.Email
		}
	}
	return ""
}

func base64Lines(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var builder strings.Builder
	for len(encoded) > 76 {
		builder.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	builder.WriteString(encoded)
	return builder.String()
}

func randomBoundary() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "a8m-" + hex.EncodeToString(random), nil
}

// OpenPGPProvider implements Provider for server-managed OpenPGP. Key material
// is supplied by the key service after unwrapping; the provider never reads
// the database.
type OpenPGPProvider struct{}

func (OpenPGPProvider) Protocol() Protocol { return ProtocolOpenPGP }

func (OpenPGPProvider) Capabilities() Capabilities {
	return Capabilities{
		CanEncrypt: true, CanSign: true, CanDecrypt: true, CanVerify: true,
		Algorithms: []string{"rsa3072", "aes256", "sha256"},
	}
}

func (p OpenPGPProvider) Encrypt(_ context.Context, req EncryptRequest) (EncryptResult, error) {
	if len(req.PrivateKey) == 0 && req.Sign {
		return EncryptResult{}, fmt.Errorf("signing requires a private key")
	}
	var signer *openpgp.Entity
	if len(req.PrivateKey) > 0 {
		key, err := ParseOpenPGPPrivateKey(req.PrivateKey, nil)
		if err != nil {
			return EncryptResult{}, err
		}
		signer = key.Entity
	}
	recipients := make([]*openpgp.Entity, 0, len(req.Recipients))
	keyIDs := make([]string, 0, len(req.Recipients))
	for _, recipient := range req.Recipients {
		entity, err := ParseOpenPGPPublicKey(recipient.PublicKey)
		if err != nil {
			return EncryptResult{}, fmt.Errorf("%s: %w", recipient.Email, err)
		}
		recipients = append(recipients, entity)
		keyIDs = append(keyIDs, recipient.KeyID)
	}
	if req.EncryptToSelf && signer != nil {
		recipients = append(recipients, signer)
	}
	encryptSigner := signer
	if !req.Sign {
		encryptSigner = nil
	}
	mime, err := EncryptPGPMIME(req.MIME, recipients, encryptSigner, req.Sign)
	if err != nil {
		return EncryptResult{}, err
	}
	return EncryptResult{Protocol: ProtocolOpenPGP, MIME: mime, RecipientKeyIDs: keyIDs, Signed: req.Sign}, nil
}

func (p OpenPGPProvider) Sign(_ context.Context, req SignRequest) (SignResult, error) {
	if len(req.PrivateKey) == 0 {
		return SignResult{}, fmt.Errorf("signing requires a private key")
	}
	key, err := ParseOpenPGPPrivateKey(req.PrivateKey, nil)
	if err != nil {
		return SignResult{}, err
	}
	mime, err := SignPGPMIME(req.MIME, key.Entity)
	if err != nil {
		return SignResult{}, err
	}
	return SignResult{Protocol: ProtocolOpenPGP, MIME: mime, KeyID: key.KeyID}, nil
}

func (p OpenPGPProvider) Decrypt(_ context.Context, req DecryptRequest) (DecryptResult, error) {
	if len(req.PrivateKey) == 0 {
		return DecryptResult{}, fmt.Errorf("decryption requires a private key")
	}
	key, err := ParseOpenPGPPrivateKey(req.PrivateKey, nil)
	if err != nil {
		return DecryptResult{}, err
	}
	mime, status, err := DecryptPGPMIME(req.Message, key)
	if err != nil {
		return DecryptResult{}, err
	}
	return DecryptResult{Protocol: ProtocolOpenPGP, MIME: mime, Signature: status}, nil
}

func (p OpenPGPProvider) Verify(_ context.Context, req VerifyRequest) (SignatureStatus, error) {
	if len(req.PublicKey) == 0 {
		return SignatureStatus{State: SignatureUnknown}, fmt.Errorf("verification requires a public key")
	}
	entity, err := ParseOpenPGPPublicKey(req.PublicKey)
	if err != nil {
		return SignatureStatus{State: SignatureUnknown}, err
	}
	return VerifyPGPMIMESignature(req.Message, []*openpgp.Entity{entity})
}
