package httpapi

import (
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

var composeColorPattern = regexp.MustCompile(`(?i)^(#[0-9a-f]{3,8}|rgba?\([0-9.,%\s]+\)|hsla?\([0-9.,%\s]+\)|transparent|[a-z]{3,20})$`)
var composeTextDecorationPattern = regexp.MustCompile(`(?i)^(none|underline|line-through|overline|underline line-through|line-through underline)$`)
var composeFontFamilyPattern = regexp.MustCompile(`(?i)^[a-z0-9 ,'"_-]{1,120}$`)
var composeFontSizePattern = regexp.MustCompile(`(?i)^(xx-small|x-small|small|medium|large|x-large|xx-large|xxx-large|[0-9]{1,2}(?:\.[0-9]{1,2})?(?:px|pt|em|rem|%))$`)
var composeHTMLPolicy = func() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	elements := []string{
		"p", "br", "div", "span", "strong", "b", "em", "i", "u", "s", "strike",
		"ul", "ol", "li", "blockquote", "h1", "h2", "h3", "h4", "pre", "code",
		"a", "hr", "table", "thead", "tbody", "tr", "td", "th", "img",
	}
	policy.AllowElements(elements...)
	policy.AllowAttrs("href", "title").OnElements("a")
	policy.AllowAttrs("src", "alt", "title", "width", "height", "data-email-src").OnElements("img")
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^(compose-quote|compose-quote-inner|compose-quote-lead|compose-quote-meta|email-quote)$`)).OnElements("div", "p", "blockquote", "span")
	policy.AllowAttrs("style").OnElements(elements...)
	policy.AllowStyles("text-align").MatchingEnum("left", "center", "right", "justify").OnElements(elements...)
	policy.AllowStyles("color", "background-color").Matching(composeColorPattern).OnElements(elements...)
	policy.AllowStyles("font-weight").MatchingEnum("normal", "bold", "600", "700", "800", "900").OnElements(elements...)
	policy.AllowStyles("font-style").MatchingEnum("normal", "italic").OnElements(elements...)
	policy.AllowStyles("font-family").Matching(composeFontFamilyPattern).OnElements(elements...)
	policy.AllowStyles("font-size").Matching(composeFontSizePattern).OnElements(elements...)
	policy.AllowStyles("text-decoration").Matching(composeTextDecorationPattern).OnElements(elements...)
	// Quoted HTML commonly carries spacing, borders and table layout inline.
	// Allow conservative CSS values without functions/URLs so the original
	// structure survives in the sent message as well as in the editor.
	composeLayoutValuePattern := regexp.MustCompile(`^[0-9A-Za-z#.,%+\-\s/]+$`)
	policy.AllowStyles(
		"line-height", "letter-spacing", "vertical-align", "text-indent", "font-variant",
		"white-space", "word-break", "overflow-wrap", "table-layout", "opacity",
		"background", "border-radius", "border-collapse", "border-spacing",
		"width", "max-width", "height", "max-height", "display",
	).Matching(composeLayoutValuePattern).OnElements(elements...)
	for _, property := range []string{
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"border", "border-top", "border-right", "border-bottom", "border-left",
		"border-color", "border-width", "border-style",
	} {
		policy.AllowStyles(property).Matching(composeLayoutValuePattern).OnElements(elements...)
	}
	policy.AllowURLSchemes("http", "https", "mailto")
	// Draft bodies may reference Alt8Mail's same-origin image proxy. Outbound
	// sends restore the original remote URL in the browser before submission.
	policy.AllowRelativeURLs(true)
	return policy
}()

// sanitizeComposeHTML is the only path by which editor HTML enters a draft or
// outbound message. It removes scripts, event handlers, iframes, forms,
// javascript: URLs and unsupported inline styles.
func sanitizeComposeHTML(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	clean := composeHTMLPolicy.Sanitize(raw)
	if strings.TrimSpace(clean) == "" {
		return ""
	}
	return clean
}

func htmlToPlainText(raw string) string {
	if raw == "" {
		return ""
	}
	text := regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-4]|/blockquote)\s*/?>`).ReplaceAllString(raw, "\n")
	text = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// normalizeComposeBody accepts either a plain-text or HTML editor payload and
// returns sanitized HTML plus a plain-text fallback. Plain messages still keep
// their text in bodyHtml so the reader and MIME builder have one representation.
func normalizeComposeBody(bodyHTML, bodyText string) (string, string) {
	bodyHTML = sanitizeComposeHTML(bodyHTML)
	bodyText = strings.TrimSpace(truncate(bodyText, 200_000))
	if bodyHTML == "" && bodyText != "" {
		bodyHTML = textToHTML(bodyText)
	}
	if bodyText == "" && bodyHTML != "" {
		bodyText = htmlToPlainText(bodyHTML)
	}
	if bodyHTML == "" && bodyText == "" {
		return "", ""
	}
	return bodyHTML, bodyText
}
