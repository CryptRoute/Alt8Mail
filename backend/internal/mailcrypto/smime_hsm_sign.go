package mailcrypto

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"time"
)

// SignSMIMEMIMEWithSigner builds the same detached multipart/signed entity as
// SignSMIMEMIME, but signs the CMS signed attributes through crypto.Signer so
// the key can stay inside an HSM or smart-card token.

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
)

type cmsAttribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

type cmsSignerInfo struct {
	Version                   int
	IssuerAndSerialNumber     smimeIssuerAndSerial
	DigestAlgorithm           pkix.AlgorithmIdentifier
	SignedAttrs               asn1.RawValue `asn1:"optional"`
	DigestEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedDigest           []byte
}

type cmsSignedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	ContentInfo      cmsEncapsulatedContent
	Certificates     asn1.RawValue   `asn1:"optional"`
	SignerInfos      []cmsSignerInfo `asn1:"set"`
}

type cmsEncapsulatedContent struct {
	ContentType asn1.ObjectIdentifier
}

func cmsRawValue(value any) (asn1.RawValue, error) {
	der, err := asn1.Marshal(value)
	if err != nil {
		return asn1.RawValue{}, err
	}
	return asn1.RawValue{FullBytes: der}, nil
}

func cmsAlgorithm(oid asn1.ObjectIdentifier) pkix.AlgorithmIdentifier {
	return pkix.AlgorithmIdentifier{
		Algorithm:  oid,
		Parameters: asn1.RawValue{Tag: asn1.TagNull},
	}
}

func SignSMIMEMIMEWithSigner(plaintext []byte, certificate *x509.Certificate, chain []*x509.Certificate, signer crypto.Signer) ([]byte, error) {
	if certificate == nil || signer == nil {
		return nil, fmt.Errorf("an S/MIME certificate and signer are required")
	}
	dataDigest := sha256.Sum256(plaintext)
	contentTypeValue, err := cmsRawValue(oidData)
	if err != nil {
		return nil, err
	}
	messageDigestValue, err := cmsRawValue(dataDigest[:])
	if err != nil {
		return nil, err
	}
	signingTimeValue, err := cmsRawValue(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	attributes := []cmsAttribute{
		{Type: oidContentType, Values: []asn1.RawValue{contentTypeValue}},
		{Type: oidMessageDigest, Values: []asn1.RawValue{messageDigestValue}},
		{Type: oidSigningTime, Values: []asn1.RawValue{signingTimeValue}},
	}
	attributesDER, err := asn1.MarshalWithParams(attributes, "set")
	if err != nil {
		return nil, fmt.Errorf("encode S/MIME signed attributes: %w", err)
	}
	var attributesRaw asn1.RawValue
	if _, err := asn1.Unmarshal(attributesDER, &attributesRaw); err != nil {
		return nil, err
	}
	attributesDigest := sha256.Sum256(attributesDER)
	signature, err := signer.Sign(rand.Reader, attributesDigest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("sign S/MIME attributes: %w", err)
	}
	var certificateBytes bytes.Buffer
	for _, item := range append([]*x509.Certificate{certificate}, chain...) {
		if item == nil {
			continue
		}
		certificateBytes.Write(item.Raw)
	}
	signedData := cmsSignedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{cmsAlgorithm(oidSHA256)},
		ContentInfo:      cmsEncapsulatedContent{ContentType: oidData},
		Certificates: asn1.RawValue{
			Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certificateBytes.Bytes(),
		},
		SignerInfos: []cmsSignerInfo{{
			Version: 1,
			IssuerAndSerialNumber: smimeIssuerAndSerial{
				IssuerName:   asn1.RawValue{FullBytes: certificate.RawIssuer},
				SerialNumber: certificate.SerialNumber,
			},
			DigestAlgorithm: cmsAlgorithm(oidSHA256),
			SignedAttrs: asn1.RawValue{
				Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: attributesRaw.Bytes,
			},
			DigestEncryptionAlgorithm: cmsAlgorithm(oidRSAEncryption),
			EncryptedDigest:           signature,
		}},
	}
	signedDataDER, err := asn1.Marshal(signedData)
	if err != nil {
		return nil, fmt.Errorf("encode S/MIME SignedData: %w", err)
	}
	contentInfo := struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue
	}{
		ContentType: oidSignedData,
		Content: asn1.RawValue{
			Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: signedDataDER,
		},
	}
	signatureDER, err := asn1.Marshal(contentInfo)
	if err != nil {
		return nil, fmt.Errorf("encode S/MIME ContentInfo: %w", err)
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
	output.WriteString(base64Lines(signatureDER))
	output.WriteString("\r\n--" + boundary + "--\r\n")
	return output.Bytes(), nil
}
