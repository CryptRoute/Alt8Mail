package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	ooxmlCorePath         = "docProps/core.xml"
	ooxmlCustomPath       = "docProps/custom.xml"
	ooxmlRootRelsPath     = "_rels/.rels"
	ooxmlContentTypesPath = "[Content_Types].xml"
	ooxmlCustomRelation   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/custom-properties"
	ooxmlCustomType       = "application/vnd.openxmlformats-officedocument.custom-properties+xml"
)

// SetDocumentProperties writes the author, title and the Alt8Mail platform
// marker into the OOXML package. It works for docx, xlsx and pptx because all
// three use the same package-level core/custom property parts.
func SetDocumentProperties(data []byte, author, title string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open Office package: %w", err)
	}
	author = strings.TrimSpace(author)
	title = strings.TrimSpace(title)
	if author == "" {
		author = "Alt8Mail"
	}
	if title == "" {
		title = "Untitled document"
	}
	replacements := map[string][]byte{
		ooxmlCorePath:   ooxmlCoreProperties(author, title),
		ooxmlCustomPath: ooxmlCustomProperties(),
	}
	for _, path := range []string{ooxmlRootRelsPath, ooxmlContentTypesPath} {
		var source []byte
		for _, file := range reader.File {
			if file.Name != path {
				continue
			}
			handle, openErr := file.Open()
			if openErr != nil {
				return nil, fmt.Errorf("read %s: %w", path, openErr)
			}
			source, openErr = io.ReadAll(handle)
			handle.Close()
			if openErr != nil {
				return nil, fmt.Errorf("read %s: %w", path, openErr)
			}
			break
		}
		if len(source) == 0 {
			return nil, fmt.Errorf("Office package is missing %s", path)
		}
		switch path {
		case ooxmlRootRelsPath:
			replacements[path] = addOOXMLCustomRelationship(source)
		case ooxmlContentTypesPath:
			replacements[path] = addOOXMLCustomContentType(source)
		}
	}

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range reader.File {
		payload, replaced := replacements[file.Name]
		if replaced {
			delete(replacements, file.Name)
		} else {
			handle, openErr := file.Open()
			if openErr != nil {
				writer.Close()
				return nil, fmt.Errorf("read %s: %w", file.Name, openErr)
			}
			payload, openErr = io.ReadAll(handle)
			handle.Close()
			if openErr != nil {
				writer.Close()
				return nil, fmt.Errorf("read %s: %w", file.Name, openErr)
			}
		}
		header := &zip.FileHeader{Name: file.Name, Method: file.Method, Modified: file.Modified}
		header.SetMode(file.Mode())
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			writer.Close()
			return nil, fmt.Errorf("write %s: %w", file.Name, createErr)
		}
		if _, writeErr := entry.Write(payload); writeErr != nil {
			writer.Close()
			return nil, fmt.Errorf("write %s: %w", file.Name, writeErr)
		}
	}
	missing := make([]string, 0, len(replacements))
	for path := range replacements {
		missing = append(missing, path)
	}
	sort.Strings(missing)
	for _, path := range missing {
		header := &zip.FileHeader{Name: path, Method: zip.Deflate, Modified: time.Now()}
		header.SetMode(0o644)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			writer.Close()
			return nil, fmt.Errorf("write %s: %w", path, createErr)
		}
		if _, writeErr := entry.Write(replacements[path]); writeErr != nil {
			writer.Close()
			return nil, fmt.Errorf("write %s: %w", path, writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close Office package: %w", err)
	}
	return output.Bytes(), nil
}

func ooxmlCoreProperties(author, title string) []byte {
	stamp := time.Now().UTC().Format(time.RFC3339)
	var body strings.Builder
	body.WriteString(xml.Header)
	body.WriteString(`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	body.WriteString(`<dc:title>` + ooxmlEscape(title) + `</dc:title>`)
	body.WriteString(`<dc:creator>` + ooxmlEscape(author) + `</dc:creator>`)
	body.WriteString(`<cp:lastModifiedBy>` + ooxmlEscape(author) + `</cp:lastModifiedBy>`)
	body.WriteString(`<cp:revision>1</cp:revision>`)
	body.WriteString(`<dcterms:created xsi:type="dcterms:W3CDTF">` + stamp + `</dcterms:created>`)
	body.WriteString(`<dcterms:modified xsi:type="dcterms:W3CDTF">` + stamp + `</dcterms:modified>`)
	body.WriteString(`</cp:coreProperties>`)
	return []byte(body.String())
}

func ooxmlCustomProperties() []byte {
	return []byte(xml.Header + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="Platform"><vt:lpwstr>Alt8Mail Workspace</vt:lpwstr></property></Properties>`)
}

func ooxmlEscape(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func addOOXMLCustomRelationship(source []byte) []byte {
	if bytes.Contains(source, []byte(ooxmlCustomRelation)) {
		return source
	}
	addition := []byte(`<Relationship Id="rIdCustomProps" Type="` + ooxmlCustomRelation + `" Target="docProps/custom.xml"/>`)
	return insertBeforeClosing(source, "</Relationships>", addition)
}

func addOOXMLCustomContentType(source []byte) []byte {
	if bytes.Contains(source, []byte(`PartName="/docProps/custom.xml"`)) {
		return source
	}
	addition := []byte(`<Override PartName="/docProps/custom.xml" ContentType="` + ooxmlCustomType + `"/>`)
	return insertBeforeClosing(source, "</Types>", addition)
}

func insertBeforeClosing(source []byte, closing string, addition []byte) []byte {
	index := bytes.LastIndex(source, []byte(closing))
	if index < 0 {
		return source
	}
	output := make([]byte, 0, len(source)+len(addition))
	output = append(output, source[:index]...)
	output = append(output, addition...)
	output = append(output, source[index:]...)
	return output
}
