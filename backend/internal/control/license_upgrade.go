package control

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type licenseUpgradeJob struct {
	DeploymentID string    `json:"deploymentId"`
	Phase        string    `json:"phase"`
	Message      string    `json:"message,omitempty"`
	Error        string    `json:"error,omitempty"`
	LicenseID    string    `json:"licenseId,omitempty"`
	Version      string    `json:"version,omitempty"`
	Enterprise   bool      `json:"enterprise"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type upgradeClaimPayload struct {
	License struct {
		ID               string `json:"id"`
		Token            string `json:"token"`
		Kid              string `json:"kid"`
		Edition          string `json:"edition"`
		Plan             string `json:"plan"`
		Seats            int    `json:"seats"`
		SigningPublicKey string `json:"signingPublicKey"`
	} `json:"license"`
	Bundle *struct {
		URL       string `json:"url"`
		SHA256    string `json:"sha256"`
		Version   string `json:"version"`
		Signature string `json:"signature"`
		PublicKey string `json:"publicKey"`
	} `json:"bundle"`
}

func (s *Server) registerLicenseUpgradeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /control/api/license/upgrade/start", s.require("settings.write", s.handleLicenseUpgradeStart))
	mux.HandleFunc("GET /control/api/license/upgrade/status", s.require("settings.write", s.handleLicenseUpgradeStatus))
	mux.HandleFunc("GET /control/api/license/upgrade/callback", s.require("settings.write", s.handleLicenseUpgradeCallback))
}

func (s *Server) handleLicenseUpgradeStart(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	if strings.TrimSpace(s.cfg.LicensePortalURL) == "" {
		writeError(w, http.StatusServiceUnavailable, "PORTAL_NOT_CONFIGURED", "Set CONTROL_LICENSE_PORTAL_URL to the licensing portal URL first.")
		return
	}
	deployment, err := ensureDeploymentID(s.cfg.DeploymentIDFile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DEPLOYMENT_ID", "Could not read or create the deployment id.")
		return
	}
	now := time.Now().UTC()
	job := &licenseUpgradeJob{
		DeploymentID: deployment, Phase: "started",
		Message:   "Open the licensing portal to start a trial or purchase a license.",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := s.saveUpgradeJob(job); err != nil {
		writeError(w, http.StatusInternalServerError, "UPGRADE_STATE", "Could not persist the upgrade request.")
		return
	}
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PublicURL), "/")
	if base == "" {
		base = requestBaseURL(r)
	}
	callback := base + "/control/api/license/upgrade/callback"
	query := url.Values{}
	query.Set("deployment", deployment)
	query.Set("return", callback)
	portalURL := strings.TrimRight(s.cfg.LicensePortalURL, "/") + "/upgrade?" + query.Encode()
	writeJSON(w, http.StatusOK, map[string]any{
		"portalUrl": portalURL, "deploymentId": deployment, "expiresAt": job.ExpiresAt,
	})
}

func (s *Server) handleLicenseUpgradeStatus(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	job, err := s.loadUpgradeJob()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPGRADE_STATE", "Could not read the upgrade state.")
		return
	}
	if job == nil {
		writeJSON(w, http.StatusOK, map[string]any{"phase": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleLicenseUpgradeCallback(w http.ResponseWriter, r *http.Request, _ *sessionRecord) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	deployment := strings.TrimSpace(r.URL.Query().Get("deployment"))
	if code == "" || deployment == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "The upgrade callback is missing code or deployment.")
		return
	}
	job, err := s.loadUpgradeJob()
	if err != nil || job == nil {
		writeError(w, http.StatusConflict, "NO_UPGRADE", "Start the upgrade from the license page first.")
		return
	}
	if job.DeploymentID != deployment {
		writeError(w, http.StatusForbidden, "DEPLOYMENT_MISMATCH", "This callback belongs to another deployment.")
		return
	}
	if !job.ExpiresAt.After(time.Now()) {
		writeError(w, http.StatusGone, "UPGRADE_EXPIRED", "This upgrade request expired. Start again.")
		return
	}
	if job.Phase == "claiming" || job.Phase == "downloading" || job.Phase == "installing" || job.Phase == "complete" {
		http.Redirect(w, r, "/#/license?upgrade=1", http.StatusSeeOther)
		return
	}
	job.Phase = "claiming"
	job.Message = "Claiming the signed license from the portal..."
	job.UpdatedAt = time.Now().UTC()
	if err := s.saveUpgradeJob(job); err != nil {
		writeError(w, http.StatusInternalServerError, "UPGRADE_STATE", "Could not persist the upgrade request.")
		return
	}
	go s.runLicenseUpgrade(job, code)
	http.Redirect(w, r, "/#/license?upgrade=1", http.StatusSeeOther)
}

func (s *Server) runLicenseUpgrade(job *licenseUpgradeJob, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	fail := func(err error) {
		job.Phase = "error"
		job.Error = err.Error()
		job.Message = "The upgrade failed. The existing Alt8Mail release is unchanged."
		job.UpdatedAt = time.Now().UTC()
		_ = s.saveUpgradeJob(job)
	}

	claim, err := s.claimUpgrade(ctx, code, job.DeploymentID)
	if err != nil {
		fail(err)
		return
	}
	job.LicenseID = claim.License.ID
	publicKey := strings.TrimSpace(s.cfg.LicensePublicKey)
	if publicKey == "" {
		publicKey = strings.TrimSpace(claim.License.SigningPublicKey)
	}
	if publicKey == "" {
		fail(errors.New("the portal did not return a license signing public key and CONTROL_LICENSE_PUBLIC_KEY is empty"))
		return
	}
	if err := writeLicenseTokenFile(s.cfg.LicenseFile, publicKey, claim.License.Token, s.cfg.LicenseGroup, s.logger); err != nil {
		fail(err)
		return
	}
	_ = persistEnvFileValue(s.cfg.AppEnvFile, "ALT8MAIL_LICENSE_FILE", s.cfg.LicenseFile)
	_ = persistEnvFileValue(s.cfg.AppEnvFile, "ALT8MAIL_LICENSE_PUBLIC_KEY", publicKey)
	_ = persistEnvFileValue(s.cfg.AppEnvFile, "ALT8MAIL_LICENSE_PUBLIC_KEY_ID", claim.License.Kid)
	_ = persistEnvFileValue(s.cfg.ControlEnvFile, "CONTROL_LICENSE_FILE", s.cfg.LicenseFile)
	_ = persistEnvFileValue(s.cfg.ControlEnvFile, "CONTROL_LICENSE_PUBLIC_KEY", publicKey)
	job.Message = "License installed."
	job.UpdatedAt = time.Now().UTC()
	_ = s.saveUpgradeJob(job)

	if claim.Bundle == nil || strings.TrimSpace(claim.Bundle.URL) == "" {
		job.Phase = "complete"
		job.Message = "License installed. The portal does not publish an enterprise bundle; install the enterprise release separately."
		job.UpdatedAt = time.Now().UTC()
		_ = s.saveUpgradeJob(job)
		return
	}
	job.Phase = "downloading"
	job.Message = "Downloading the signed enterprise release..."
	job.UpdatedAt = time.Now().UTC()
	_ = s.saveUpgradeJob(job)
	bundlePath, err := s.downloadEnterpriseBundle(ctx, claim.Bundle.URL, claim.Bundle.SHA256)
	if err != nil {
		fail(err)
		return
	}
	defer os.Remove(bundlePath)
	releaseKey := strings.TrimSpace(s.cfg.EnterpriseReleaseKey)
	if releaseKey == "" {
		releaseKey = strings.TrimSpace(claim.Bundle.PublicKey)
	}
	if err := verifyEnterpriseBundleSignature(releaseKey, claim.Bundle.Signature, claim.Bundle.SHA256); err != nil {
		fail(err)
		return
	}
	job.Phase = "installing"
	job.Message = "Installing the enterprise release and restarting services..."
	job.Version = claim.Bundle.Version
	job.UpdatedAt = time.Now().UTC()
	_ = s.saveUpgradeJob(job)
	if err := s.installEnterpriseBundle(ctx, bundlePath, claim.Bundle.Version); err != nil {
		fail(err)
		return
	}
	job.Phase = "complete"
	job.Enterprise = true
	job.Message = "Enterprise release installed and services restarted."
	job.UpdatedAt = time.Now().UTC()
	_ = s.saveUpgradeJob(job)
}

func (s *Server) claimUpgrade(ctx context.Context, code, deployment string) (upgradeClaimPayload, error) {
	base := strings.TrimRight(s.cfg.LicensePortalURL, "/")
	if base == "" {
		return upgradeClaimPayload{}, errors.New("CONTROL_LICENSE_PORTAL_URL is not configured")
	}
	body, _ := json.Marshal(map[string]string{"code": code, "deploymentId": deployment})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/upgrades/claim", strings.NewReader(string(body)))
	if err != nil {
		return upgradeClaimPayload{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return upgradeClaimPayload{}, fmt.Errorf("contact the licensing portal: %w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return upgradeClaimPayload{}, fmt.Errorf("portal claim failed: HTTP %d", response.StatusCode)
	}
	var claim upgradeClaimPayload
	if err := json.Unmarshal(raw, &claim); err != nil {
		return upgradeClaimPayload{}, errors.New("portal claim response is invalid")
	}
	if strings.TrimSpace(claim.License.Token) == "" {
		return upgradeClaimPayload{}, errors.New("portal claim returned no license token")
	}
	return claim, nil
}

func (s *Server) downloadEnterpriseBundle(ctx context.Context, rawURL, expectedSHA string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", errors.New("enterprise bundle URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download enterprise bundle: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download enterprise bundle: HTTP %d", response.StatusCode)
	}
	tmp, err := os.CreateTemp(s.cfg.EnterpriseStateDir, "enterprise-*.tar.gz")
	if err != nil {
		_ = os.MkdirAll(s.cfg.EnterpriseStateDir, 0o700)
		tmp, err = os.CreateTemp(s.cfg.EnterpriseStateDir, "enterprise-*.tar.gz")
	}
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(response.Body, 512<<20+1))
	closeErr := tmp.Close()
	if err != nil || closeErr != nil || written > 512<<20 {
		_ = os.Remove(path)
		return "", errors.New("enterprise bundle download failed or exceeded the size limit")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(strings.TrimSpace(expectedSHA), actual) {
		_ = os.Remove(path)
		return "", errors.New("enterprise bundle checksum does not match")
	}
	return path, nil
}

// verifyEnterpriseBundleSignature checks the release signature produced by
// cmd/release-sign: Ed25519 over "a8m-enterprise-release-sha256:" plus the
// lowercase hex SHA-256 of the bundle. An empty signature is accepted only
// when no release public key is configured, so operators can opt into strict
// verification by setting CONTROL_ENTERPRISE_RELEASE_PUBLIC_KEY.
func verifyEnterpriseBundleSignature(publicKey, signature, checksum string) error {
	publicKey = strings.TrimSpace(publicKey)
	signature = strings.TrimSpace(signature)
	if signature == "" {
		if publicKey != "" {
			return errors.New("enterprise bundle is not signed")
		}
		return nil
	}
	if publicKey == "" {
		return errors.New("enterprise bundle is signed but no release public key is configured")
	}
	rawKey, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil || len(rawKey) != ed25519.PublicKeySize {
		return errors.New("enterprise release public key is invalid")
	}
	rawSignature, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(rawSignature) != ed25519.SignatureSize {
		return errors.New("enterprise bundle signature is invalid")
	}
	digest, err := hex.DecodeString(strings.TrimSpace(checksum))
	if err != nil || len(digest) != sha256.Size {
		return errors.New("enterprise bundle checksum is invalid")
	}
	message := []byte("a8m-enterprise-release-sha256:" + hex.EncodeToString(digest))
	if !ed25519.Verify(ed25519.PublicKey(rawKey), message, rawSignature) {
		return errors.New("enterprise bundle signature verification failed")
	}
	return nil
}

// installEnterpriseBundle is a test seam. Production uses the default below.
func (s *Server) installEnterpriseBundle(ctx context.Context, bundlePath, version string) error {
	if s.enterpriseInstall != nil {
		return s.enterpriseInstall(ctx, bundlePath, version)
	}
	if strings.TrimSpace(s.cfg.EnterpriseInstallCmd) != "" {
		cmd := exec.CommandContext(ctx, s.cfg.EnterpriseInstallCmd, bundlePath, version, s.cfg.EnterpriseInstallRoot)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("enterprise install command failed: %s", strings.TrimSpace(string(output)))
		}
		return nil
	}
	return installEnterpriseRelease(ctx, s.cfg.EnterpriseInstallRoot, bundlePath, version)
}

func installEnterpriseRelease(ctx context.Context, root, bundlePath, version string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "/opt/a8m"
	}
	current := filepath.Join(root, "current")
	if _, err := os.Stat(current); err != nil {
		return fmt.Errorf("current Alt8Mail release not found: %w", err)
	}
	extractDir, err := os.MkdirTemp("", "a8m-enterprise-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	if err := extractTarGz(bundlePath, extractDir); err != nil {
		return err
	}
	binSource := filepath.Join(extractDir, "alt8mail-enterprise-"+version+"-linux-amd64", "bin")
	if _, err := os.Stat(filepath.Join(binSource, "a8m-server")); err != nil {
		matches, _ := filepath.Glob(filepath.Join(extractDir, "*", "bin", "a8m-server"))
		if len(matches) == 0 {
			return errors.New("enterprise bundle does not contain bin/a8m-server")
		}
		binSource = filepath.Dir(matches[0])
	}
	newRelease := filepath.Join(root, "releases", version+"-enterprise-"+time.Now().UTC().Format("20060102150405"))
	_ = os.RemoveAll(newRelease)
	if err := os.MkdirAll(newRelease, 0o755); err != nil {
		return err
	}
	if output, err := exec.CommandContext(ctx, "cp", "-a", current+"/.", newRelease+"/").CombinedOutput(); err != nil {
		return fmt.Errorf("copy current release: %s", strings.TrimSpace(string(output)))
	}
	if err := os.MkdirAll(filepath.Join(newRelease, "bin"), 0o755); err != nil {
		return err
	}
	if output, err := exec.CommandContext(ctx, "cp", "-a", binSource+"/.", filepath.Join(newRelease, "bin")+"/").CombinedOutput(); err != nil {
		return fmt.Errorf("install enterprise binaries: %s", strings.TrimSpace(string(output)))
	}
	marker := map[string]any{"version": version, "installedAt": time.Now().UTC(), "kind": "enterprise"}
	markerRaw, _ := json.MarshalIndent(marker, "", "  ")
	if err := os.WriteFile(filepath.Join(newRelease, "enterprise.json"), markerRaw, 0o644); err != nil {
		return err
	}
	tmpLink := filepath.Join(root, ".current-enterprise")
	_ = os.Remove(tmpLink)
	if err := os.Symlink(newRelease, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, current); err != nil {
		return err
	}
	return enterpriseRestart(ctx, version)
}

// enterpriseRestart is a test seam. The transient unit lets Control persist
// the completed job before systemd restarts the process that scheduled it.
var enterpriseRestart = func(ctx context.Context, version string) error {
	unit := "a8m-enterprise-" + strings.NewReplacer(".", "-", " ", "-").Replace(version)
	script := "sleep 2; systemctl restart a8m-server a8m-admin; systemctl restart alt8mail-control"
	if output, err := exec.CommandContext(ctx, "systemd-run", "--collect", "--unit="+unit, "/bin/sh", "-c", script).CombinedOutput(); err != nil {
		return fmt.Errorf("restart services: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func extractTarGz(archive, dest string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	dest = filepath.Clean(dest)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			return errors.New("enterprise bundle contains an unsafe path")
		}
		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, dest+string(filepath.Separator)) {
			return errors.New("enterprise bundle contains an unsafe path")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(reader, 256<<20)); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

// setEnvFileValue updates or appends one KEY=value line while preserving the
// file's mode and ownership. It is used to make the license trust anchor
// available to the webmail after a one-click portal claim.
func persistEnvFileValue(path, key, value string) error {
	path = strings.TrimSpace(path)
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if path == "" || key == "" || value == "" || strings.ContainsAny(value, "\r\n") {
		return nil
	}
	var uid, gid int
	var haveOwner bool
	mode := os.FileMode(0o640)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		uid, gid, haveOwner = envFileOwner(info)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	replaced := false
	for index, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			lines[index] = key + "=" + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	output := strings.Join(lines, "\n") + "\n"
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(output); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if haveOwner {
		_ = chownEnvFile(tmpName, uid, gid)
	}
	return os.Rename(tmpName, path)
}

func ensureDeploymentID(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/etc/a8m/deployment.id"
	}
	if raw, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(raw)); value != "" {
			return value, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value := "dep_" + hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o640); err != nil {
		return "", err
	}
	return value, nil
}

func requestBaseURL(r *http.Request) string {
	scheme := "https"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	} else if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func (s *Server) upgradeJobPath() string {
	dir := strings.TrimSpace(s.cfg.EnterpriseStateDir)
	if dir == "" {
		dir = "/var/lib/alt8mail-control"
	}
	return filepath.Join(dir, "license-upgrade.json")
}

func (s *Server) saveUpgradeJob(job *licenseUpgradeJob) error {
	path := s.upgradeJobPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) loadUpgradeJob() (*licenseUpgradeJob, error) {
	raw, err := os.ReadFile(s.upgradeJobPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var job licenseUpgradeJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	return &job, nil
}
