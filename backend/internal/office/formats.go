// Package office holds the OnlyOffice format model, JWT helpers and blank
// document templates used by the webmail Office editor.
package office

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Format describes one document extension supported by OnlyOffice.
type Format struct {
	Ext          string
	DocumentType string
	MIME         string
	Editable     bool
	NewDocument  bool
}

var formats = map[string]Format{
	"docx": {Ext: "docx", DocumentType: "word", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Editable: true, NewDocument: true},
	"doc":  {Ext: "doc", DocumentType: "word", MIME: "application/msword"},
	"odt":  {Ext: "odt", DocumentType: "word", MIME: "application/vnd.oasis.opendocument.text", Editable: true},
	"rtf":  {Ext: "rtf", DocumentType: "word", MIME: "application/rtf", Editable: true},
	"txt":  {Ext: "txt", DocumentType: "word", MIME: "text/plain; charset=utf-8", Editable: true},

	"xlsx": {Ext: "xlsx", DocumentType: "cell", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Editable: true, NewDocument: true},
	"xls":  {Ext: "xls", DocumentType: "cell", MIME: "application/vnd.ms-excel"},
	"ods":  {Ext: "ods", DocumentType: "cell", MIME: "application/vnd.oasis.opendocument.spreadsheet", Editable: true},
	"csv":  {Ext: "csv", DocumentType: "cell", MIME: "text/csv; charset=utf-8", Editable: true},

	"pptx": {Ext: "pptx", DocumentType: "slide", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Editable: true, NewDocument: true},
	"ppt":  {Ext: "ppt", DocumentType: "slide", MIME: "application/vnd.ms-powerpoint"},
	"odp":  {Ext: "odp", DocumentType: "slide", MIME: "application/vnd.oasis.opendocument.presentation", Editable: true},

	"pdf": {Ext: "pdf", DocumentType: "pdf", MIME: "application/pdf"},
}

// Lookup returns the format for a file name or extension.
func Lookup(name string) (Format, bool) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(name))), ".")
	if ext == "" {
		return Format{}, false
	}
	format, ok := formats[ext]
	return format, ok
}

// LookupExt returns the format for an extension without the leading dot.
func LookupExt(ext string) (Format, bool) {
	format, ok := formats[strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ext)), ".")]
	return format, ok
}

// NewFormats lists the extensions offered by the "new document" dialog.
func NewFormats() []string {
	out := []string{}
	for ext, format := range formats {
		if format.NewDocument {
			out = append(out, ext)
		}
	}
	sort.Strings(out)
	return out
}

// SupportedPayload groups extensions for the browser-safe feature endpoint.
func SupportedPayload() map[string][]string {
	out := map[string][]string{"word": {}, "cell": {}, "slide": {}, "pdf": {}}
	for ext, format := range formats {
		out[format.DocumentType] = append(out[format.DocumentType], ext)
	}
	for key := range out {
		sort.Strings(out[key])
	}
	return out
}

// NameWithExtension makes a user-supplied document name end in the requested
// extension, replacing a different known Office suffix when necessary.
func NameWithExtension(name, ext string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Untitled"
	}
	current := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if current == ext {
		return name
	}
	base := name
	if current != "" {
		if _, known := formats[current]; known {
			base = strings.TrimSuffix(name, filepath.Ext(name))
		}
	}
	return strings.TrimSpace(base) + "." + ext
}

// NewDocumentKey creates the OnlyOffice document key for a new edit session.
// Keys are at most 128 characters and only contain characters accepted by the
// Document Server.
func NewDocumentKey(documentID string, version int) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-v%d", documentID, version)
	}
	return fmt.Sprintf("%s-v%d-%s", documentID, version, base64.RawURLEncoding.EncodeToString(buf))
}
