package mailcrypto

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
)

const (
	maxMIMEDepth = 24
	maxMIMEParts = 512
)

var ErrBoundary = errors.New("multipart boundary is missing or malformed")

type partParser struct {
	warnings []string
	index    int
}

func (p *partParser) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

// parseRawPart parses one MIME entity and keeps every byte of it. It is used
// instead of mime/multipart because signature verification must not depend on
// a parser's header re-serialization.
func (p *partParser) parseRawPart(raw []byte, depth int) (*Part, error) {
	if depth > maxMIMEDepth {
		return nil, fmt.Errorf("MIME nesting exceeds %d levels", maxMIMEDepth)
	}
	header, body, ok := splitRawHeaderBody(raw)
	if !ok || !looksLikeHeaderBlock(header) {
		// MIME allows an entity with no header block at all. OpenSSL emits this
		// shape for the first part of an S/MIME signed entity, and a headerless
		// body may itself contain blank lines.
		header = nil
		body = raw
	}
	p.index++
	part := &Part{
		Index:      p.index,
		Depth:      depth,
		Raw:        raw,
		RawHeaders: header,
		RawBody:    body,
		Params:     map[string]string{},
	}
	headers, err := parseMIMEHeader(header)
	if err != nil {
		p.warn("part %d has malformed headers: %v", part.Index, err)
	}
	part.Headers = headers
	mediaType, params := contentTypeFromHeaders(headers)
	part.MediaType = mediaType
	part.Params = params
	disposition, dispositionParams := dispositionFromHeaders(headers)
	part.Disposition = disposition
	part.DispositionParams = dispositionParams
	part.TransferEncoding = strings.ToLower(strings.TrimSpace(headers.Get("Content-Transfer-Encoding")))

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			p.warn("part %d is %s without a boundary", part.Index, mediaType)
			return part, nil
		}
		children, err := p.parseMultipartBody(body, boundary, depth+1)
		if err != nil {
			p.warn("part %d multipart parse: %v", part.Index, err)
		}
		part.Children = children
	}
	return part, nil
}

func (p *partParser) parseMultipartBody(body []byte, boundary string, depth int) ([]*Part, error) {
	segments, closed, err := rawMultipartSegments(body, boundary)
	if err != nil {
		return nil, err
	}
	if !closed {
		p.warn("multipart boundary %q has no closing delimiter", boundary)
	}
	parts := make([]*Part, 0, len(segments))
	for _, segment := range segments {
		if len(parts) >= maxMIMEParts {
			p.warn("multipart has more than %d parts; remaining parts ignored", maxMIMEParts)
			break
		}
		child, childErr := p.parseRawPart(segment, depth)
		if childErr != nil {
			p.warn("multipart part %d parse: %v", len(parts)+1, childErr)
			continue
		}
		parts = append(parts, child)
	}
	return parts, nil
}

// rawMultipartSegments splits a multipart body into exact entity bytes. The
// CRLF immediately before a boundary delimiter is part of the delimiter and is
// removed from the preceding entity.
func rawMultipartSegments(body []byte, boundary string) ([][]byte, bool, error) {
	if boundary == "" {
		return nil, false, ErrBoundary
	}
	delimiter := "--" + boundary
	segments := make([][]byte, 0, 4)
	inPart := false
	partStart := 0
	closed := false

	for offset := 0; offset <= len(body); {
		lineStart := offset
		lineEnd, next := nextLine(body, offset)
		line := body[lineStart:lineEnd]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		trimmed := strings.TrimRight(string(line), " \t")
		if trimmed == delimiter || trimmed == delimiter+"--" {
			if inPart {
				end := lineStart
				// The line break before the delimiter belongs to the delimiter.
				if end >= 2 && body[end-2] == '\r' && body[end-1] == '\n' {
					end -= 2
				} else if end >= 1 && body[end-1] == '\n' {
					end--
				}
				if end < partStart {
					end = partStart
				}
				segments = append(segments, body[partStart:end])
			}
			if trimmed == delimiter+"--" {
				closed = true
				break
			}
			inPart = true
			partStart = next
		}
		if next <= offset {
			break
		}
		offset = next
	}
	if inPart && !closed && partStart <= len(body) {
		// Some senders omit the closing delimiter. Keep the final entity.
		segments = append(segments, body[partStart:])
	}
	return segments, closed, nil
}

func nextLine(data []byte, offset int) (lineEnd int, next int) {
	if offset >= len(data) {
		return len(data), len(data)
	}
	if index := bytes.IndexByte(data[offset:], '\n'); index >= 0 {
		return offset + index, offset + index + 1
	}
	return len(data), len(data)
}

func splitRawHeaderBody(raw []byte) (header, body []byte, ok bool) {
	if index := bytes.Index(raw, []byte("\r\n\r\n")); index >= 0 {
		return raw[:index], raw[index+4:], true
	}
	if index := bytes.Index(raw, []byte("\n\n")); index >= 0 {
		return raw[:index], raw[index+2:], true
	}
	return nil, nil, false
}

// looksLikeHeaderBlock distinguishes an actual MIME header block from a
// headerless body whose text happens to contain a blank line. A header line is
// a field name followed by a colon; continuation lines start with whitespace.
func looksLikeHeaderBlock(raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	sawField := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if !sawField {
				return false
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			return false
		}
		name := strings.TrimSpace(line[:colon])
		if name == "" {
			return false
		}
		for _, char := range name {
			if !(char >= 'A' && char <= 'Z') && !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
		sawField = true
	}
	return sawField
}

func parseMIMEHeader(raw []byte) (textproto.MIMEHeader, error) {
	if len(raw) == 0 {
		return textproto.MIMEHeader{}, nil
	}
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(append([]byte{}, raw...), "\r\n\r\n"...))))
	headers, err := reader.ReadMIMEHeader()
	if err != nil {
		return textproto.MIMEHeader{}, err
	}
	return headers, nil
}

func contentTypeFromHeaders(headers textproto.MIMEHeader) (string, map[string]string) {
	value := strings.TrimSpace(headers.Get("Content-Type"))
	if value == "" {
		return "text/plain", map[string]string{}
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0])), map[string]string{}
	}
	return strings.ToLower(mediaType), params
}

func dispositionFromHeaders(headers textproto.MIMEHeader) (string, map[string]string) {
	value := strings.TrimSpace(headers.Get("Content-Disposition"))
	if value == "" {
		return "", map[string]string{}
	}
	disposition, params, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0])), map[string]string{}
	}
	return strings.ToLower(disposition), params
}

// DecodeTransferEncoding decodes a MIME part body. It is used only when a
// caller needs content such as the PGP/MIME version part; signature
// verification must continue to use the raw bytes.
func DecodeTransferEncoding(part *Part) ([]byte, error) {
	if part == nil {
		return nil, errors.New("nil MIME part")
	}
	return decodeTransferEncoding(part.RawBody, part.TransferEncoding)
}

func decodeTransferEncoding(body []byte, transferEncoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "base64":
		return io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(body)), 8<<20))
	case "quoted-printable":
		return io.ReadAll(io.LimitReader(quotedprintable.NewReader(bytes.NewReader(body)), 8<<20))
	default:
		return body, nil
	}
}
