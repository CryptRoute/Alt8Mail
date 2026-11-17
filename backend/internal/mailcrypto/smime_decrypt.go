package mailcrypto

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
)

// The PKCS#7 library used for the server-managed path accepts only an
// *rsa.PrivateKey. HSM and smart-card sessions expose crypto.Decrypter, so this
// small RFC 5652 reader performs the envelope unwrap with that interface.

type smimeContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"tag:0,explicit,optional"`
}

type smimeIssuerAndSerial struct {
	IssuerName   asn1.RawValue
	SerialNumber *big.Int
}

type smimeRecipientInfo struct {
	Version                int
	IssuerAndSerialNumber  smimeIssuerAndSerial
	KeyEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedKey           []byte
}

type smimeEncryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"tag:0,optional"`
}

type smimeEnvelopedData struct {
	Version              int
	OriginatorInfo       asn1.RawValue        `asn1:"tag:0,optional"`
	RecipientInfos       []smimeRecipientInfo `asn1:"set"`
	EncryptedContentInfo smimeEncryptedContentInfo
	UnprotectedAttrs     asn1.RawValue `asn1:"tag:1,optional"`
}

var (
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidRSAEncryption = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidAES256CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidAES128CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES256GCM     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 46}
	oidAES128GCM     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 6}
	oidDESEDE3CBC    = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
	oidDESCBC        = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 7}
)

// DecryptSMIMEEnvelopeWithDecrypter unwraps a CMS EnvelopedData using a key
// operation exposed by a token. It supports the content ciphers emitted by the
// server-managed path and common external S/MIME senders.
func DecryptSMIMEEnvelopeWithDecrypter(der []byte, certificate *x509.Certificate, decrypter crypto.Decrypter) ([]byte, error) {
	if certificate == nil || decrypter == nil {
		return nil, fmt.Errorf("an S/MIME certificate and decrypter are required")
	}
	var contentInfo smimeContentInfo
	if rest, err := asn1.Unmarshal(der, &contentInfo); err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("parse CMS ContentInfo: %w", err)
	}
	if !contentInfo.ContentType.Equal(oidEnvelopedData) || len(contentInfo.Content.Bytes) == 0 {
		return nil, fmt.Errorf("the CMS message is not EnvelopedData")
	}
	var envelope smimeEnvelopedData
	if rest, err := asn1.Unmarshal(contentInfo.Content.Bytes, &envelope); err != nil || len(rest) != 0 {
		return nil, fmt.Errorf("parse CMS EnvelopedData: %w", err)
	}
	recipient, err := selectSMIMERecipient(envelope.RecipientInfos, certificate)
	if err != nil {
		return nil, err
	}
	if !recipient.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAEncryption) {
		return nil, fmt.Errorf("the S/MIME recipient uses an unsupported key transport algorithm")
	}
	contentKey, err := decrypter.Decrypt(rand.Reader, recipient.EncryptedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap the S/MIME content key: %w", err)
	}
	return decryptSMIMEContent(envelope.EncryptedContentInfo, contentKey)
}

func selectSMIMERecipient(recipients []smimeRecipientInfo, certificate *x509.Certificate) (smimeRecipientInfo, error) {
	for _, recipient := range recipients {
		if !bytes.Equal(recipient.IssuerAndSerialNumber.IssuerName.FullBytes, certificate.RawIssuer) {
			continue
		}
		if recipient.IssuerAndSerialNumber.SerialNumber == nil ||
			recipient.IssuerAndSerialNumber.SerialNumber.Cmp(certificate.SerialNumber) != 0 {
			continue
		}
		return recipient, nil
	}
	return smimeRecipientInfo{}, fmt.Errorf("no S/MIME recipient matches the certificate")
}

