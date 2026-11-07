package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed office_editor.html
var officeEditorHTML []byte

//go:embed office_editor.js
var officeEditorJS []byte

// officeEditorHost is an isolated, same-origin page. Keeping the OnlyOffice
// browser API off the webmail shell lets nginx apply a narrow CSP to this one
// page instead of allowing a third-party script origin in the mail app.
const officeEditorCSP = "default-src 'self'; img-src 'self' data: blob: https:; style-src 'self' 'unsafe-inline' https:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob: https:; connect-src 'self' https: wss:; font-src 'self' data: https:; worker-src 'self' blob:; frame-src 'self' blob: https:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'"

func (s *Server) officeEditorHost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", officeEditorCSP)
	_, _ = w.Write(officeEditorHTML)
}

func (s *Server) officeEditorScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(officeEditorJS)
}
