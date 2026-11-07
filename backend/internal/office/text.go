package office

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// maxTextBytes bounds the uncompressed XML we read from a document. Office
	// files can be zip bombs; extraction must never expand without a ceiling.
	maxTextBytes = 24 << 20
	// MaxExtractedText is the number of characters returned to callers. It is
	// far more than DLP or a line diff needs and keeps memory predictable.
	MaxExtractedText = 400_000
)

// ExtractText returns the best-effort plain text of an Office document. It is
// used for DLP-on-save and version comparison, never for rendering. PDFs have
// no text extraction in the dependency-free build, and legacy binary Office
// formats fall back to readable string runs.
func ExtractText(name string, data []byte) (string, error) {
	format, ok := Lookup(name)
	if !ok {
		return "", fmt.Errorf("unsupported document format")
	}
	if len(data) == 0 {
		return "", nil
	}
	switch format.Ext {
	case "docx", "xlsx", "pptx", "odt", "ods", "odp":
		return extractZipText(format.Ext, data)
	case "txt", "csv":
		return capText(decodeTextBytes(data)), nil
	case "rtf":
		return capText(rtfPlainText(data)), nil
	case "pdf":
		return "", nil
	case "doc", "xls", "ppt":
		return capText(legacyText(data)), nil
	default:
		return "", fmt.Errorf("unsupported document format")
	}
}

func capText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= MaxExtractedText {
		return text
	}
	// Cut on a rune boundary so callers never receive broken UTF-8.
	cut := MaxExtractedText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut])
}

func extractZipText(ext string, data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("invalid Office archive")
	}
	wanted := func(name string) bool {
		name = strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
		switch ext {
		case "docx":
			return name == "word/document.xml" || strings.HasPrefix(name, "word/header") ||
				strings.HasPrefix(name, "word/footer") || name == "word/footnotes.xml" ||
				name == "word/endnotes.xml" || name == "word/comments.xml"
		case "xlsx":
			return name == "xl/sharedstrings.xml" || strings.HasPrefix(name, "xl/worksheets/sheet")
		case "pptx":
			return strings.HasPrefix(name, "ppt/slides/slide") ||
				strings.HasPrefix(name, "ppt/notesslides/notesslide")
		case "odt", "ods", "odp":
			return name == "content.xml"
		default:
			return false
		}
	}
	budget := int64(maxTextBytes)
	var out strings.Builder
	found := false
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || !wanted(file.Name) {
			continue
		}
		if file.UncompressedSize64 > uint64(budget) {
			break
		}
		handle, err := file.Open()
		if err != nil {
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(handle, budget))
		handle.Close()
		if readErr != nil {
			continue
		}
		budget -= int64(len(raw))
		found = true
		out.WriteString(xmlPlainText(raw))
		out.WriteString("\n")
		if budget <= 0 || out.Len() >= MaxExtractedText*2 {
			break
		}
	}
	if !found {
		return "", fmt.Errorf("no readable document text")
	}
	return capText(out.String()), nil
}

// xmlPlainText walks Office XML and keeps character data, turning paragraph,
// line-break and tab elements into readable separators.
func xmlPlainText(raw []byte) string {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = false
	var out strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.CharData:
			text := strings.TrimSpace(string(value))
			if text == "" {
				continue
			}
			if out.Len() > 0 {
				out.WriteByte(' ')
			}
			out.WriteString(text)
		case xml.StartElement:
			if isTextSeparator(value.Name.Local) {
				out.WriteByte('\n')
			}
		case xml.EndElement:
			if isTextSeparator(value.Name.Local) {
				out.WriteByte('\n')
			}
		}
		if out.Len() >= MaxExtractedText*2 {
			break
		}
	}
	return out.String()
}

func isTextSeparator(local string) bool {
	switch strings.ToLower(local) {
	case "p", "br", "cr", "tab", "line-break", "s", "row", "tr", "si":
		return true
	default:
		return false
	}
}

