package httpapi

import (
	"html/template"
	"net/http"
	"time"
)

var uploadPageTemplate = template.Must(template.New("upload").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="robots" content="noindex,nofollow"><title>{{.Title}}</title>
<style>
:root{color-scheme:light dark;--brand:{{.BrandColor}};--ink:#132033;--muted:#667085;--line:#e2e8f0;--surface:#fff;--bg:#f6f8fc;--soft:#eef4ff}
@media(prefers-color-scheme:dark){:root{--ink:#eef3f9;--muted:#98a5b8;--line:#29364b;--surface:#101827;--bg:#070b12;--soft:#122036}}
*{box-sizing:border-box}html,body{margin:0;min-height:100%}body{background:radial-gradient(circle at 50% 0,#dbeafe66,transparent 42%),var(--bg);color:var(--ink);font:15px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;padding:22px 14px 38px;display:flex;justify-content:center}
.wrap{width:min(560px,100%)}header{display:flex;align-items:center;gap:11px;margin:4px 0 18px}header .mark{width:42px;height:42px;border-radius:13px;background:var(--brand);color:#fff;display:grid;place-items:center;font-weight:800;box-shadow:0 10px 24px #0f172a2e}header b{display:block;font-size:14px}header small{display:block;color:var(--muted);font-size:12px}
.card{background:var(--surface);border:1px solid var(--line);border-radius:20px;padding:22px;box-shadow:0 24px 60px #0f172a14}.eyebrow{color:var(--brand);font-size:11px;font-weight:800;letter-spacing:.1em;text-transform:uppercase}h1{font-size:clamp(21px,6vw,27px);line-height:1.15;margin:6px 0 6px;letter-spacing:-.03em}.lede{color:var(--muted);font-size:13px;margin:0 0 18px}
.drop{border:1.5px dashed color-mix(in srgb,var(--brand) 40%,var(--line));background:var(--soft);border-radius:15px;padding:24px 16px;text-align:center;cursor:pointer;transition:.18s}.drop:active,.drop.drag{transform:scale(.995);border-color:var(--brand)}.drop strong{display:block;font-size:15px;margin-bottom:3px}.drop span{color:var(--muted);font-size:12px}.drop input{display:none}
.list{display:grid;gap:9px;margin-top:16px}.file{border:1px solid var(--line);border-radius:12px;padding:11px 12px;background:var(--surface)}.file-head{display:flex;align-items:center;gap:8px}.file-head b{flex:1;min-width:0;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.file-head small{color:var(--muted);font-size:11px;white-space:nowrap}.bar{height:5px;background:var(--line);border-radius:99px;overflow:hidden;margin-top:9px}.bar span{display:block;height:100%;width:0;background:var(--brand);border-radius:99px;transition:width .2s}.status{display:flex;justify-content:space-between;margin-top:5px;color:var(--muted);font-size:11px}.status strong{color:var(--brand);font-weight:700}
.foot{margin-top:17px;padding-top:13px;border-top:1px solid var(--line);color:var(--muted);font-size:11px;line-height:1.6}.pill{display:inline-flex;align-items:center;gap:5px;padding:3px 7px;border-radius:99px;background:var(--soft);color:var(--brand);font-weight:700;font-size:10px;margin-bottom:8px}
.expired{padding:28px 4px;text-align:center}.expired h1{margin-bottom:8px}.expired p{color:var(--muted);font-size:13px}
</style></head><body data-waiting="{{.Waiting}}" data-uploading="{{.Uploading}}" data-uploaded="{{.Uploaded}}" data-failed="{{.Failed}}" data-network="{{.Network}}"><main class="wrap"><header><span class="mark">A8</span><div><b>{{.TenantName}}</b><small>{{.Secure}}</small></div></header>
{{if .Expired}}<section class="card expired"><span class="pill">{{.SessionClosed}}</span><h1>{{.ExpiredTitle}}</h1><p>{{.ExpiredBody}}</p></section>{{else}}
<section class="card"><span class="pill">{{.AuthLabel}}</span><h1>{{.SendFiles}}</h1><p class="lede">{{.Lead}}</p>
<label class="drop" id="drop"><strong>{{.Tap}}</strong><span>{{.Drag}}</span><input id="files" type="file" multiple></label>
<div class="list" id="list"></div>
<div class="foot">{{.Foot}}</div></section>{{end}}</main>
{{if not .Expired}}<script src="/upload-mobile.js"></script>{{end}}</body></html>`))

func (s *Server) mobileUploadPage(w http.ResponseWriter, r *http.Request) {
	row, owner, tenantName, brandColor, err := s.loadUploadSessionByToken(r.Context(), r.PathValue("token"))
	expired := err != nil || row.Revoked != nil || !row.ExpiresAt.After(time.Now())
	brandColor = safeBrandColor(brandColor)
	translator := s.requestTranslator(r)
	if tenantName == "" {
		tenantName = "Alt8Mail"
	}
	if owner.Name == "" {
		owner.Name = translator.T("server.upload.ownerFallback", nil)
	}
	expiryLabel := ""
	if !expired {
		expiryLabel = translator.T("server.upload.expires", map[string]any{"time": row.ExpiresAt.Local().Format("2 Jan, 15:04")})
	}
	data := map[string]any{
		"Lang": translator.Locale(), "Dir": translator.Dir(),
		"TenantName": tenantName, "OwnerName": owner.Name, "BrandColor": template.CSS(safeBrandColor(brandColor)),
		"Expired":       expired,
		"Title":         translator.T("server.upload.title", map[string]any{"tenant": tenantName}),
		"Secure":        translator.T("server.upload.secure", nil),
		"SessionClosed": translator.T("server.upload.sessionClosed", nil),
		"ExpiredTitle":  translator.T("server.upload.expiredTitle", nil),
		"ExpiredBody":   translator.T("server.upload.expiredBody", nil),
		"AuthLabel":     translator.T("server.upload.authenticated", map[string]any{"expiry": expiryLabel}),
		"SendFiles":     translator.T("server.upload.sendFiles", map[string]any{"owner": owner.Name}),
		"Lead":          translator.T("server.upload.lead", nil),
		"Tap":           translator.T("server.upload.tap", nil),
		"Drag":          translator.T("server.upload.drag", map[string]any{"max": formatBytes(maxAttachmentBytes)}),
		"Foot":          translator.T("server.upload.foot", nil),
		"Uploaded":      translator.T("server.upload.uploaded", nil),
		"Failed":        translator.T("server.upload.failed", nil),
		"Network":       translator.T("server.upload.network", nil),
		"Waiting":       translator.T("server.upload.waiting", nil),
		"Uploading":     translator.T("server.upload.uploading", nil),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if expired {
		w.WriteHeader(http.StatusGone)
	}
	if err := uploadPageTemplate.Execute(w, data); err != nil {
		s.logger.Warn("mobile upload page render failed", "err", err)
	}
}
