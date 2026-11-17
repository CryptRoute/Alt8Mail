package httpapi

import (
	"errors"
	"net/url"
	"strings"
)

// scimBaseURL returns the public SCIM base URL advertised in resource meta and
// used by IdP configuration. SCIM carries identity data and bearer tokens, so a
// cleartext origin is accepted only for loopback development and tests.
func (s *Server) scimBaseURL() (string, error) {
	raw := strings.TrimSpace(s.cfg.PublicURL)
	if raw == "" {
		return "", errors.New("ALT8MAIL_PUBLIC_URL is required for SCIM provisioning")
	}
	publicURL, err := url.Parse(raw)
	if err != nil || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.Host == "" {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must be an absolute HTTP(S) URL for SCIM provisioning")
	}
	// The public URL is used as the token-authenticated provisioning origin.
	// Reject embedded credentials and parameters so they can never leak into
	// resource locations or IdP configuration. A literal "#" is rejected too:
	// url.Parse accepts a bare trailing fragment as an empty string while the
	// configured value would still contain it.
	if publicURL.User != nil || publicURL.RawQuery != "" || publicURL.ForceQuery || publicURL.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must be a bare origin without userinfo, query or fragment for SCIM provisioning")
	}
	if publicURL.Scheme == "http" && !samlLoopbackHost(publicURL.Hostname()) {
		return "", errors.New("ALT8MAIL_PUBLIC_URL must use HTTPS for SCIM provisioning (HTTP is allowed only for loopback)")
	}
	return strings.TrimRight(raw, "/") + "/scim/v2", nil
}