func decryptSMIMEContent(info smimeEncryptedContentInfo, key []byte) ([]byte, error) {
	ciphertext, err := smimeEncryptedContentBytes(info.EncryptedContent)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 {
		return nil, fmt.Errorf("the S/MIME envelope has no ciphertext")
	}
	parameters := info.ContentEncryptionAlgorithm.Parameters.FullBytes
	switch {
	case info.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES256CBC):
		return decryptSMIMECBC(ciphertext, key, 32, parameters, 16)
	case info.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES128CBC):
		return decryptSMIMECBC(ciphertext, key, 16, parameters, 16)
	case info.ContentEncryptionAlgorithm.Algorithm.Equal(oidDESEDE3CBC):
		return decryptSMIMECBC(ciphertext, key, 24, parameters, 8)
	case info.ContentEncryptionAlgorithm.Algorithm.Equal(oidDESCBC):
		return decryptSMIMECBC(ciphertext, key, 8, parameters, 8)
	case info.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES256GCM), info.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES128GCM):
		return decryptSMIMEGCM(ciphertext, key, parameters)
	default:
		return nil, fmt.Errorf("unsupported S/MIME content encryption algorithm")
	}
}

func smimeEncryptedContentBytes(raw asn1.RawValue) ([]byte, error) {
	if !raw.IsCompound {
		return raw.Bytes, nil
	}
	var out bytes.Buffer
	rest := raw.Bytes
	for len(rest) > 0 {
		var part []byte
		next, err := asn1.Unmarshal(rest, &part)
		if err != nil {
			return nil, fmt.Errorf("read constructed S/MIME ciphertext: %w", err)
		}
		out.Write(part)
		rest = next
	}
	return out.Bytes(), nil
}

func smimeIV(parameters []byte, expected int) ([]byte, error) {
	var iv []byte
	if len(parameters) > 0 {
		if _, err := asn1.Unmarshal(parameters, &iv); err != nil {
			return nil, fmt.Errorf("read S/MIME IV: %w", err)
		}
	}
	if len(iv) != expected {
		return nil, fmt.Errorf("S/MIME IV has %d bytes, expected %d", len(iv), expected)
	}
	return iv, nil
}

func decryptSMIMECBC(ciphertext, key []byte, keyLength int, parameters []byte, ivLength int) ([]byte, error) {
	if len(key) != keyLength {
		return nil, fmt.Errorf("S/MIME content key has %d bytes, expected %d", len(key), keyLength)
	}
	var block cipher.Block
	var err error
	switch keyLength {
	case 8:
		block, err = des.NewCipher(key)
	case 16, 32:
		block, err = aes.NewCipher(key)
	case 24:
		block, err = des.NewTripleDESCipher(key)
	default:
		return nil, fmt.Errorf("unsupported S/MIME content key length %d", keyLength)
	}
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("S/MIME ciphertext is not block-aligned")
	}
	iv, err := smimeIV(parameters, ivLength)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	return stripSMIMEPadding(plaintext, block.BlockSize())
}

func stripSMIMEPadding(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 {
		return nil, fmt.Errorf("the S/MIME plaintext is empty")
	}
	padding := int(value[len(value)-1])
	if padding == 0 || padding > blockSize || padding > len(value) {
		return nil, fmt.Errorf("the S/MIME padding is invalid")
	}
	for _, item := range value[len(value)-padding:] {
		if int(item) != padding {
			return nil, fmt.Errorf("the S/MIME padding is invalid")
		}
	}
	return value[:len(value)-padding], nil
}

func decryptSMIMEGCM(ciphertext, key []byte, parameters []byte) ([]byte, error) {
	if len(key) != 16 && len(key) != 32 {
		return nil, fmt.Errorf("S/MIME AES-GCM content key has an invalid length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var params struct {
		Nonce  []byte
		ICVLen int
	}
	if _, err := asn1.Unmarshal(parameters, &params); err != nil {
		return nil, fmt.Errorf("read S/MIME GCM parameters: %w", err)
	}
	if len(params.Nonce) != gcm.NonceSize() || params.ICVLen != gcm.Overhead() {
		return nil, fmt.Errorf("S/MIME GCM parameters are invalid")
	}
	plaintext, err := gcm.Open(nil, params.Nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("authenticate S/MIME GCM ciphertext: %w", err)
	}
	return plaintext, nil
}
