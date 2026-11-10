package httpapi

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/jackc/pgx/v5"
)

const (
	// seatLimitMessage is the fixed admin error returned when an activation
	// would exceed the licensed seat count.
	seatLimitMessage = "The license seat limit has been reached. Deactivate a user or upgrade the license."
	// globalSeatLockClass namespaces the advisory lock that serializes
	// seat-consuming changes for a global (multi-tenant) license.
	globalSeatLockClass int32 = 0x41384D31
)

// errSeatLimit is returned by the seat helpers when an activation would exceed
// the licensed seat count. Callers map it to the admin or SCIM error envelope.
var errSeatLimit = errors.New("license seat limit reached")

// rowQuerier is the subset of pgxpool.Pool/pgx.Tx used to count seats. Counting
// through the provisioning transaction lets the row lock and the re-count share
// one snapshot ordering.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadLicense reads and verifies the optional offline license file. A missing
// file is a normal OSS deployment; an unreadable or invalid file is reported
// to the caller so startup can continue with paid features disabled. When
// ALT8MAIL_LICENSE_TENANT is configured, the payload's tenant claim must match
// it exactly (an absent claim never matches).
func loadLicense(cfg config.Config, logger *slog.Logger) (*licensing.License, error) {
	if strings.TrimSpace(cfg.LicenseFile) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(cfg.LicenseFile)
	if err != nil {
		return nil, fmt.Errorf("read license file: %w", err)
	}
	publicKey, err := licensing.ParsePublicKey(cfg.LicensePublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse license public key: %w", err)
	}
	license, err := (licensing.Verifier{PublicKey: publicKey}).Verify(string(raw))
	if err != nil {
		return nil, fmt.Errorf("verify license: %w", err)
	}
	if expected := strings.TrimSpace(cfg.LicenseTenant); expected != "" && license.Tenant != expected {
		return nil, fmt.Errorf("license tenant %q does not match ALT8MAIL_LICENSE_TENANT", license.Tenant)
	}
	if logger != nil {
		status := license.Status(time.Now())
		logger.Info("license loaded", "edition", license.Edition, "state", status.State,
			"seats", status.SeatLimit, "daysRemaining", status.DaysRemaining)
	}
	return &license, nil
}

// licenseTime is the clock used to evaluate the license lifecycle. Production
// uses time.Now; tests can inject a clock to move through valid, grace and
// expired without waiting.
func (s *Server) licenseTime() time.Time {
	if s.licenseNow != nil {
		return s.licenseNow()
	}
	return time.Now()
}

