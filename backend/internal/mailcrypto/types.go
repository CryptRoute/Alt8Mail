package mailcrypto

import (
	"context"
	"net/textproto"
	"time"
)

// Protocol identifies the cryptographic message format, not the key algorithm.
type Protocol string

const (
	ProtocolNone    Protocol = ""
	ProtocolOpenPGP Protocol = "openpgp"
	ProtocolSMIME   Protocol = "smime"
)

// Mode is the tenant/user encryption model. It is intentionally explicit:
// server-managed encryption is not end-to-end encryption.
type Mode string

const (
	ModeDisabled      Mode = "disabled"
	ModeServerManaged Mode = "server_managed"
	ModeUserUnlocked  Mode = "user_unlocked"
	ModeClientSide    Mode = "client_side"
)

// Kind describes the exact outer MIME shape that was detected.
type Kind string

const (
	KindNone                 Kind = ""
	KindSMIMEEnveloped       Kind = "smime-enveloped"
	KindSMIMESigned          Kind = "smime-signed"
	KindSMIMESignedEncrypted Kind = "smime-signed-encrypted"
	KindSMIMECertsOnly       Kind = "smime-certs-only"
	KindPGPMIMEEncrypted     Kind = "pgp-mime-encrypted"
	KindPGPMIMESigned        Kind = "pgp-mime-signed"
	KindPGPInlineEncrypted   Kind = "pgp-inline-encrypted"
	KindPGPInlineSigned      Kind = "pgp-inline-signed"
	KindEncryptedAttachment  Kind = "encrypted-attachment"
	KindUnknownProtected     Kind = "unknown-protected"
	KindMalformed            Kind = "malformed"
)

// TrustLevel is the user-visible trust state of a key, separate from whether a
// signature is mathematically valid.
type TrustLevel string

const (
	TrustUnknown    TrustLevel = "unknown"
	TrustTOFU       TrustLevel = "tofu"
	TrustVerified   TrustLevel = "verified"
	TrustEnterprise TrustLevel = "enterprise"
	TrustRevoked    TrustLevel = "revoked"
)

// SignatureState is deliberately richer than valid/invalid. A cryptographically
// valid signature from an untrusted or changed key is not the same as a trusted
// signer.
type SignatureState string

const (
	SignatureNone           SignatureState = "none"
	SignatureValid          SignatureState = "valid"
	SignatureValidUntrusted SignatureState = "valid_untrusted"
	SignatureInvalid        SignatureState = "invalid"
	SignatureExpired        SignatureState = "expired"
	SignatureRevoked        SignatureState = "revoked"
	SignatureKeyChanged     SignatureState = "key_changed"
	SignatureUnknown        SignatureState = "unknown"
)

