package mailcrypto

import (
	"bytes"
	"fmt"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"
)

const MaxAnalyzeBytes = 32 << 20

// Analyze inspects one complete RFC 822 message without decrypting it and
// without modifying the bytes. The returned Part tree keeps exact raw entities
// so a later verification step can use the sender's bytes, not a re-encoding.
func Analyze(raw []byte) (Analysis, error) {
	if len(raw) == 0 {
		return Analysis{}, fmt.Errorf("message is empty")
	}
	if len(raw) > MaxAnalyzeBytes {
		return Analysis{}, fmt.Errorf("message exceeds the %d-byte analysis limit", MaxAnalyzeBytes)
	}
	parser := &partParser{}
	root, err := parser.parseRawPart(raw, 0)
	if err != nil {
		return Analysis{}, err
	}
	protection := Protection{}
	parser.inspect(root, &protection, true, false)
	return Analysis{
		Headers:    headerInfo(root.Headers),
		Protection: protection,
		Parts:      flattenParts(root),
		Warnings:   append(append([]string{}, parser.warnings...), protectionWarnings(protection)...),
		Size:       len(raw),
	}, nil
}

func flattenParts(root *Part) []*Part {
	if root == nil {
		return nil
	}
	out := make([]*Part, 0, 8)
	var walk func(*Part)
	walk = func(part *Part) {
		out = append(out, part)
		for _, child := range part.Children {
			walk(child)
		}
	}
	walk(root)
	return out
}

func (p *partParser) inspect(part *Part, protection *Protection, top, attachment bool) {
	if part == nil {
		return
	}
	mediaType := part.MediaType
	disposition := part.Disposition
	isAttachment := attachment || (!top && (disposition == "attachment" || part.DispositionParams["filename"] != ""))

	switch mediaType {
	case "multipart/signed":
		p.inspectSigned(part, protection, isAttachment)
	case "multipart/encrypted":
		p.inspectEncrypted(part, protection, isAttachment)
	case "application/pkcs7-mime", "application/x-pkcs7-mime":
		p.inspectPKCS7(part, protection, isAttachment)
	case "application/pkcs7-signature", "application/x-pkcs7-signature":
		if isAttachment {
			protection.HasSignedAttachment = true
			return
		}
		if top {
			p.warn("top-level S/MIME signature has no signed content")
			protection.Signed = true
			protection.Protocol = ProtocolSMIME
			protection.Kind = KindUnknownProtected
			protection.Signature = part
		}
	case "application/pgp-encrypted":
		if isAttachment {
			protection.HasEncryptedAttachment = true
			if protection.Kind == KindNone {
				protection.Kind = KindEncryptedAttachment
			}
			return
		}
		protection.Encrypted = true
		protection.Protocol = ProtocolOpenPGP
		protection.Kind = KindPGPMIMEEncrypted
		protection.EncryptedPart = part
		if protection.PGP == nil {
			protection.PGP = &PGPInfo{}
		}
	case "application/pgp-signature":
		if isAttachment {
			protection.HasSignedAttachment = true
			return
		}
		if top {
			p.warn("top-level PGP signature has no signed content")
			protection.Signed = true
			protection.Protocol = ProtocolOpenPGP
			protection.Kind = KindUnknownProtected
			protection.Signature = part
		}
	case "multipart/mixed", "multipart/alternative", "multipart/related", "multipart/report", "multipart/digest", "multipart/parallel":
		for _, child := range part.Children {
			childProtection := Protection{}
			p.inspect(child, &childProtection, false, isAttachment)
			mergeProtection(protection, childProtection)
		}
	case "message/rfc822", "message/global":
		if top {
			p.warn("attached message/rfc822 content was not inspected")
		}
	default:
		if !isAttachment && isTextual(mediaType) {
			p.inspectInline(part, protection)
		}
	}
}

func (p *partParser) inspectSigned(part *Part, protection *Protection, attachment bool) {
	if len(part.Children) < 2 {
		p.warn("multipart/signed has %d parts; expected a signed entity and a signature", len(part.Children))
		protection.Kind = KindMalformed
		return
	}
	if attachment {
		protection.HasSignedAttachment = true
		return
	}
	signedContent := part.Children[0]
	signaturePart := part.Children[1]
	protocol := strings.ToLower(strings.TrimSpace(part.Params["protocol"]))
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(signaturePart.MediaType))
	}
	signatureInfo := &Protection{
		Signed:        true,
		SignedContent: signedContent,
		Signature:     signaturePart,
	}
	switch protocol {
	case "application/pkcs7-signature", "application/x-pkcs7-signature", "application/pkcs7-mime":
		signatureInfo.Protocol = ProtocolSMIME
		signatureInfo.Kind = KindSMIMESigned
		signatureInfo.SMIME = &SMIMEInfo{
			Type:            "signed-data",
			Protocol:        protocol,
			DigestAlgorithm: strings.TrimSpace(part.Params["micalg"]),
		}
	case "application/pgp-signature":
		signatureInfo.Protocol = ProtocolOpenPGP
		signatureInfo.Kind = KindPGPMIMESigned
		signatureInfo.PGP = &PGPInfo{}
	default:
		p.warn("multipart/signed uses unsupported protocol %q", protocol)
		signatureInfo.Kind = KindUnknownProtected
	}
	// A signed multipart may itself contain an encrypted entity (for example a
	// gateway signs an already-encrypted message). Inspect it for that shape.
	nested := Protection{}
	p.inspect(signedContent, &nested, false, false)
	if nested.Encrypted {
		signatureInfo.Encrypted = true
		if signatureInfo.Kind == KindSMIMESigned || signatureInfo.Kind == KindPGPMIMESigned {
			if signatureInfo.Protocol == ProtocolSMIME {
				signatureInfo.Kind = KindSMIMESignedEncrypted
			}
		}
	}
	if signatureInfo.Encrypted && signatureInfo.Protocol == ProtocolSMIME {
		signatureInfo.Kind = KindSMIMESignedEncrypted
	}
	mergeProtection(protection, *signatureInfo)
}

