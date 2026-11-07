package officeagent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const officeFontsDir = "/var/lib/a8m-office/fonts"

type fontInstallResult struct {
	OK       bool     `json:"ok"`
	Files    []string `json:"files"`
	Message  string   `json:"message"`
	Packages bool     `json:"packagesInstalled"`
}

func (s *Server) handleFonts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.installFonts(r.Context())
	if err != nil {
		result.Message = err.Error()
		writeJSON(w, http.StatusBadGateway, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (s *Server) installFonts(ctx context.Context) (fontInstallResult, error) {
	result := fontInstallResult{Files: []string{}}
	if !fileExists(s.cfg.ComposeFile) {
		return result, fmt.Errorf("install Office before adding document fonts")
	}
	if err := os.MkdirAll(s.cfg.FontsDir, 0o755); err != nil {
		return result, fmt.Errorf("could not create the Office font directory: %w", err)
	}
	required := []string{
		"fonts-crosextra-carlito", "fonts-crosextra-caladea", "fonts-liberation",
		"fonts-dejavu-core", "fonts-noto-core",
	}
	// Download and extract the packages instead of installing them into /usr.
	// The agent runs with ProtectSystem=full, so /usr is read-only; extracting
	// into the writable Office data directory keeps the host untouched while
	// still supplying the font files to the container mount.
	aptGet := ""
	if output, _, err := s.run(ctx, 10*time.Second, "sh", "-c", "command -v apt-get"); err == nil {
		aptGet = strings.TrimSpace(output)
	}
	dpkgDeb := ""
	if output, _, err := s.run(ctx, 10*time.Second, "sh", "-c", "command -v dpkg-deb"); err == nil {
		dpkgDeb = strings.TrimSpace(output)
	}
	if aptGet != "" && dpkgDeb != "" {
		workDir := s.cfg.FontsDir + "-work"
		_ = os.RemoveAll(workDir)
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return result, fmt.Errorf("could not prepare the font download directory: %w", err)
		}
		defer os.RemoveAll(workDir)
		script := fmt.Sprintf(`set -e
cd %s
apt-get update >/dev/null 2>&1 || true
apt-get download %s
for f in *.deb; do dpkg-deb -x "$f" extract; done
if [ -d extract/usr/share/fonts ]; then
  find extract/usr/share/fonts -type f \( -iname '*.ttf' -o -iname '*.otf' \) -exec cp -f {} %s/ \;
fi
`, shellQuote(workDir), strings.Join(required, " "), shellQuote(s.cfg.FontsDir))
		if _, stderr, err := s.run(ctx, 15*time.Minute, "sh", "-c", script); err != nil {
			return result, fmt.Errorf("could not download the compatible font packages: %s", trim(stderr))
		}
		result.Packages = true
	}
	fontRoots := []string{
		"/usr/share/fonts/truetype/crosextra",
		"/usr/share/fonts/truetype/liberation",
		"/usr/share/fonts/truetype/liberation2",
		"/usr/share/fonts/truetype/dejavu",
		"/usr/share/fonts/truetype/noto",
	}
	for _, root := range fontRoots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		_, _, _ = s.run(ctx, 2*time.Minute, "find", root, "-type", "f",
			"(", "-iname", "*.ttf", "-o", "-iname", "*.otf", ")",
			"-exec", "cp", "-f", "{}", s.cfg.FontsDir+"/", ";")
	}
	entries, err := os.ReadDir(s.cfg.FontsDir)
	if err != nil {
		return result, fmt.Errorf("could not read installed fonts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".ttf" || ext == ".otf" {
			result.Files = append(result.Files, name)
		}
	}
	sort.Strings(result.Files)
	if len(result.Files) == 0 {
		return result, fmt.Errorf("no TrueType or OpenType fonts were found on the Office host")
	}
	st, _ := s.loadState()
	image := strings.TrimSpace(st.Image)
	if image == "" {
		image = strings.TrimSpace(s.cfg.DefaultImage)
	}
	if image == "" {
		return result, fmt.Errorf("the OnlyOffice image is not configured")
	}
	secret, err := s.ensureSecret()
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(s.cfg.ComposeFile, []byte(composeContents(image, secret, s.cfg.ContainerName, s.cfg.HTTPPort, s.cfg.FontsDir)), 0o640); err != nil {
		return result, fmt.Errorf("could not write the Office Compose file: %w", err)
	}
	if _, stderr, err := s.run(ctx, 10*time.Minute, s.cfg.DockerBin, "compose", "-f", s.cfg.ComposeFile, "up", "-d"); err != nil {
		return result, fmt.Errorf("could not apply the font mount: %s", trim(stderr))
	}
	if err := s.waitHealthy(ctx); err != nil {
		return result, fmt.Errorf("the Office container did not become healthy after applying the font mount: %w", err)
	}
	// The Document Server image scans the mounted custom-font directory during
	// its font generation pass. Run the generator explicitly, then restart so
	// existing worker processes reload the font cache.
	generated := false
	for _, script := range []string{"documentserver-generate-allfonts.sh", "/usr/bin/documentserver-generate-allfonts.sh"} {
		if _, _, err := s.run(ctx, 10*time.Minute, s.cfg.DockerBin, "exec", s.cfg.ContainerName, script); err == nil {
			generated = true
			break
		}
	}
	if !generated {
		return result, fmt.Errorf("the fonts were mounted, but the Document Server font generator was not available; restart OnlyOffice and try again")
	}
	if _, stderr, err := s.run(ctx, 3*time.Minute, s.cfg.DockerBin, "restart", s.cfg.ContainerName); err != nil {
		return result, fmt.Errorf("fonts were generated but OnlyOffice could not restart: %s", trim(stderr))
	}
	if err := s.waitHealthy(ctx); err != nil {
		return result, fmt.Errorf("fonts were generated but OnlyOffice did not become healthy: %w", err)
	}
	result.OK = true
	result.Message = fmt.Sprintf("Installed %d compatible font files and regenerated the OnlyOffice font cache", len(result.Files))
	return result, nil
}