// HeaderInfo is the outer RFC 5322 metadata used for indexing, key lookup, and
// UI. It intentionally does not include a parsed body.
type HeaderInfo struct {
	From        []string `json:"from,omitempty"`
	Sender      string   `json:"sender,omitempty"`
	ReplyTo     []string `json:"replyTo,omitempty"`
	To          []string `json:"to,omitempty"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	MessageID   string   `json:"messageId,omitempty"`
	Date        string   `json:"date,omitempty"`
	ContentType string   `json:"contentType,omitempty"`
}

// Part is one MIME entity with its exact raw bytes preserved. RawHeaders and
// RawBody are not normalized; callers that verify signatures must use Raw or
// the explicit canonicalization helpers instead of re-serializing a parsed
// message.
type Part struct {
	Index             int
	Depth             int
	MediaType         string
	Params            map[string]string
	Disposition       string
	DispositionParams map[string]string
	TransferEncoding  string
	Headers           textproto.MIMEHeader
	RawHeaders        []byte
	RawBody           []byte
	Raw               []byte
	Children          []*Part
}

// SMIMEInfo describes the S/MIME shape visible without decrypting.
type SMIMEInfo struct {
	Type            string `json:"type,omitempty"` // enveloped-data, signed-data, certs-only
	Protocol        string `json:"protocol,omitempty"`
	DigestAlgorithm string `json:"digestAlgorithm,omitempty"`
}

// PGPInfo describes the PGP/MIME shape visible without decrypting.
type PGPInfo struct {
	Version string `json:"version,omitempty"`
	Inline  bool   `json:"inline,omitempty"`
}

// Protection is the result of structural inspection. SignedContent and
// Signature refer to parts in Analysis.Parts.
type Protection struct {
	Encrypted              bool       `json:"encrypted"`
	Signed                 bool       `json:"signed"`
	Protocol               Protocol   `json:"protocol,omitempty"`
	Kind                   Kind       `json:"kind,omitempty"`
	SMIME                  *SMIMEInfo `json:"smime,omitempty"`
	PGP                    *PGPInfo   `json:"pgp,omitempty"`
	HasEncryptedAttachment bool       `json:"hasEncryptedAttachment,omitempty"`
	HasSignedAttachment    bool       `json:"hasSignedAttachment,omitempty"`
	SignedContent          *Part      `json:"-"`
	Signature              *Part      `json:"-"`
	EncryptedPart          *Part      `json:"-"`
}

// Analysis is the non-destructive Phase 0 view of one RFC 822 message.
type Analysis struct {
	Headers    HeaderInfo `json:"headers"`
	Protection Protection `json:"protection"`
	Parts      []*Part    `json:"-"`
	Warnings   []string   `json:"warnings,omitempty"`
	Size       int        `json:"size"`
}

// RecipientKey is a public key or certificate selected for one recipient.
type RecipientKey struct {
	Email       string
	Protocol    Protocol
	KeyID       string
	Fingerprint string
	PublicKey   []byte
	CertChain   [][]byte
	NotBefore   time.Time
	NotAfter    time.Time
	Revoked     bool
	Trust       TrustLevel
	Source      string
}

// IdentityRef identifies a key owned by a user without exposing private key
// material to callers. The provider resolves it through the key service.
type IdentityRef struct {
	UserID string
	Email  string
	KeyID  string
}

// EncryptRequest and SignRequest are intentionally protocol-neutral. Providers
// add protocol-specific behavior behind the interface.
type EncryptRequest struct {
	Identity         IdentityRef
	PrivateKey       []byte
	Certificate      []byte
	CertificateChain [][]byte
	Recipients       []RecipientKey
	MIME             []byte
	Sign             bool
	EncryptToSelf    bool
}

type EncryptResult struct {
	Protocol        Protocol
	MIME            []byte
	RecipientKeyIDs []string
	Signed          bool
}

type SignRequest struct {
	Identity         IdentityRef
	PrivateKey       []byte
	Certificate      []byte
	CertificateChain [][]byte
	MIME             []byte
}

type SignResult struct {
	Protocol Protocol
	MIME     []byte
	KeyID    string
}

type DecryptRequest struct {
	Identity         IdentityRef
	PrivateKey       []byte
	Certificate      []byte
	CertificateChain [][]byte
	Message          []byte
}

type DecryptResult struct {
	Protocol  Protocol
	MIME      []byte
	Signature SignatureStatus
	Warnings  []string
}

type VerifyRequest struct {
	Protocol  Protocol
	PublicKey []byte
	Message   []byte
}

// SignatureStatus is rendered separately from the encryption badge.
type SignatureStatus struct {
	State       SignatureState `json:"state"`
	SignerEmail string         `json:"signerEmail,omitempty"`
	KeyID       string         `json:"keyId,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Trust       TrustLevel     `json:"trust,omitempty"`
	SignedAt    *time.Time     `json:"signedAt,omitempty"`
	Reason      string         `json:"reason,omitempty"`
}

// Capabilities lets the UI and policy layer ask what a provider supports
// before showing a composer toggle.
type Capabilities struct {
	CanEncrypt       bool
	CanSign          bool
	CanDecrypt       bool
	CanVerify        bool
	ProtectedHeaders bool
	Algorithms       []string
}

// Provider is the single seam between the mail pipeline and a protocol
// implementation. OpenPGP and S/MIME implement the same interface.
type Provider interface {
	Protocol() Protocol
	Capabilities() Capabilities
	Encrypt(ctx context.Context, req EncryptRequest) (EncryptResult, error)
	Sign(ctx context.Context, req SignRequest) (SignResult, error)
	Decrypt(ctx context.Context, req DecryptRequest) (DecryptResult, error)
	Verify(ctx context.Context, req VerifyRequest) (SignatureStatus, error)
}

// Policy is the outbound gate. It is defined now so Phase 1 can wire the
// composer and DLP to the same rules.
type Policy struct {
	Mode                     Mode
	AllowedProtocols         []Protocol
	RequireSignature         bool
	RequireVerifiedRecipient bool
	AllowInlinePGP           bool
	EncryptToSelf            bool
}

// ValidateOutbound returns a human-readable refusal reason, or "" when the
// requested message is allowed. It does not perform key lookup.
func (p Policy) ValidateOutbound(protection Protection, recipients []RecipientKey) string {
	if p.Mode == ModeDisabled && (protection.Encrypted || protection.Signed) {
		return "encrypted or signed mail is disabled by policy"
	}
	allowed := false
	for _, protocol := range p.AllowedProtocols {
		if protocol == protection.Protocol {
			allowed = true
			break
		}
	}
	if protection.Protocol != ProtocolNone && len(p.AllowedProtocols) > 0 && !allowed {
		return "this encryption protocol is not allowed by policy"
	}
	if protection.Encrypted && len(recipients) == 0 {
		return "no recipient encryption key is available"
	}
	for _, recipient := range recipients {
		if recipient.Protocol != protection.Protocol && protection.Protocol != ProtocolNone {
			return "recipient key protocol does not match the message protocol"
		}
		if recipient.Revoked {
			return "a recipient key has been revoked"
		}
		if !recipient.NotAfter.IsZero() && time.Now().After(recipient.NotAfter) {
			return "a recipient key has expired"
		}
		if p.RequireVerifiedRecipient && recipient.Trust != TrustVerified && recipient.Trust != TrustEnterprise {
			return "a recipient key is not verified"
		}
	}
	if p.RequireSignature && !protection.Signed {
		return "a signature is required by policy"
	}
	return ""
}
