package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/CryptRoute/Alt8Mail/backend/internal/auth"
	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
	"github.com/jackc/pgx/v5"
)

// This file implements the SCIM host bridge. The private enterprise module owns
// the SCIM HTTP surface; these methods expose only the core identity, seat and
// role-mapping operations it must not reimplement.

func (h enterpriseHost) SCIMBaseURL() (string, error) {
	return h.server.scimBaseURL()
}

func (h enterpriseHost) SCIMRateLimitAllow(ip string) bool {
	return h.server.scimLimit.Allow(ip)
}

func (h enterpriseHost) SCIMRateLimitFail(ip string) {
	h.server.scimLimit.Fail(ip)
}

func (h enterpriseHost) ResetSCIMRateLimit(ip string) {
	h.server.scimLimit.Reset(ip)
}

func (h enterpriseHost) SCIMRateLimitRetryAfter() int {
	return h.server.scimLimit.retryAfterSeconds()
}

func (h enterpriseHost) AuditSCIM(r *http.Request, tenantName, tenantID, action, target, detail string) {
	h.server.audit(r, userRecord{TenantID: tenantID, Name: "SCIM token " + tenantName}, action, target, detail)
}

func (h enterpriseHost) SCIMSeatApplies(tenantID string) bool {
	return h.server.seatAppliesToTenant(tenantID)
}

func (h enterpriseHost) SCIMSeatLimit() int {
	return h.server.licenseSeatLimit()
}

func (h enterpriseHost) SCIMLockSeatScopeTx(ctx context.Context, tx pgx.Tx, tenantID string) error {
	return h.server.lockSeatScopeTx(ctx, tx, tenantID)
}

func (h enterpriseHost) SCIMEnsureSeatCapacityTx(ctx context.Context, tx pgx.Tx, tenantID string, additional int) (int, error) {
	return h.server.ensureSeatCapacityTx(ctx, tx, tenantID, additional)
}

func (h enterpriseHost) SCIMSeatLimitReached(err error) bool {
	return errors.Is(err, errSeatLimit)
}

func (h enterpriseHost) SCIMEnsureTenantDomain(ctx context.Context, tx pgx.Tx, tenantID, email string) error {
	return ensureTenantDomain(ctx, tx, tenantID, email)
}

func (h enterpriseHost) SCIMGenerateCredentialHashes() (string, string, error) {
	webHash, err := auth.HashPassword(randomToken(32))
	if err != nil {
		return "", "", err
	}
	mailHash, err := hashMailPassword(randomToken(32))
	if err != nil {
		return "", "", err
	}
	return webHash, mailHash, nil
}

func (h enterpriseHost) SCIMProvisionUserTx(ctx context.Context, tx pgx.Tx, req enterprise.SCIMProvisionRequest) (string, error) {
	return provisionUserTx(ctx, tx, req.TenantID, req.Name, req.Email, req.Role,
		req.WebPasswordHash, req.MailPasswordHash, req.Quota,
		"", "", "", "", "", nil, req.Status, req.ForcePasswordChange, h.server.cfg.MaildirBase)
}

func (h enterpriseHost) SCIMEnsureUserState(ctx context.Context, email string) {
	if err := h.server.ensureUserState(ctx, email); err != nil {
		h.server.logger.Warn("SCIM user state provisioning failed", "email", email, "err", err)
	}
}

func (h enterpriseHost) SCIMRecomputeRolesTx(ctx context.Context, tx pgx.Tx, tenantID string, userIDs []string) ([]enterprise.SCIMRoleChange, error) {
	changes, err := h.server.scimRecomputeRolesTx(ctx, tx, tenantID, userIDs)
	if err != nil {
		return nil, err
	}
	result := make([]enterprise.SCIMRoleChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, enterprise.SCIMRoleChange{
			UserID: change.UserID, Email: change.Email, OldRole: change.OldRole,
			NewRole: change.NewRole, RoleChanged: change.RoleChanged,
		})
	}
	return result, nil
}