func decodeTextBytes(data []byte) string {
	if len(data) >= 2 {
		if data[0] == 0xFF && data[1] == 0xFE {
			return decodeUTF16(data[2:], binary.LittleEndian)
		}
		if data[0] == 0xFE && data[1] == 0xFF {
			return decodeUTF16(data[2:], binary.BigEndian)
		}
	}
	return strings.ToValidUTF8(strings.ReplaceAll(string(data), "\r\n", "\n"), " ")
}

func decodeUTF16(data []byte, order binary.ByteOrder) string {
	values := make([]uint16, 0, len(data)/2)
	for len(data) >= 2 {
		values = append(values, order.Uint16(data[:2]))
		data = data[2:]
	}
	return string(utf16.Decode(values))
}

// rtfPlainText removes RTF control words and unescapes the common text escapes.
func rtfPlainText(data []byte) string {
	var out strings.Builder
	skipGroup := 0
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch ch {
		case '{':
			if skipGroup > 0 {
				skipGroup++
			}
		case '}':
			if skipGroup > 0 {
				skipGroup--
			}
		case '\\':
			if i+1 >= len(data) {
				continue
			}
			next := data[i+1]
			if next == '\\' || next == '{' || next == '}' {
				if skipGroup == 0 {
					out.WriteByte(next)
				}
				i++
				continue
			}
			if next == '*' {
				skipGroup++
				continue
			}
			if next == '\'' && i+3 < len(data) {
				if value, err := strconv.ParseUint(string(data[i+2:i+4]), 16, 8); err == nil {
					if skipGroup == 0 {
						out.WriteByte(byte(value))
					}
				}
				i += 3
				continue
			}
			j := i + 1
			for j < len(data) && ((data[j] >= 'a' && data[j] <= 'z') || (data[j] >= 'A' && data[j] <= 'Z')) {
				j++
			}
			word := strings.ToLower(string(data[i+1 : j]))
			for j < len(data) && data[j] == ' ' {
				j++
			}
			if skipGroup == 0 {
				switch word {
				case "par", "line", "sect", "page":
					out.WriteByte('\n')
				case "tab", "cell":
					out.WriteByte('\t')
				case "emdash":
					out.WriteString("—")
				case "endash":
					out.WriteString("–")
				case "bullet":
					out.WriteString("•")
				case "u":
					// \uN is a signed UTF-16 code unit.
					start := j
					for j < len(data) && data[j] >= '0' && data[j] <= '9' {
						j++
					}
					if start < j {
						if value, err := strconv.Atoi(string(data[start:j])); err == nil {
							out.WriteString(string(rune(value)))
						}
					}
				}
			}
			i = j - 1
		case '\r', '\n':
			if skipGroup == 0 {
				out.WriteByte('\n')
			}
		default:
			if skipGroup == 0 {
				out.WriteByte(ch)
			}
		}
	}
	return strings.ToValidUTF8(out.String(), " ")
}

// legacyText pulls readable ASCII and UTF-16LE runs out of OLE compound files.
// It is deliberately conservative: binary formatting noise is dropped and only
// runs of four or more printable characters are kept.
func legacyText(data []byte) string {
	var out strings.Builder
	var run strings.Builder
	flush := func() {
		if run.Len() >= 4 {
			out.WriteString(run.String())
			out.WriteByte('\n')
		}
		run.Reset()
	}
	for _, ch := range data {
		if ch == '\t' || ch == '\n' || ch == '\r' || (ch >= 32 && ch < 127) {
			run.WriteByte(ch)
		} else {
			flush()
		}
	}
	flush()
	// OLE stores some strings as UTF-16LE. Scan for stretches whose high byte
	// is zero and low byte is printable.
	for i := 0; i+1 < len(data); i += 2 {
		value := binary.LittleEndian.Uint16(data[i : i+2])
		if value == '\r' || value == '\n' || value == '\t' || (value >= 32 && value < 0x250 && unicode.IsPrint(rune(value))) {
			run.WriteRune(rune(value))
		} else {
			flush()
		}
	}
	flush()
	return out.String()
}
