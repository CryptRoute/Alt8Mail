package control

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/enterprise"
	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// licenseTokenMaxBytes caps both the license file read and the upload body.
// A real signed token is a few hundred bytes; anything larger is invalid.
const licenseTokenMaxBytes = 64 << 10

// licenseFileMode keeps the token root-owned but group-readable so the a8m
// webmail can load the same file. The Control service writes it; the webmail
// only reads it.
const licenseFileMode os.FileMode = 0o640

var errLicenseFileTooLarge = errors.New("license file exceeds the size limit")

// licenseFileOwner describes the owner uid and group gid to install on the
// license file. The group is always the configured webmail group; an existing
// target's uid is preserved. apply is false on non-root development installs
// and Windows, where chown is skipped and the 0640 mode is used as-is.
type licenseFileOwner struct {
	uid   int
	gid   int
	apply bool
}

// licenseStatus is the non-secret Control Plane view of the shared license
// file. It never contains the token or signature. error is one of the fixed
// categories not_configured, unreadable, malformed or signature.
type licenseStatus struct {
	Configured          bool                `json:"configured"`
	FilePath            string              `json:"filePath"`
	State               string              `json:"state"`
	Edition             string              `json:"edition"`
	BinaryEdition       string              `json:"binaryEdition"`
	EnterpriseInstalled bool                `json:"enterpriseInstalled"`
	InGrace             bool                `json:"inGrace"`
	Expired             bool                `json:"expired"`
	Seats               int                 `json:"seats"`
	LicensedSeats       int                 `json:"licensedSeats"`
	UsedSeats           int                 `json:"usedSeats"`
	RemainingSeats      int                 `json:"remainingSeats"`
	SeatSource          string              `json:"seatSource"`
	CommunitySeatLimit  int                 `json:"communitySeatLimit"`
	Warning             bool                `json:"warning"`
	WarningPercent      int                 `json:"warningPercent"`
	Features            []string            `json:"features"`
	AvailableFeatures   []string            `json:"availableFeatures"`
	MissingFeatures     []string            `json:"missingFeatures"`
	FeatureCatalog      []licensing.Feature `json:"featureCatalog"`
	TrustAnchor         string              `json:"trustAnchor"`
	PortalURL           string              `json:"portalUrl"`
	ExpiresAt           *time.Time          `json:"expiresAt"`
	Tenant              string              `json:"tenant"`
	LicenseID           string              `json:"licenseId"`
	Plan                string              `json:"plan"`
	DeploymentID        string              `json:"deploymentId,omitempty"`
	Trial               bool                `json:"trial"`
	Limits              *licensing.Limits   `json:"limits,omitempty"`
	LastModified        *time.Time          `json:"lastModified"`
	Error               string              `json:"error"`
}

func (s *Server) registerLicenseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/license", s.require("settings.write", s.handleLicenseStatus))
	mux.HandleFunc("POST /control/api/license", s.require("settings.write", s.handleLicenseUpload))
	s.registerLicenseUpgradeRoutes(mux)
}

func (s *Server) enterpriseBinary() bool {
	return enterprise.Registered() != nil
}

func (s *Server) binaryEdition() string {
	if s.enterpriseBinary() {
		return "enterprise"
	}
	return "community"
}

func (s *Server) hasPaidFeature(feature string) bool {
	path := strings.TrimSpace(s.cfg.LicenseFile)
	if path == "" {
		return false
	}
	raw, err := readLicenseTokenFile(path)
	if err != nil {
		return false
	}
	verifier, _, err := licensing.ResolveVerifierWithKeys(
		s.cfg.LicensePublicKey,
		s.cfg.LicensePublicKeys,
		licensing.ConfigKeyAllowed(s.cfg.Env, s.cfg.LicenseAllowConfigKey),
	)
	if err != nil {
		return false
	}
	license, err := verifier.Verify(string(raw))
	if err != nil {
		return false
	}
	if license.Status(time.Now()).Expired {
		return false
	}
	return license.HasFeature(feature)
}

