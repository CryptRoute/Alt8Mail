package office

import (
	"embed"
	"fmt"
)

//go:embed templates/blank.docx templates/blank.xlsx templates/blank.pptx
var templateFS embed.FS

// Template returns a blank OOXML document for a new Office file.
func Template(ext string) ([]byte, error) {
	format, ok := LookupExt(ext)
	if !ok || !format.NewDocument {
		return nil, fmt.Errorf("no blank template for %q", ext)
	}
	data, err := templateFS.ReadFile("templates/blank." + format.Ext)
	if err != nil {
		return nil, fmt.Errorf("blank template is unavailable: %w", err)
	}
	return data, nil
}