// licenseSnapshot returns the non-secret entitlement view exposed to the
// webmail and Control Plane. Raw tokens, signatures and private material never
// leave this process. The lifecycle is evaluated against the current clock on
// every call, so a long-running server transitions through valid, grace and
// expired without restarting.
func (s *Server) licenseSnapshot(ctx context.Context) map[string]any {
	if s.license == nil {
		state := "unlicensed"
		if s.licenseConfigured {
			state = "invalid"
		}
		return map[string]any{
			"edition": "oss", "state": state, "inGrace": false, "expired": false,
			"seats": 0, "seatLimit": 0, "usedSeats": 0,
			"features": []string{}, "daysRemaining": 0, "expiresAt": "",
		}
	}
	status := s.license.Status(s.licenseTime())
	features := append([]string(nil), status.Features...)
	if status.Expired || features == nil {
		// An expired license must not advertise usable paid features.
		features = []string{}
	}
	used := 0
	if s.store != nil && s.store.DB != nil {
		count, err := s.activeSeatCount(ctx, "")
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("license seat count failed", "err", err)
			}
		} else {
			used = count
		}
	}
	return map[string]any{
		"edition": s.license.Edition, "state": status.State,
		"inGrace": status.InGrace, "expired": status.Expired,
		"seats": status.SeatLimit, "seatLimit": status.SeatLimit, "usedSeats": used,
		"features":      features,
		"daysRemaining": status.DaysRemaining,
		"expiresAt":     status.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// licenseSeatLimit is the licensed seat count, or zero without a license.
func (s *Server) licenseSeatLimit() int {
	if s.license == nil {
		return 0
	}
	return s.license.Seats
}

// licenseSeatTenant is the tenant claim that scopes seat counting. It is empty
// for a global license.
func (s *Server) licenseSeatTenant() string {
	if s.license == nil {
		return ""
	}
	return strings.TrimSpace(s.license.Tenant)
}

// seatEnforced reports whether activation consumes a limited seat. Only a
// loaded license in the valid or grace state with a positive seat count gates
// user creation. OSS, unlicensed, invalid and expired deployments keep core
// user creation available; expired paid features are gated separately.
func (s *Server) seatEnforced() bool {
	if s.license == nil {
		return false
	}
	status := s.license.Status(s.licenseTime())
	if status.SeatLimit <= 0 {
		return false
	}
	return status.State == "valid" || status.State == "grace"
}

// seatAppliesToTenant reports whether seat enforcement covers this tenant. A
// tenant-scoped license limits only the tenant named in its claim; every other
// tenant keeps core mail and user creation available.
func (s *Server) seatAppliesToTenant(tenantID string) bool {
	if !s.seatEnforced() {
		return false
	}
	scope := s.licenseSeatTenant()
	return scope == "" || scope == tenantID
}

// activeSeatCount counts the users that consume a seat from the pool. A
// tenant-scoped license always counts the licensed tenant; a global license
// counts every tenant. System administrators and soft-deleted users never
// consume a seat.
func (s *Server) activeSeatCount(ctx context.Context, tenantID string) (int, error) {
	if s.store == nil || s.store.DB == nil {
		return 0, errors.New("license seat count requires a database")
	}
	return s.countActiveSeats(ctx, s.store.DB, tenantID)
}

// countActiveSeats runs the seat query through q, which may be the pool for a
// read-only check or the provisioning transaction for a locked re-count.
// tenantID names the caller's tenant but is ignored for a global license,
// whose count spans every tenant.
func (s *Server) countActiveSeats(ctx context.Context, q rowQuerier, tenantID string) (int, error) {
	query := `SELECT count(*) FROM users
		WHERE status = 'active' AND deleted_at IS NULL AND is_system_admin = false`
	args := []any{}
	if scope := s.licenseSeatTenant(); scope != "" {
		query += ` AND tenant_id = $1`
		args = append(args, scope)
	}
	var count int
	if err := q.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// seatAvailable reports whether another active seat is available for tenantID.
// It is a read-only convenience for callers outside a provisioning transaction;
// activation paths must use ensureSeatCapacityTx after locking.
func (s *Server) seatAvailable(ctx context.Context, tenantID string) (bool, error) {
	if !s.seatAppliesToTenant(tenantID) {
		return true, nil
	}
	used, err := s.activeSeatCount(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return used < s.licenseSeatLimit(), nil
}

// lockSeatScopeTx serializes seat-consuming changes before they re-count. The
// tenant row lock covers a tenant-scoped license and same-tenant creations. A
// global license counts users in every tenant, so an advisory lock keyed by the
// license further serializes activations across tenants.
func (s *Server) lockSeatScopeTx(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if !s.seatAppliesToTenant(tenantID) {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id = $1 FOR UPDATE`, tenantID); err != nil {
		return err
	}
	if s.licenseSeatTenant() == "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, globalSeatLockClass, s.globalSeatLockKey()); err != nil {
			return err
		}
	}
	return nil
}

// globalSeatLockKey derives a stable advisory-lock key from the license id so
// separate licenses (and separate deployments sharing a database) do not
// serialize each other.
func (s *Server) globalSeatLockKey() int32 {
	hash := fnv.New32a()
	if s.license != nil {
		_, _ = hash.Write([]byte(s.license.ID))
	}
	return int32(hash.Sum32())
}

// ensureSeatCapacityTx re-counts inside the locked transaction and reports
// errSeatLimit when adding `additional` active users would exceed the license.
// It returns the current usage for audit metadata on both success and failure.
func (s *Server) ensureSeatCapacityTx(ctx context.Context, tx pgx.Tx, tenantID string, additional int) (int, error) {
	if !s.seatAppliesToTenant(tenantID) {
		return 0, nil
	}
	used, err := s.countActiveSeats(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}
	if used+additional > s.licenseSeatLimit() {
		return used, errSeatLimit
	}
	return used, nil
}

// recordSeatLimitBlocked writes the admin audit row for a rejected activation.
// It records only the tenant, limit and usage; the raw license token and
// signature are never included.
func (s *Server) recordSeatLimitBlocked(ctx context.Context, sess *adminSessionRecord, tenantID, target string, used int) {
	limit := s.licenseSeatLimit()
	detail := fmt.Sprintf("License seat limit reached (tenant %s, limit %d, used %d)", tenantID, limit, used)
	if sess == nil {
		sess = &adminSessionRecord{ActiveTenantID: tenantID}
	}
	s.recordAdminAudit(ctx, sess, sess.User, "license.seat_limit_blocked", target, detail, map[string]any{
		"tenantId": tenantID, "limit": limit, "used": used,
	})
}

// writeSeatLimit writes the fixed admin 409 used for every rejected activation.
func writeSeatLimit(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "SEAT_LIMIT", seatLimitMessage)
}

// enforceAdminSeatTx locks the seat scope and verifies capacity for an admin
// activation. It writes the response and returns false when the request must
// stop (limit reached or the count could not be verified).
func (s *Server) enforceAdminSeatTx(ctx context.Context, w http.ResponseWriter, tx pgx.Tx, sess *adminSessionRecord, tenantID, target string, additional int) bool {
	if !s.seatAppliesToTenant(tenantID) {
		return true
	}
	if err := s.lockSeatScopeTx(ctx, tx, tenantID); err != nil {
		_ = tx.Rollback(ctx)
		if s.logger != nil {
			s.logger.Warn("license seat lock failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "LICENSE", "Could not verify the license seat limit")
		return false
	}
	used, err := s.ensureSeatCapacityTx(ctx, tx, tenantID, additional)
	if errors.Is(err, errSeatLimit) {
		// Release the tenant row lock before writing the audit: admin_audit_log
		// has a foreign key to tenants, whose FK check would otherwise wait on
		// the lock this transaction still holds.
		_ = tx.Rollback(ctx)
		s.recordSeatLimitBlocked(ctx, sess, tenantID, target, used)
		writeSeatLimit(w)
		return false
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		if s.logger != nil {
			s.logger.Warn("license seat count failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "LICENSE", "Could not verify the license seat limit")
		return false
	}
	return true
}

// entitled reports whether a verified, non-expired license includes a paid
// feature. Grace-period licenses remain usable; expired licenses do not.
func (s *Server) entitled(feature string) bool {
	if s.license == nil {
		return false
	}
	if s.license.Status(s.licenseTime()).Expired {
		return false
	}
	return s.license.HasFeature(feature)
}

func (s *Server) getEntitlements(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.licenseSnapshot(r.Context()))
}