func (s *Server) requireFeature(feature string, next authedHandler) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
		if s.hasPaidFeature(feature) {
			next(w, r, sess)
			return
		}
		details := map[string]any{"feature": feature}
		if descriptor, ok := licensing.FeatureByKey(feature); ok {
			details["label"] = descriptor.Label
		}
		if s.cfg.LicensePortalURL != "" {
			details["upgradeUrl"] = s.cfg.LicensePortalURL + "/upgrade?feature=" + feature
		}
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
			"code": "FEATURE_REQUIRED", "message": "This capability requires a paid Alt8Mail license.", "details": details,
		}})
	}
}

func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	status, byTenant := s.licenseStatusView(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"license": status, "byTenant": byTenant})
}

func (s *Server) handleLicenseUpload(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "CONTENT_TYPE", "Upload the license as application/json")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeLicenseUpload(w, r, &body, licenseTokenMaxBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "LICENSE_TOO_LARGE", "License tokens must be 64 KiB or smaller")
			return
		}
		writeError(w, http.StatusBadRequest, "BAD_JSON", `Send the license as {"token":"A8M1...."}`)
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "LICENSE_TOKEN", "Enter a license token")
		return
	}

	if err := s.writeLicenseToken(s.cfg.LicenseFile, s.cfg.LicensePublicKey, token); err != nil {
		if s.logger != nil {
			var failure *apiFailure
			if errors.As(err, &failure) {
				// Fixed code and configured path only: never the token or the
				// verifier's parser error.
				s.logger.Warn("license upload rejected", "path", s.cfg.LicenseFile, "code", failure.Code)
			}
		}
		writeFromError(w, err)
		return
	}

	status := s.licenseStatus(r.Context())
	s.actorAudit(r, sess, "license.uploaded", s.cfg.LicenseFile, "Uploaded a new Alt8Mail license", map[string]any{
		"licenseId": status.LicenseID,
		"edition":   status.Edition,
		"seats":     status.Seats,
		"expiresAt": status.ExpiresAt,
	})
	if s.logger != nil {
		s.logger.Info("license uploaded", "path", s.cfg.LicenseFile,
			"licenseId", status.LicenseID, "edition", status.Edition)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "restartRequired": true, "license": status,
		"binaryEdition": s.binaryEdition(),
		"enterpriseInstallRequired": !s.enterpriseBinary() && status.Edition != "oss" &&
			(status.State == "valid" || status.State == "grace"),
	})
}

