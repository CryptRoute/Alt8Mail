// Package licensing is the public, non-internal surface used by the private
// licensing portal and offline issuance tooling. Customer servers use the
// internal verifier through the product binaries; the portal uses Sign.
package licensing

import (
	"crypto/ed25519"

	internal "github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
)

type (
	License  = internal.License
	Limits   = internal.Limits
	Status   = internal.Status
	Verifier = internal.Verifier
	Feature  = internal.Feature
)

var (
	ErrMalformed   = internal.ErrMalformed
	ErrSignature   = internal.ErrSignature
	ErrClaim       = internal.ErrClaim
	ErrPublicKey   = internal.ErrPublicKey
	ErrPrivateKey  = internal.ErrPrivateKey
	ErrUnsupported = internal.ErrUnsupported
	ErrTrustAnchor = internal.ErrTrustAnchor
	ErrKeyID       = internal.ErrKeyID
)

const (
	EnvelopePrefix = internal.EnvelopePrefix
	Version        = internal.Version
	MinVersion     = internal.MinVersion
)

// Canonical paid feature keys, re-exported so the licensing portal and private
// enterprise modules cannot drift from the product catalog.
const (
	FeatureRecipientScheduling = internal.FeatureRecipientScheduling
	FeatureFollowUps           = internal.FeatureFollowUps
	FeatureTemplateGallery     = internal.FeatureTemplateGallery
	FeatureDeliverability      = internal.FeatureDeliverability
	FeatureCampaignTracking    = internal.FeatureCampaignTracking
	FeatureAITriage            = internal.FeatureAITriage
	FeatureAIObligations       = internal.FeatureAIObligations
	FeatureAISearch            = internal.FeatureAISearch
	FeatureAIMeetingPrep       = internal.FeatureAIMeetingPrep
	FeatureOfficeAI            = internal.FeatureOfficeAI
	FeatureAIPolicyAssist      = internal.FeatureAIPolicyAssist
	FeatureMCPGovernance       = internal.FeatureMCPGovernance
	FeatureAIBudget            = internal.FeatureAIBudget
	FeatureDLPAdvanced         = internal.FeatureDLPAdvanced
	FeatureEDiscovery          = internal.FeatureEDiscovery
	FeatureBYOK                = internal.FeatureBYOK
	FeatureFileGovernance      = internal.FeatureFileGovernance
	FeatureAuditExport         = internal.FeatureAuditExport
	FeatureCRMSync             = internal.FeatureCRMSync
	FeatureMigration           = internal.FeatureMigration
	FeatureAnalytics           = internal.FeatureAnalytics
	FeatureSecurityAwareness   = internal.FeatureSecurityAwareness
	FeatureSupportOps          = internal.FeatureSupportOps
	FeatureSSO                 = internal.FeatureSSO
	FeatureSAML                = internal.FeatureSAML
	FeatureSCIM                = internal.FeatureSCIM
	FeatureWhiteLabel          = internal.FeatureWhiteLabel
	FeatureSharedInbox         = internal.FeatureSharedInbox
	FeatureSharedDrafts        = internal.FeatureSharedDrafts
	FeatureTeamTemplates       = internal.FeatureTeamTemplates
	FeatureAutomation          = internal.FeatureAutomation
	FeatureMailMerge           = internal.FeatureMailMerge
	FeatureBookingPages        = internal.FeatureBookingPages
	FeatureBackupRestore       = internal.FeatureBackupRestore
)

func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	return internal.ParsePublicKey(encoded)
}

func Sign(privateKey ed25519.PrivateKey, license License) (string, error) {
	return internal.Sign(privateKey, license)
}

// SignFunc builds the canonical envelope and delegates the Ed25519 signature
// to an external KMS/HSM-backed signer.
func SignFunc(sign func([]byte) ([]byte, error), license License) (string, error) {
	return internal.SignFunc(sign, license)
}

func FeatureByKey(key string) (Feature, bool) {
	return internal.FeatureByKey(key)
}

func Catalog() []Feature {
	return internal.Catalog()
}

func PaidFeatureKeys() []string {
	return append([]string(nil), internal.PaidFeatureKeys...)
}

func ResolveVerifier(configuredKey string, allowConfigured bool) (Verifier, string, error) {
	return internal.ResolveVerifier(configuredKey, allowConfigured)
}

func ResolveVerifierWithKeys(configuredKey, configuredKeys string, allowConfigured bool) (Verifier, string, error) {
	return internal.ResolveVerifierWithKeys(configuredKey, configuredKeys, allowConfigured)
}

func ConfigKeyAllowed(env string, explicit bool) bool {
	return internal.ConfigKeyAllowed(env, explicit)
}
