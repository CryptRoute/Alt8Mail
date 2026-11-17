package httpapi

import "time"

// Shared SSO callback rate limits used by the OIDC, SAML and SCIM entry points.
const (
	oidcCallbackRateLimit  = 20
	oidcCallbackRateWindow = 10 * time.Minute
)