// decodeLicenseUpload additionally requires a clean EOF after the JSON object.
// Without the second decode, a valid object followed by trailing bytes beyond
// the request limit could otherwise be accepted after only the prefix was
// read, bypassing the 64 KiB body cap.
func decodeLicenseUpload(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

// licenseStatus reads and verifies the license file on every call. The file is
// small, so there is no cache to invalidate after an upload or an operator
// replacement. Seat usage is live and intentionally not cached either.
func (s *Server) licenseStatus(ctx context.Context) licenseStatus {
	status, _ := s.licenseStatusView(ctx)
	return status
}

// licenseStatusView also returns the per-tenant seat rows for global licenses.
// tenant-scoped licenses always return an empty list so one tenant's usage
// cannot leak through a deployment-wide license view.
func (s *Server) licenseStatusView(ctx context.Context) (licenseStatus, []map[string]any) {
	defer s.syncSeatPolicy(ctx)
	warningPercent := s.cfg.LicenseWarningPercent
	if warningPercent < 1 || warningPercent > 100 {
		warningPercent = 80
	}
	status := licenseStatus{
		FilePath:            s.cfg.LicenseFile,
		State:               "unlicensed",
		Edition:             "oss",
		BinaryEdition:       s.binaryEdition(),
		EnterpriseInstalled: s.enterpriseBinary(),
		Seats:               communitySeatLimit,
		CommunitySeatLimit:  communitySeatLimit,
		SeatSource:          "community",
		Plan:                "community",
		Features:            []string{},
		AvailableFeatures:   append([]string{}, licensing.PaidFeatureKeys...),
		MissingFeatures:     append([]string{}, licensing.PaidFeatureKeys...),
		FeatureCatalog:      licensing.Catalog(),
		TrustAnchor:         s.licenseTrustSource(),
		PortalURL:           s.cfg.LicensePortalURL,
		WarningPercent:      warningPercent,
	}

	path := strings.TrimSpace(s.cfg.LicenseFile)
	if path == "" {
		status.Error = "not_configured"
		return s.finalizeLicenseStatus(ctx, status, "")
	}
	status.Configured = true
	if info, err := os.Stat(path); err == nil {
		modified := info.ModTime().UTC()
		status.LastModified = &modified
	}

	raw, err := readLicenseTokenFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			status.Error = "not_configured"
			return s.finalizeLicenseStatus(ctx, status, "")
		}
		status.State = "invalid"
		status.Error = "unreadable"
		return s.finalizeLicenseStatus(ctx, status, "")
	}

	verifier, _, err := licensing.ResolveVerifierWithKeys(
		s.cfg.LicensePublicKey,
		s.cfg.LicensePublicKeys,
		licensing.ConfigKeyAllowed(s.cfg.Env, s.cfg.LicenseAllowConfigKey),
	)
	if err != nil {
		status.State = "invalid"
		status.Error = "not_configured"
		return s.finalizeLicenseStatus(ctx, status, "")
	}
	license, err := verifier.Verify(string(raw))
	if err != nil {
		status.State = "invalid"
		status.Error = licenseStatusError(err)
		return s.finalizeLicenseStatus(ctx, status, "")
	}

	lifecycle := license.Status(time.Now())
	status.State = lifecycle.State
	status.InGrace = lifecycle.InGrace
	status.Expired = lifecycle.Expired
	status.Edition = license.Edition
	status.LicenseID = license.ID
	status.Tenant = license.Tenant
	status.Plan = license.EffectivePlan()
	status.DeploymentID = license.DeploymentID
	status.Trial = license.Trial
	status.Limits = lifecycle.Limits
	expiresAt := license.ExpiresAt.UTC()
	status.ExpiresAt = &expiresAt
	if lifecycle.SeatLimit > 0 {
		status.LicensedSeats = lifecycle.SeatLimit
	}
	scope := ""
	if !lifecycle.Expired {
		status.Features = append(status.Features, lifecycle.Features...)
		status.MissingFeatures = license.MissingFeatures()
		if lifecycle.SeatLimit > 0 {
			status.Seats = lifecycle.SeatLimit
			status.SeatSource = "license"
			scope = strings.TrimSpace(license.Tenant)
		}
	} else {
		status.Seats = communitySeatLimit
		status.SeatSource = "community"
		status.MissingFeatures = append([]string{}, licensing.PaidFeatureKeys...)
	}
	return s.finalizeLicenseStatus(ctx, status, scope)
}

func (s *Server) finalizeLicenseStatus(ctx context.Context, status licenseStatus, tenant string) (licenseStatus, []map[string]any) {
	used, perTenant, err := s.licenseSeatUsage(ctx, tenant)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("license seat count failed", "path", s.cfg.LicenseFile, "tenant", tenant)
		}
		used = 0
		perTenant = []map[string]any{}
	}
	status.UsedSeats = used
	status.RemainingSeats = status.Seats - used
	if status.RemainingSeats < 0 {
		status.RemainingSeats = 0
	}
	status.Warning = status.Seats > 0 && used*100 >= status.Seats*status.WarningPercent
	byTenant := []map[string]any{}
	if tenant == "" {
		byTenant = perTenant
	}
	if byTenant == nil {
		byTenant = []map[string]any{}
	}
	return status, byTenant
}