func (p *partParser) inspectEncrypted(part *Part, protection *Protection, attachment bool) {
	if len(part.Children) < 2 {
		p.warn("multipart/encrypted has %d parts; expected a version part and ciphertext", len(part.Children))
		protection.Kind = KindMalformed
		return
	}
	if attachment {
		protection.HasEncryptedAttachment = true
		if protection.Kind == KindNone {
			protection.Kind = KindEncryptedAttachment
		}
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(part.Params["protocol"]))
	versionPart := part.Children[0]
	ciphertext := part.Children[len(part.Children)-1]
	info := &Protection{
		Encrypted:     true,
		EncryptedPart: ciphertext,
	}
	switch {
	case protocol == "application/pgp-encrypted" || protocol == "application/pgp-signature":
		info.Protocol = ProtocolOpenPGP
		info.Kind = KindPGPMIMEEncrypted
		info.PGP = &PGPInfo{}
		if decoded, err := DecodeTransferEncoding(versionPart); err == nil {
			info.PGP.Version = pgpVersion(string(decoded))
		}
		if strings.EqualFold(strings.TrimSpace(versionPart.MediaType), "application/pgp-encrypted") {
			if info.PGP.Version == "" {
				p.warn("PGP/MIME version part did not expose Version: 1")
			}
		} else {
			p.warn("PGP/MIME version part has media type %q", versionPart.MediaType)
		}
	case strings.HasPrefix(protocol, "application/pkcs7-mime"), protocol == "application/pkcs7-signature":
		info.Protocol = ProtocolSMIME
		info.Kind = KindSMIMEEnveloped
		info.SMIME = &SMIMEInfo{Type: "enveloped-data", Protocol: protocol}
	default:
		p.warn("multipart/encrypted uses unsupported protocol %q", protocol)
		info.Kind = KindUnknownProtected
	}
	mergeProtection(protection, *info)
}

func pgpVersion(body string) string {
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "version:") {
			return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}
	return ""
}

func (p *partParser) inspectPKCS7(part *Part, protection *Protection, attachment bool) {
	smimeType := strings.ToLower(strings.TrimSpace(part.Params["smime-type"]))
	if smimeType == "" {
		smimeType = strings.ToLower(strings.TrimSpace(part.Params["smime_type"]))
	}
	info := &Protection{SMIME: &SMIMEInfo{Type: smimeType, Protocol: part.MediaType}}
	switch smimeType {
	case "enveloped-data":
		info.Encrypted = true
		info.Protocol = ProtocolSMIME
		info.Kind = KindSMIMEEnveloped
		info.EncryptedPart = part
	case "signed-data":
		info.Signed = true
		info.Protocol = ProtocolSMIME
		info.Kind = KindSMIMESigned
		info.Signature = part
	case "certs-only":
		info.Kind = KindSMIMECertsOnly
	case "compressed-data":
		p.warn("S/MIME compressed-data is not unwrapped in Phase 0")
		info.Kind = KindUnknownProtected
	default:
		p.warn("application/pkcs7-mime has unknown smime-type %q", smimeType)
		info.Kind = KindUnknownProtected
	}
	if attachment {
		switch {
		case info.Encrypted:
			info.Encrypted = false
			info.HasEncryptedAttachment = true
			info.Kind = KindEncryptedAttachment
		case info.Signed:
			info.Signed = false
			info.HasSignedAttachment = true
			info.Kind = KindNone
		}
	}
	mergeProtection(protection, *info)
}

func (p *partParser) inspectInline(part *Part, protection *Protection) {
	body, err := DecodeTransferEncoding(part)
	if err != nil {
		p.warn("part %d transfer encoding could not be decoded: %v", part.Index, err)
		return
	}
	trimmed := strings.TrimSpace(string(body))
	switch {
	case strings.HasPrefix(trimmed, "-----BEGIN PGP MESSAGE-----"):
		protection.Encrypted = true
		protection.Protocol = ProtocolOpenPGP
		protection.Kind = KindPGPInlineEncrypted
		protection.EncryptedPart = part
		protection.PGP = &PGPInfo{Inline: true}
	case strings.HasPrefix(trimmed, "-----BEGIN PGP SIGNED MESSAGE-----"):
		protection.Signed = true
		protection.Protocol = ProtocolOpenPGP
		protection.Kind = KindPGPInlineSigned
		protection.SignedContent = part
		protection.PGP = &PGPInfo{Inline: true}
	case strings.HasPrefix(trimmed, "-----BEGIN PGP PUBLIC KEY BLOCK-----"):
		p.warn("message contains a PGP public key block; it is not an encrypted payload")
	}
}

