package httpapi

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
)

// accountNotice describes a mailbox welcome/ready email. The message is built
// by the same MIME writer used for normal mail so it is signed and delivered
// through the tenant's submission service rather than a third-party relay.
type accountNotice struct {
	TenantID   string
	FromEmail  string
	FromName   string
	To         string
	ToName     string
	Subject    string
	Eyebrow    string
	Heading    string
	Intro      string
	Mailbox    string
	Password   string
	Kind       string // "created" or "migrated"
	Steps      []string
	FooterNote string
}

func (s *Server) sendAccountNotice(ctx context.Context, notice accountNotice) error {
	if strings.TrimSpace(notice.To) == "" || !s.mailSyncConfigured() {
		return fmt.Errorf("mail delivery is not configured")
	}
	fromEmail := strings.ToLower(strings.TrimSpace(notice.FromEmail))
	if fromEmail == "" {
		return fmt.Errorf("no system sender is available")
	}
	fromName := strings.TrimSpace(notice.FromName)
	if fromName == "" {
		fromName = "Alt8Mail Accounts"
	}
	tenantName, productName, primaryColor, supportEmail := s.noticeBranding(ctx, notice.TenantID)
	loginURL := strings.TrimRight(s.cfg.PublicURL, "/") + "/"
	textBody, htmlBody := renderAccountNotice(notice, tenantName, productName, primaryColor, loginURL, supportEmail)
	messageID := fmt.Sprintf("<%s@%s>", newID("notice"), s.cfg.MailDomain)
	item := map[string]any{
		"from":        map[string]any{"name": fromName, "email": fromEmail},
		"to":          []any{map[string]any{"name": notice.ToName, "email": notice.To}},
		"subject":     notice.Subject,
		"bodyText":    textBody,
		"bodyHtml":    htmlBody,
		"trustedHtml": true,
	}
	raw, err := buildMIMEMessage(item, messageID, nil)
	if err != nil {
		return err
	}
	return s.submitSMTPMessage(fromEmail, []string{notice.To}, raw)
}

func (s *Server) noticeBranding(ctx context.Context, tenantID string) (tenantName, productName, primaryColor, supportEmail string) {
	productName = "Alt8Mail"
	primaryColor = "#0f6cbd"
	var settings []byte
	if err := s.store.DB.QueryRow(ctx, `SELECT name, settings FROM tenants WHERE id = $1`, tenantID).Scan(&tenantName, &settings); err != nil {
		return tenantName, productName, primaryColor, supportEmail
	}
	policy := tenantPolicyFromSettings(settings)
	if value := strings.TrimSpace(policy.Branding.ProductName); value != "" {
		productName = value
	}
	if value := strings.TrimSpace(policy.Branding.PrimaryColor); value != "" {
		primaryColor = value
	}
	supportEmail = strings.TrimSpace(policy.Branding.SupportEmail)
	return tenantName, productName, primaryColor, supportEmail
}

