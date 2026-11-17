package office

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSetDocumentPropertiesWritesCoreAndCustomParts(t *testing.T) {
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		t.Run(ext, func(t *testing.T) {
			source, err := Template(ext)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := SetDocumentProperties(source, `Avery Chen & Co`, `Quarterly Plan`)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := zip.NewReader(bytes.NewReader(updated), int64(len(updated)))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{}
			for _, file := range reader.File {
				handle, openErr := file.Open()
				if openErr != nil {
					t.Fatal(openErr)
				}
				payload, readErr := io.ReadAll(handle)
				handle.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				files[file.Name] = string(payload)
			}
			core := files["docProps/core.xml"]
			if !strings.Contains(core, `Avery Chen &amp; Co`) || !strings.Contains(core, `Quarterly Plan`) {
				t.Fatalf("core properties are incomplete:\n%s", core)
			}
			custom := files["docProps/custom.xml"]
			if !strings.Contains(custom, `name="Platform"`) || !strings.Contains(custom, `Alt8Mail Workspace`) {
				t.Fatalf("custom properties are incomplete:\n%s", custom)
			}
			if !strings.Contains(files["_rels/.rels"], "custom-properties") {
				t.Fatalf("root relationships are missing the custom property part:\n%s", files["_rels/.rels"])
			}
			if !strings.Contains(files["[Content_Types].xml"], "/docProps/custom.xml") {
				t.Fatalf("content types are missing the custom property part:\n%s", files["[Content_Types].xml"])
			}
			if _, ok := files["word/document.xml"]; ext == "docx" && !ok {
				t.Fatal("document.xml was lost while rewriting the package")
			}
		})
	}
}