func mergeProtection(dst *Protection, src Protection) {
	if src.Encrypted {
		dst.Encrypted = true
	}
	if src.Signed {
		dst.Signed = true
	}
	if src.HasEncryptedAttachment {
		dst.HasEncryptedAttachment = true
	}
	if src.HasSignedAttachment {
		dst.HasSignedAttachment = true
	}
	if dst.Protocol == ProtocolNone && src.Protocol != ProtocolNone {
		dst.Protocol = src.Protocol
	}
	if dst.Kind == KindNone && src.Kind != KindNone {
		dst.Kind = src.Kind
	}
	if dst.SMIME == nil {
		dst.SMIME = src.SMIME
	}
	if dst.PGP == nil {
		dst.PGP = src.PGP
	}
	if dst.SignedContent == nil {
		dst.SignedContent = src.SignedContent
	}
	if dst.Signature == nil {
		dst.Signature = src.Signature
	}
	if dst.EncryptedPart == nil {
		dst.EncryptedPart = src.EncryptedPart
	}
}

func protectionWarnings(protection Protection) []string {
	var warnings []string
	if protection.Encrypted && protection.Signed {
		warnings = append(warnings, "message is both signed and encrypted; the visible signature may not cover the ciphertext")
	}
	if protection.HasEncryptedAttachment && !protection.Encrypted {
		warnings = append(warnings, "message contains an encrypted attachment but the message body itself is not encrypted")
	}
	if protection.HasSignedAttachment && !protection.Signed {
		warnings = append(warnings, "message contains a signed attachment but the message body itself is not signed")
	}
	return warnings
}

func isTextual(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/octet-stream" ||
		mediaType == "application/pgp"
}

func headerInfo(headers textproto.MIMEHeader) HeaderInfo {
	info := HeaderInfo{
		From:        headerAddresses(headers, "From"),
		Sender:      headerValue(headers, "Sender"),
		ReplyTo:     headerAddresses(headers, "Reply-To"),
		To:          headerAddresses(headers, "To"),
		Cc:          headerAddresses(headers, "Cc"),
		Bcc:         headerAddresses(headers, "Bcc"),
		Subject:     headerValue(headers, "Subject"),
		MessageID:   strings.TrimSpace(headerValue(headers, "Message-Id")),
		Date:        strings.TrimSpace(headerValue(headers, "Date")),
		ContentType: headerValue(headers, "Content-Type"),
	}
	return info
}

func headerValue(headers textproto.MIMEHeader, name string) string {
	raw := headers.Get(name)
	if raw == "" {
		return ""
	}
	decoded := raw
	if value, err := (&mime.WordDecoder{}).DecodeHeader(raw); err == nil {
		decoded = value
	}
	return rewriteHeaderText(decoded)
}

func headerAddresses(headers textproto.MIMEHeader, name string) []string {
	raw := headers.Get(name)
	if raw == "" {
		return nil
	}
	decoded := raw
	if value, err := (&mime.WordDecoder{}).DecodeHeader(raw); err == nil {
		decoded = value
	}
	decoded = rewriteHeaderText(decoded)
	list, err := mail.ParseAddressList(decoded)
	if err != nil {
		if decoded == "" {
			return nil
		}
		return []string{decoded}
	}
	out := make([]string, 0, len(list))
	for _, address := range list {
		out = append(out, address.String())
	}
	return out
}

func rewriteHeaderText(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

// CanonicalizeCRLF normalizes line endings to CRLF without changing any other
// byte. It is used only by explicit signature/canonicalization helpers.
func CanonicalizeCRLF(data []byte) []byte {
	out := make([]byte, 0, len(data)+len(data)/16+2)
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '\r':
			if index+1 < len(data) && data[index+1] == '\n' {
				out = append(out, '\r', '\n')
				index++
				continue
			}
			out = append(out, '\r', '\n')
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, data[index])
		}
	}
	return out
}

// EnsureTrailingCRLF makes a MIME entity end with exactly one CRLF.
func EnsureTrailingCRLF(data []byte) []byte {
	normalized := CanonicalizeCRLF(data)
	normalized = bytes.TrimRight(normalized, "\r\n")
	return append(normalized, '\r', '\n')
}

// SignedEntity returns the exact MIME entity bytes for a signed part,
// normalized only to CRLF. Phase 1 must validate this against RFC 5751 and
// RFC 3156 fixtures before verification is enabled.
func SignedEntity(part *Part) []byte {
	if part == nil {
		return nil
	}
	return CanonicalizeCRLF(part.Raw)
}