func renderAccountNotice(notice accountNotice, tenantName, productName, primaryColor, loginURL, supportEmail string) (string, string) {
	mailbox := notice.Mailbox
	if mailbox == "" {
		mailbox = notice.To
	}
	greeting := strings.TrimSpace(notice.ToName)
	if greeting == "" {
		greeting = mailbox
	} else {
		greeting = greeting + " (" + mailbox + ")"
	}
	var steps string
	if len(notice.Steps) > 0 {
		steps = `<ol style="margin:18px 0 0;padding:0 0 0 20px;color:#3f4a5a;font-size:14px;line-height:1.7">`
		for _, step := range notice.Steps {
			steps += `<li style="margin:0 0 6px">` + html.EscapeString(step) + `</li>`
		}
		steps += `</ol>`
	}
	passwordRow := ""
	if strings.TrimSpace(notice.Password) != "" {
		passwordRow = `<tr><td style="padding:7px 0;color:#667085;font-size:13px">Temporary password</td><td style="padding:7px 0;text-align:right;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:14px;font-weight:700;color:#101828">` + html.EscapeString(notice.Password) + `</td></tr>`
	}
	supportRow := ""
	if supportEmail != "" {
		supportRow = `<p style="margin:18px 0 0;color:#667085;font-size:12px;line-height:1.6">Need help? Contact <a href="mailto:` + html.EscapeString(supportEmail) + `" style="color:` + html.EscapeString(primaryColor) + `;text-decoration:none;font-weight:600">` + html.EscapeString(supportEmail) + `</a>.</p>`
	}
	eyebrow := notice.Eyebrow
	if eyebrow == "" {
		eyebrow = productName
	}
	htmlBody := `<!doctype html><html><body style="margin:0;background:#eef2f7;padding:32px 16px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif">` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:620px;margin:0 auto;background:#ffffff;border-radius:16px;overflow:hidden;border:1px solid #e4e9f1">` +
		`<tr><td style="background:` + html.EscapeString(primaryColor) + `;padding:20px 26px;color:#ffffff"><div style="font-size:11px;letter-spacing:.14em;text-transform:uppercase;opacity:.78">` + html.EscapeString(eyebrow) + `</div><div style="margin-top:4px;font-size:17px;font-weight:700">` + html.EscapeString(productName) + `</div></td></tr>` +
		`<tr><td style="padding:30px 30px 26px"><div style="font-size:12px;color:#98a2b3;font-weight:600">` + html.EscapeString(tenantName) + `</div>` +
		`<h1 style="margin:8px 0 0;font-size:24px;line-height:1.25;color:#101828">` + html.EscapeString(notice.Heading) + `</h1>` +
		`<p style="margin:12px 0 0;font-size:14px;line-height:1.7;color:#475467">` + html.EscapeString(notice.Intro) + `</p>` +
		`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="margin:22px 0 0;background:#f7f9fc;border:1px solid #e4e9f1;border-radius:12px"><tr><td style="padding:14px 16px"><table role="presentation" width="100%" cellspacing="0" cellpadding="0">` +
		`<tr><td style="padding:7px 0;color:#667085;font-size:13px">Email address</td><td style="padding:7px 0;text-align:right;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:14px;font-weight:700;color:#101828">` + html.EscapeString(mailbox) + `</td></tr>` +
		passwordRow + `</table></td></tr></table>` +
		`<div style="margin:24px 0 0"><a href="` + html.EscapeString(loginURL) + `" style="display:inline-block;background:` + html.EscapeString(primaryColor) + `;color:#ffffff;text-decoration:none;font-size:14px;font-weight:700;padding:12px 22px;border-radius:9px">Sign in to your mailbox</a></div>` +
		`<p style="margin:10px 0 0;color:#98a2b3;font-size:11px;word-break:break-all">` + html.EscapeString(loginURL) + `</p>` +
		steps + supportRow +
		`<p style="margin:22px 0 0;padding-top:16px;border-top:1px solid #eef1f6;color:#98a2b3;font-size:11px;line-height:1.6">` + html.EscapeString(notice.FooterNote) + `</p>` +
		`</td></tr></table>` +
		`<p style="max-width:620px;margin:14px auto 0;text-align:center;color:#98a2b3;font-size:10.5px">Sent by ` + html.EscapeString(productName) + ` for ` + html.EscapeString(tenantName) + `. If you were not expecting this message, contact ` + html.EscapeString(firstNonEmpty(supportEmail, "your administrator")) + `.</p>` +
		`</body></html>`
	textBody := fmt.Sprintf("%s\n\n%s\n\n%s\n\nEmail address: %s\n", notice.Heading, notice.Intro, greeting, mailbox)
	if notice.Password != "" {
		textBody += "Temporary password: " + notice.Password + "\n"
	}
	textBody += "\nSign in: " + loginURL + "\n"
	if len(notice.Steps) > 0 {
		textBody += "\nNext steps:\n"
		for index, step := range notice.Steps {
			textBody += fmt.Sprintf("%d. %s\n", index+1, step)
		}
	}
	if supportEmail != "" {
		textBody += "\nSupport: " + supportEmail + "\n"
	}
	textBody += "\n" + notice.FooterNote + "\n"
	return textBody, htmlBody
}

// accountNoticeSender picks the envelope sender for system notice mail. The
// acting administrator's mailbox is always deliverable through the master
// user; the organization's support address becomes Reply-To-style guidance in
// the body instead of a spoofed From line.
func accountNoticeSender(fromEmail, fromName, productName string) (string, string) {
	fromEmail = strings.ToLower(strings.TrimSpace(fromEmail))
	if fromEmail == "" {
		return "", ""
	}
	name := strings.TrimSpace(fromName)
	if name == "" {
		name = productName + " Accounts"
	} else {
		name = productName + " · " + name
	}
	return fromEmail, name
}

func accountNoticeExpiryNote() string {
	return "This temporary password expires when you change it. " + time.Now().UTC().Format("2 Jan 2006")
}

// noticeSenderFor resolves a deliverable From mailbox for administrative
// notices: the operator who started the action, otherwise a tenant owner.
func (s *Server) noticeSenderFor(ctx context.Context, tenantID, preferredUserID string) (fromEmail, fromName, productName string, err error) {
	_, productName, _, _ = s.noticeBranding(ctx, tenantID)
	query := func(userID string) error {
		return s.store.DB.QueryRow(ctx, `
			SELECT email::text, name FROM users
			WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
		`, userID, tenantID).Scan(&fromEmail, &fromName)
	}
	if strings.TrimSpace(preferredUserID) != "" {
		if query(preferredUserID) == nil && fromEmail != "" {
			return fromEmail, accountNoticeName(fromName, productName), productName, nil
		}
	}
	if err := s.store.DB.QueryRow(ctx, `
		SELECT email::text, name FROM users
		WHERE tenant_id = $1 AND deleted_at IS NULL AND status = 'active'
		ORDER BY (role IN ('owner','admin')) DESC, created_at ASC LIMIT 1
	`, tenantID).Scan(&fromEmail, &fromName); err != nil {
		return "", "", productName, fmt.Errorf("no active sender mailbox is available")
	}
	return fromEmail, accountNoticeName(fromName, productName), productName, nil
}

func accountNoticeName(name, productName string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return productName + " Accounts"
	}
	return productName + " · " + name
}
