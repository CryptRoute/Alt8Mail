// Package threatintel extracts confirmed threat indicators from user reports
// and publishes them to the Redis-backed maps consumed by Rspamd.
package threatintel

import (
	stdhtml "html"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/idna"
)

// MaxURLHosts bounds how many hosts one message can contribute. Reviewers see
// this same list, so a noisy marketing email cannot flood the review dialog or
// the Redis map.
const MaxURLHosts = 100

// URLHost is one normalized hostname extracted from a reported message.
type URLHost struct {
	Host   string `json:"host"`
	Shared bool   `json:"shared"`
}

var textURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"'` + "`" + `]+`)

// ExtractURLHosts returns sorted, deduplicated hostnames from an HTML body and
// a plain-text body. Only http and https URLs are considered. Full URLs and
// paths are deliberately discarded.
func ExtractURLHosts(htmlBody, textBody string) []URLHost {
	found := map[string]struct{}{}
	collect := func(raw string) {
		collectURLHosts(raw, found)
	}
	collect(textBody)
	collectHTML(htmlBody, collect)

	hosts := make([]string, 0, len(found))
	for host := range found {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	if len(hosts) > MaxURLHosts {
		hosts = hosts[:MaxURLHosts]
	}
	out := make([]URLHost, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, URLHost{Host: host, Shared: IsSharedDestination(host)})
	}
	return out
}

func collectHTML(raw string, collect func(string)) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	sawText := false
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			if !sawText {
				// Malformed HTML still often contains usable URLs in plain text.
				collect(raw)
			}
			return
		case xhtml.TextToken:
			text := string(tokenizer.Text())
			if strings.TrimSpace(text) != "" {
				sawText = true
				collect(text)
			}
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			for _, attribute := range token.Attr {
				if strings.TrimSpace(attribute.Val) != "" {
					collect(attribute.Val)
				}
			}
		}
	}
}

func collectURLHosts(raw string, found map[string]struct{}) {
	raw = stdhtml.UnescapeString(strings.TrimSpace(raw))
	if raw == "" {
		return
	}
	for _, candidate := range textURLPattern.FindAllString(raw, -1) {
		candidate = strings.TrimRight(candidate, ".,;:!?)]}>\"'")
		if host, ok := normalizeHostname(candidate); ok {
			found[host] = struct{}{}
		}
	}
}

func normalizeHostname(raw string) (string, bool) {
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || len(host) > 253 {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		// URL maps match hostnames, not IP literals. Avoid publishing a form
		// that Rspamd and the browser may spell differently.
		return "", false
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", false
	}
	ascii = strings.TrimSuffix(strings.ToLower(ascii), ".")
	if ascii == "" || strings.ContainsAny(ascii, " \t\r\n/\\@[]") {
		return "", false
	}
	if strings.EqualFold(ascii, "localhost") || strings.HasSuffix(ascii, ".localhost") ||
		strings.HasSuffix(ascii, ".local") || strings.HasSuffix(ascii, ".internal") ||
		strings.HasSuffix(ascii, ".invalid") || strings.HasSuffix(ascii, ".test") {
		return "", false
	}
	labels := strings.Split(ascii, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", false
		}
	}
	return ascii, true
}