func (s *Server) licenseTrustSource() string {
	_, source, err := licensing.ResolveVerifierWithKeys(
		s.cfg.LicensePublicKey,
		s.cfg.LicensePublicKeys,
		licensing.ConfigKeyAllowed(s.cfg.Env, s.cfg.LicenseAllowConfigKey),
	)
	if err != nil {
		return licensing.TrustAnchorMissing
	}
	return source
}

// licenseStatusError maps verifier errors onto the four fixed categories. No
// parser text, token contents or filesystem detail is ever exposed.
func licenseStatusError(err error) string {
	switch {
	case errors.Is(err, licensing.ErrSignature):
		return "signature"
	case errors.Is(err, licensing.ErrPublicKey):
		return "not_configured"
	default:
		return "malformed"
	}
}

var errControlSeatLimit = errors.New("license seat limit reached")

const (
	controlSeatLockClass = 0x41384D31
	communitySeatLimit   = 25
)

type controlSeatPolicy struct {
	Limit     int
	Tenant    string
	Source    string
	LockKey   int32
	ExpiresAt *time.Time
	GraceDays int
}

func (s *Server) controlSeatPolicy() controlSeatPolicy {
	policy := controlSeatPolicy{Limit: communitySeatLimit, Source: "community"}
	hash := fnv.New32a()
	policy.LockKey = int32(hash.Sum32())
	raw, err := readLicenseTokenFile(strings.TrimSpace(s.cfg.LicenseFile))
	if err != nil {
		return policy
	}
	verifier, _, err := licensing.ResolveVerifierWithKeys(
		s.cfg.LicensePublicKey,
		s.cfg.LicensePublicKeys,
		licensing.ConfigKeyAllowed(s.cfg.Env, s.cfg.LicenseAllowConfigKey),
	)
	if err != nil {
		return policy
	}
	license, err := verifier.Verify(string(raw))
	if err != nil {
		return policy
	}
	// The webmail's global lock is keyed by the license ID even when the
	// license has expired and the effective limit has fallen back to the
	// community allowance. Match that key so both services serialize against
	// the same activation scope.
	licenseHash := fnv.New32a()
	_, _ = licenseHash.Write([]byte(license.ID))
	policy.LockKey = int32(licenseHash.Sum32())
	status := license.Status(time.Now())
	if status.Expired || status.SeatLimit <= 0 {
		return policy
	}
	policy.Limit = status.SeatLimit
	policy.Source = "license"
	policy.Tenant = strings.TrimSpace(license.Tenant)
	expires := license.ExpiresAt.UTC()
	policy.ExpiresAt = &expires
	policy.GraceDays = license.GraceDays
	return policy
}

