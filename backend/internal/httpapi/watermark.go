package httpapi

import (
	"html"
	"strings"
)

// embeddedWatermarkHTML puts a visible classification watermark inside the
// message body as well as around it. Email clients that strip positioned CSS
// still render the bordered banners and the repeated inline mark.
func embeddedWatermarkHTML(bodyHTML, stamp, classification string) string {
	mark := html.EscapeString(strings.ToUpper(classification) + " · " + stamp)
	banner := `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:0 0 14px"><tr><td style="border:2px solid #b42318;border-radius:8px;padding:9px 11px;color:#b42318;font:700 11px/1.4 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.08em">` + mark + `</td></tr></table>`
	ghost := `<div style="margin:12px 0;padding:14px;border:1px dashed #b4231855;border-radius:8px;color:#b42318;font:700 20px/1.25 -apple-system,Segoe UI,Roboto,Arial,sans-serif;text-align:center;opacity:.55;transform:rotate(-2deg)">` + mark + `</div>`
	return banner + ghost + bodyHTML + ghost + banner
}

func embeddedFileWatermark(data []byte, mimeType, watermark string) []byte {
	if watermark == "" {
		return data
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "text/plain":
		return []byte("[" + watermark + "]\n\n" + string(data) + "\n\n[" + watermark + "]")
	case "text/html", "application/xhtml+xml":
		banner := `<div style="position:sticky;top:0;z-index:2147483647;padding:10px 12px;border:2px solid #b42318;background:#fff5f5;color:#b42318;font:700 11px/1.4 system-ui,sans-serif;letter-spacing:.06em">` + html.EscapeString(watermark) + `</div>`
		return []byte(banner + string(data) + banner)
	}
	return data
}
