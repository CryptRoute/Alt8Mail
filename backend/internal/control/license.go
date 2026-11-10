package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/licensing"
	"github.com/google/uuid"
)

// licenseTokenMaxBytes caps both the license file read and the upload body.
// A real signed token is a few hundred bytes; anything larger is invalid.
const licenseTokenMaxBytes = 64 << 10

// licenseFileMode keeps the token root-owned but group-readable so the a8m
// webmail can load the same file. The Control service writes it; the webmail
// only reads it.
const licenseFileMode os.FileMode = 0o640

var errLicenseFileTooLarge = errors.New("license file exceeds the size limit")

// licenseFileOwner describes the uid/gid to install on the license file.
// apply is false on non-root development installs and Windows, where chown is
// skipped and the 0640 mode is used as-is.
type licenseFileOwner struct {
	uid   int
	gid   int
	apply bool
}

// licenseStatus is the non-secret Control Plane view of the shared license
// file. It never contains the token or signature. error is one of the fixed
// categories not_configured, unreadable, malformed or signature.
type licenseStatus struct {
	Configured     bool       `json:"configured"`
	FilePath       string     `json:"filePath"`
	State          string     `json:"state"`
	Edition        string     `json:"edition"`
	InGrace        bool       `json:"inGrace"`
	Expired        bool       `json:"expired"`
	Seats          int        `json:"seats"`
	UsedSeats      int        `json:"usedSeats"`
	RemainingSeats int        `json:"remainingSeats"`
	Warning        bool       `json:"warning"`
	Features       []string   `json:"features"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	Tenant         string     `json:"tenant"`
	LicenseID      string     `json:"licenseId"`
	LastModified   *time.Time `json:"lastModified"`
	Error          string     `json:"error"`
}

func (s *Server) registerLicenseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/license", s.require("settings.write", s.handleLicenseStatus))
	mux.HandleFunc("POST /control/api/license", s.require("settings.write", s.handleLicenseUpload))
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
	status := licenseStatus{
		FilePath: s.cfg.LicenseFile,
		State:    "unlicensed",
		Edition:  "oss",
		Features: []string{},
	}
	byTenant := []map[string]any{}

	path := strings.TrimSpace(s.cfg.LicenseFile)
	if path == "" {
		status.Error = "not_configured"
		return status, byTenant
	}
	status.Configured = true
	if info, err := os.Stat(path); err == nil {
		modified := info.ModTime().UTC()
		status.LastModified = &modified
	}

	raw, err := readLicenseTokenFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The default path does not exist on a fresh OSS deployment; that
			// is an empty state, not an error to alarm the operator about.
			status.Error = "not_configured"
			return status, byTenant
		}
		status.State = "invalid"
		status.Error = "unreadable"
		return status, byTenant
	}

	publicKey, err := licensing.ParsePublicKey(s.cfg.LicensePublicKey)
	if err != nil {
		status.State = "invalid"
		status.Error = "not_configured"
		return status, byTenant
	}
	license, err := (licensing.Verifier{PublicKey: publicKey}).Verify(string(raw))
	if err != nil {
		status.State = "invalid"
		status.Error = licenseStatusError(err)
		return status, byTenant
	}

	lifecycle := license.Status(time.Now())
	status.State = lifecycle.State
	status.InGrace = lifecycle.InGrace
	status.Expired = lifecycle.Expired
	status.Edition = license.Edition
	status.Seats = lifecycle.SeatLimit
	status.LicenseID = license.ID
	status.Tenant = license.Tenant
	expiresAt := license.ExpiresAt.UTC()
	status.ExpiresAt = &expiresAt
	if !lifecycle.Expired {
		status.Features = append(status.Features, lifecycle.Features...)
	}

	used, perTenant, err := s.licenseSeatUsage(ctx, license.Tenant)
	if err != nil {
		// A seat-count failure must not hide an otherwise valid license; the
		// meter degrades to zero and the operator can retry.
		if s.logger != nil {
			s.logger.Warn("license seat count failed", "path", path, "tenant", license.Tenant)
		}
		used = 0
		perTenant = []map[string]any{}
	}
	status.UsedSeats = used
	status.RemainingSeats = status.Seats - used
	if status.RemainingSeats < 0 {
		status.RemainingSeats = 0
	}
	percent := s.cfg.LicenseWarningPercent
	if percent < 1 || percent > 100 {
		percent = 80
	}
	status.Warning = status.Seats > 0 && used*100 >= status.Seats*percent
	if license.Tenant == "" {
		byTenant = perTenant
	}
	if byTenant == nil {
		byTenant = []map[string]any{}
	}
	return status, byTenant
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
	return writeLicenseTokenFile(path, publicKey, token, s.cfg.LicenseGroup, s.logger)
}

// writeLicenseToken verifies the envelope and only then replaces the shared
// license file. Every failure path (bad key, malformed, wrong key, expired,
// write error) leaves the previous file byte-for-byte intact. This helper uses
// the root group so unit tests do not depend on an installed a8m group;
// production always goes through Server.writeLicenseToken and its configured
// group.
func writeLicenseToken(path, publicKey, token string) error {
	return writeLicenseTokenFile(path, publicKey, token, "0", nil)
}

func writeLicenseTokenFile(path, publicKey, token, group string, logger *slog.Logger) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License storage is not configured on this server")
	}
	key, err := licensing.ParsePublicKey(publicKey)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "LICENSE_NOT_CONFIGURED",
			"License verification is not configured on this server")
	}
	token = strings.TrimSpace(token)
	if len(token) > licenseTokenMaxBytes {
		return fail(http.StatusBadRequest, "LICENSE_TOKEN", "That license token is not valid")
	}
	license, err := (licensing.Verifier{PublicKey: key}).Verify(token)
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
	if err := tmp.Sync(); err != nil {
		return fail(http.StatusInternalServerError, "LICENSE_WRITE", "Could not write the license file")
	}
	// Ownership is applied to the temporary inode before rename so the target
	// never briefly exists with the wrong owner.
	if err := owner.applyTo(name); err != nil {
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