// syncSeatPolicy publishes the Control-side view to the database trigger. The
// trigger is a backstop, not a replacement for the application checks.
func (s *Server) syncSeatPolicy(ctx context.Context) {
	if s.db == nil {
		return
	}
	policy := s.controlSeatPolicy()
	var expires any
	if policy.ExpiresAt != nil {
		expires = policy.ExpiresAt.UTC()
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO control.seat_policy
		  (id, seats, tenant_id, expires_at, grace_days, lock_key, source, updated_at)
		VALUES (true, $1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (id) DO UPDATE SET
		  seats = EXCLUDED.seats, tenant_id = EXCLUDED.tenant_id,
		  expires_at = EXCLUDED.expires_at, grace_days = EXCLUDED.grace_days,
		  lock_key = EXCLUDED.lock_key, source = EXCLUDED.source,
		  updated_at = now()
	`, policy.Limit, policy.Tenant, expires, policy.GraceDays, policy.LockKey, policy.Source)
	if err != nil && s.logger != nil {
		s.logger.Warn("seat policy sync failed", "err", err)
	}
}

func (s *Server) lockControlSeatScope(ctx context.Context, tx pgx.Tx, tenantID string) (controlSeatPolicy, int, error) {
	policy := s.controlSeatPolicy()
	if policy.Tenant != "" && policy.Tenant != tenantID {
		return policy, 0, nil
	}
	if policy.Tenant != "" {
		if _, err := tx.Exec(ctx, `SELECT id FROM public.tenants WHERE id=$1 FOR UPDATE`, tenantID); err != nil {
			return policy, 0, err
		}
	} else {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, controlSeatLockClass, policy.LockKey); err != nil {
			return policy, 0, err
		}
	}
	query := `SELECT count(*) FROM public.users
		WHERE status = 'active' AND deleted_at IS NULL AND is_system_admin = false`
	args := []any{}
	if policy.Tenant != "" {
		query += ` AND tenant_id = $1`
		args = append(args, policy.Tenant)
	}
	var used int
	if err := tx.QueryRow(ctx, query, args...).Scan(&used); err != nil {
		return policy, 0, err
	}
	return policy, used, nil
}

func (s *Server) ensureControlSeatCapacity(ctx context.Context, tx pgx.Tx, tenantID string, additional int) (int, error) {
	policy, used, err := s.lockControlSeatScope(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}
	if policy.Tenant != "" && policy.Tenant != tenantID {
		return 0, nil
	}
	if used+additional > policy.Limit {
		return used, errControlSeatLimit
	}
	return used, nil
}

func writeControlSeatLimit(w http.ResponseWriter, used, limit int) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
		"code":    "SEAT_LIMIT",
		"message": "The license seat limit has been reached. Deactivate a user or upgrade the license.",
		"details": map[string]any{"used": used, "limit": limit},
	}})
}

// licenseSeatUsage counts active, non-deleted users. A tenant claim scopes the
// count; without one the full tenant list is returned ordered by name. The
// per-tenant rows are only populated for a global license.
func (s *Server) licenseSeatUsage(ctx context.Context, tenant string) (int, []map[string]any, error) {
	if s.seatCounter != nil {
		return s.seatCounter(ctx, tenant)
	}
	byTenant := []map[string]any{}
	if s.db == nil {
		return 0, byTenant, nil
	}
	tenant = strings.TrimSpace(tenant)
	if tenant != "" {
		// A claim that is not a tenant id cannot match any row; treat it as
		// zero usage instead of surfacing a PostgreSQL cast error.
		if _, err := uuid.Parse(tenant); err != nil {
			return 0, byTenant, nil
		}
		var used int
		err := s.db.QueryRow(ctx, `
			SELECT count(*) FROM public.users
			WHERE status = 'active' AND deleted_at IS NULL
			  AND is_system_admin = false AND tenant_id = $1::uuid
		`, tenant).Scan(&used)
		if err != nil {
			return 0, byTenant, err
		}
		return used, byTenant, nil
	}

	rows, err := s.db.Query(ctx, `
		SELECT t.id::text, t.name, count(u.id)::int
		FROM public.tenants t
		LEFT JOIN public.users u
		  ON u.tenant_id = t.id AND u.status = 'active' AND u.deleted_at IS NULL
		 AND u.is_system_admin = false
		GROUP BY t.id, t.name
		ORDER BY lower(t.name), t.name, t.id
	`)
	if err != nil {
		return 0, byTenant, err
	}
	defer rows.Close()
	used := 0
	for rows.Next() {
		var tenantID, name string
		var activeSeats int
		if err := rows.Scan(&tenantID, &name, &activeSeats); err != nil {
			return 0, byTenant, err
		}
		used += activeSeats
		byTenant = append(byTenant, map[string]any{
			"tenantId": tenantID, "name": name, "activeSeats": activeSeats,
		})
	}
	if err := rows.Err(); err != nil {
		return 0, byTenant, err
	}
	return used, byTenant, nil
}

func readLicenseTokenFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, licenseTokenMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > licenseTokenMaxBytes {
		return nil, errLicenseFileTooLarge
	}
	return raw, nil
}

// writeLicenseToken is the handler-facing method; the package-level helper is
// directly testable because it does not depend on Server state.
func (s *Server) writeLicenseToken(path, publicKey, token string) error {
	verifier, _, err := licensing.ResolveVerifierWithKeys(
		publicKey,
		s.cfg.LicensePublicKeys,
		licensing.ConfigKeyAllowed(s.cfg.Env, s.cfg.LicenseAllowConfigKey),
	)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License verification trust anchor is not configured on this server")
	}
	return writeLicenseTokenVerifier(path, verifier, token, s.cfg.LicenseGroup, s.logger)
}

// writeLicenseToken verifies the envelope and only then replaces the shared
// license file. Every failure path (bad key, malformed, wrong key, expired,
// write error) leaves the previous file byte-for-byte intact. This helper uses
// the root group so unit tests do not depend on an installed a8m group;
// production always goes through Server.writeLicenseToken and its configured
// group.
func writeLicenseToken(path, publicKey, token string) error {
	verifier, _, err := licensing.ResolveVerifier(publicKey, true)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License verification is not configured on this server")
	}
	return writeLicenseTokenVerifier(path, verifier, token, "0", nil)
}

func writeLicenseTokenFile(path, publicKey, token, group string, logger *slog.Logger) error {
	verifier, _, err := licensing.ResolveVerifier(publicKey, true)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License verification is not configured on this server")
	}
	return writeLicenseTokenVerifier(path, verifier, token, group, logger)
}

func writeLicenseTokenVerifier(path string, verifier licensing.Verifier, token, group string, logger *slog.Logger) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License storage is not configured on this server")
	}
	token = strings.TrimSpace(token)
	if len(token) > licenseTokenMaxBytes {
		return fail(http.StatusBadRequest, "LICENSE_TOKEN", "That license token is not valid")
	}
	license, err := verifier.Verify(token)
	if err != nil {
		return licenseUploadError(err)
	}
	if license.Status(time.Now()).Expired {
		return fail(http.StatusBadRequest, "LICENSE_EXPIRED", "That license token has expired")
	}
	return writeLicenseFileAtomic(path, token, group, logger)
}

func licenseUploadError(err error) error {
	switch {
	case errors.Is(err, licensing.ErrPublicKey):
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License verification is not configured on this server")
	default:
		// Malformed, unsupported, claim and signature failures share one fixed
		// response so the endpoint is not a verification oracle.
		return fail(http.StatusBadRequest, "LICENSE_TOKEN", "That license token is not valid")
	}
}

func writeLicenseFileAtomic(path, token, group string, logger *slog.Logger) error {
	dir := filepath.Dir(path)
	owner, err := resolveLicenseFileOwner(path, group)
	if err != nil {
		// Production Control runs as root; if the a8m group cannot be resolved
		// the webmail would not be able to read the installed file.
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	tmp, err := os.CreateTemp(dir, ".license-token-*.tmp")
	if err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	name := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()

	if err := tmp.Chmod(licenseFileMode); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	if _, err := tmp.WriteString(token); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	// Ownership is applied to the temporary inode before the final sync and
	// rename so the target never briefly exists with the wrong owner and the
	// ownership metadata is durable with the contents.
	if err := owner.applyTo(name); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	if err := tmp.Sync(); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	if err := tmp.Close(); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	if err := os.Rename(name, path); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	renamed = true

	// Directory sync so the rename survives a crash. Some platforms (notably
	// Windows) do not permit syncing a directory handle; the token itself is
	// already fsynced above, so this failure is logged but not fatal.
	if dirHandle, err := os.Open(dir); err != nil {
		if logger != nil {
			logger.Warn("license directory sync skipped", "dir", dir, "err", err)
		}
	} else {
		if err := dirHandle.Sync(); err != nil && logger != nil {
			logger.Warn("license directory sync failed", "dir", dir, "err", err)
		}
		_ = dirHandle.Close()
	}
	return nil
}
