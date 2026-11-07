package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// storagePolicy is the effective Cabinet quota rule for one user. "combined"
// shares the mailbox allowance (the default); "split" gives Cabinet its own
// allowance. Overrides are resolved user -> domain -> organization default.
type storagePolicy struct {
	Mode              string `json:"mode"`
	EmailQuotaBytes   int64  `json:"emailQuotaBytes"`
	StorageQuotaBytes int64  `json:"storageQuotaBytes"`
	Source            string `json:"source"`
	Domain            string `json:"domain"`
}

type storageUsage struct {
	Policy        storagePolicy `json:"policy"`
	MailUsedBytes int64         `json:"mailUsedBytes"`
	CabinetUsed   int64         `json:"cabinetUsedBytes"`
	MailLimit     int64         `json:"mailLimitBytes"`
	StorageLimit  int64         `json:"storageLimitBytes"`
	UsedBytes     int64         `json:"usedBytes"`
	LimitBytes    int64         `json:"limitBytes"`
	Percent       float64       `json:"percent"`
	Combined      bool          `json:"combined"`
}

func (s *Server) storagePolicyDefault(ctx context.Context, tenantID string) storagePolicy {
	policy := storagePolicy{Mode: "combined", Source: "organization"}
	var mode string
	var emailQuota, storageQuota int64
	err := s.store.DB.QueryRow(ctx, `
		SELECT mode, email_quota_bytes, storage_quota_bytes
		FROM storage_policy_defaults WHERE tenant_id=$1
	`, tenantID).Scan(&mode, &emailQuota, &storageQuota)
	if err == nil {
		if mode == "split" {
			policy.Mode = "split"
		}
		policy.EmailQuotaBytes = emailQuota
		policy.StorageQuotaBytes = storageQuota
	}
	return policy
}

func (s *Server) effectiveStoragePolicy(ctx context.Context, user userRecord) storagePolicy {
	policy := s.storagePolicyDefault(ctx, user.TenantID)
	_, domain := splitEmail(user.Email)
	policy.Domain = domain
	if domain != "" {
		var mode string
		var emailQuota, storageQuota int64
		if err := s.store.DB.QueryRow(ctx, `
			SELECT mode, email_quota_bytes, storage_quota_bytes
			FROM storage_policies
			WHERE tenant_id=$1 AND scope='domain' AND domain=$2
		`, user.TenantID, domain).Scan(&mode, &emailQuota, &storageQuota); err == nil {
			policy.Mode = mode
			policy.EmailQuotaBytes = emailQuota
			policy.StorageQuotaBytes = storageQuota
			policy.Source = "domain"
		}
	}
	var mode string
	var emailQuota, storageQuota int64
	if err := s.store.DB.QueryRow(ctx, `
		SELECT mode, email_quota_bytes, storage_quota_bytes
		FROM storage_policies
		WHERE tenant_id=$1 AND scope='user' AND user_id=$2
	`, user.TenantID, user.ID).Scan(&mode, &emailQuota, &storageQuota); err == nil {
		policy.Mode = mode
		policy.EmailQuotaBytes = emailQuota
		policy.StorageQuotaBytes = storageQuota
		policy.Source = "user"
	}
	return policy
}

func (s *Server) cabinetUsedBytes(ctx context.Context, userID string) int64 {
	var used int64
	_ = s.store.DB.QueryRow(ctx, `
		SELECT COALESCE((SELECT SUM(f.size_bytes) FROM files f
		                 WHERE f.user_id=$1 AND f.provider_id='a8m'), 0)
		     + COALESCE((SELECT SUM(v.size_bytes)
		                 FROM office_document_versions v
		                 JOIN office_documents d ON d.id=v.document_id
		                 JOIN files f ON f.id=d.file_id
		                 WHERE d.user_id=$1 AND v.storage_key <> f.storage_key), 0)
	`, userID).Scan(&used)
	return used
}

func (s *Server) storageUsageFor(ctx context.Context, user userRecord) storageUsage {
	policy := s.effectiveStoragePolicy(ctx, user)
	mailUsed := user.UsedBytes
	if mailUsed < 0 {
		mailUsed = 0
	}
	cabinetUsed := s.cabinetUsedBytes(ctx, user.ID)
	mailLimit := policy.EmailQuotaBytes
	if mailLimit <= 0 {
		mailLimit = user.QuotaBytes
	}
	usage := storageUsage{
		Policy: policy, MailUsedBytes: mailUsed, CabinetUsed: cabinetUsed,
		MailLimit: mailLimit, StorageLimit: policy.StorageQuotaBytes,
	}
	if policy.Mode == "split" {
		usage.LimitBytes = policy.StorageQuotaBytes
		usage.UsedBytes = cabinetUsed
	} else {
		usage.Combined = true
		usage.LimitBytes = mailLimit
		usage.UsedBytes = mailUsed + cabinetUsed
	}
	if usage.LimitBytes > 0 {
		usage.Percent = float64(usage.UsedBytes) / float64(usage.LimitBytes) * 100
	}
	return usage
}

type storageQuotaError struct {
	message string
}

func (e storageQuotaError) Error() string { return e.message }

// enforceStorageQuota is called before a file is written into Alt8Mail Drive.
// External connectors keep their own provider quota. A zero limit means the
// organization has not set an enforceable allowance yet.
func (s *Server) enforceStorageQuota(ctx context.Context, user userRecord, addBytes int64) error {
	if addBytes <= 0 {
		return nil
	}
	usage := s.storageUsageFor(ctx, user)
	if usage.LimitBytes <= 0 {
		return nil
	}
	if usage.UsedBytes+addBytes > usage.LimitBytes {
		label := "Your combined mailbox and Cabinet allowance"
		if usage.Policy.Mode == "split" {
			label = "Your Cabinet allowance"
		}
		return storageQuotaError{message: fmt.Sprintf(
			"%s is full (%s of %s). Remove files or ask an administrator for more storage",
			label, formatBytes(usage.UsedBytes), formatBytes(usage.LimitBytes))}
	}
	return nil
}

func (s *Server) getStorageQuota(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	usage := s.storageUsageFor(r.Context(), sess.User)
	writeJSON(w, http.StatusOK, storageQuotaPayload(usage))
}

func storageQuotaPayload(usage storageUsage) map[string]any {
	label := fmt.Sprintf("%s of %s used", formatBytes(usage.UsedBytes), formatBytes(usage.LimitBytes))
	if usage.LimitBytes <= 0 {
		label = fmt.Sprintf("%s used", formatBytes(usage.UsedBytes))
	}
	return map[string]any{
		"policy":            usage.Policy,
		"mode":              usage.Policy.Mode,
		"combined":          usage.Combined,
		"source":            usage.Policy.Source,
		"mailUsedBytes":     usage.MailUsedBytes,
		"mailLimitBytes":    usage.MailLimit,
		"cabinetUsedBytes":  usage.CabinetUsed,
		"storageLimitBytes": usage.StorageLimit,
		"usedBytes":         usage.UsedBytes,
		"limitBytes":        usage.LimitBytes,
		"percent":           usage.Percent,
		"label":             label,
		"refreshedAt":       time.Now().UTC().Format(time.RFC3339),
	}
}

func cleanStorageMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "split") {
		return "split"
	}
	return "combined"
}
