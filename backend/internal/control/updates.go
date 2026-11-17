package control

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:-]{0,199}$`)

type packageUpdate struct {
	Name       string `json:"name"`
	Current    string `json:"current"`
	Candidate  string `json:"candidate"`
	Repository string `json:"repository"`
	Arch       string `json:"arch"`
	Security   bool   `json:"security"`
}

type updateJob struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Packages   []string  `json:"packages"`
	All        bool      `json:"all"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	ExitCode   int       `json:"exitCode"`
	Error      string    `json:"error,omitempty"`
	Remaining  int       `json:"remaining"`
	output     *safeBuffer
	mu         sync.Mutex
}

func (j *updateJob) appendOutput(data []byte) {
	if j.output != nil {
		_, _ = j.output.Write(data)
	}
}

func (j *updateJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]any{
		"id": j.ID, "status": j.Status, "packages": j.Packages, "all": j.All,
		"startedAt": j.StartedAt.UTC().Format(time.RFC3339), "exitCode": j.ExitCode,
		"remaining": j.Remaining,
	}
	if !j.FinishedAt.IsZero() {
		out["finishedAt"] = j.FinishedAt.UTC().Format(time.RFC3339)
	}
	if j.Error != "" {
		out["error"] = j.Error
	}
	if j.output != nil {
		out["output"] = j.output.String()
	}
	return out
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (s *Server) registerUpdateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/infra/updates", s.require("server.read", s.handleUpdates))
	mux.HandleFunc("POST /control/api/infra/updates/check", s.require("server.manage", s.handleUpdatesCheck))
	mux.HandleFunc("POST /control/api/infra/updates/upgrade", s.require("server.manage", s.handleUpdatesUpgrade))
	mux.HandleFunc("GET /control/api/infra/updates/jobs/{id}", s.require("server.read", s.handleUpdateJob))
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	packages, err := listPackageUpdates(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "UPDATES", "Could not read available package updates: "+err.Error())
		return
	}
	security := 0
	for _, pkg := range packages {
		if pkg.Security {
			security++
		}
	}
	_, rebootErr := os.Stat("/var/run/reboot-required")
	lastChecked := s.settingString(r.Context(), "infra.updates_checked_at", "")
	writeJSON(w, http.StatusOK, map[string]any{
		"packages": packages, "count": len(packages), "security": security,
		"rebootRequired": rebootErr == nil, "lastChecked": lastChecked,
		"checkedAt": nowRFC3339(),
	})
}

func (s *Server) handleUpdatesCheck(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 180*time.Second, "apt-get", "update", "-qq")
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"code": "APT_UPDATE", "message": "apt-get update failed", "details": output,
		}})
		return
	}
	packages, listErr := listPackageUpdates(r.Context())
	if listErr != nil {
		writeError(w, http.StatusBadGateway, "UPDATES", "Package index updated but the package list could not be read: "+listErr.Error())
		return
	}
	checkedAt := nowRFC3339()
	_ = s.setSetting(r.Context(), "infra.updates_checked_at", checkedAt, sess.User.ID)
	security := 0
	for _, pkg := range packages {
		if pkg.Security {
			security++
		}
	}
	_, rebootErr := os.Stat("/var/run/reboot-required")
	s.actorAudit(r, sess, "system.updates_checked", "apt", "Refreshed the OS package index",
		map[string]any{"available": len(packages), "security": security})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "output": output, "packages": packages, "count": len(packages),
		"security": security, "rebootRequired": rebootErr == nil, "lastChecked": checkedAt,
	})
}

func listPackageUpdates(ctx context.Context) ([]packageUpdate, error) {
	result, err := runCommand(ctx, 60*time.Second, "apt", "list", "--upgradable")
	output := result.Stdout + "\n" + result.Stderr
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(output))
	}
	return parseAptUpgradable(result.Stdout), nil
}

func parseAptUpgradable(output string) []packageUpdate {
	seen := map[string]bool{}
	packages := []packageUpdate{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing") || strings.HasPrefix(line, "WARNING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[0], "/") {
			continue
		}
		nameRepo := strings.SplitN(fields[0], "/", 2)
		name := nameRepo[0]
		if !packageNamePattern.MatchString(name) || seen[name] {
			continue
		}
		arch := ""
		if len(fields) > 2 {
			arch = fields[2]
		}
		current := ""
		for i, field := range fields {
			if field == "from:" && i+1 < len(fields) {
				current = strings.TrimSuffix(fields[i+1], "]")
				break
			}
		}
		repo := nameRepo[1]
		seen[name] = true
		packages = append(packages, packageUpdate{
			Name: name, Current: current, Candidate: fields[1], Repository: repo, Arch: arch,
			Security: strings.Contains(strings.ToLower(repo), "security"),
		})
	}
	sort.SliceStable(packages, func(i, j int) bool {
		if packages[i].Security != packages[j].Security {
			return packages[i].Security
		}
		return packages[i].Name < packages[j].Name
	})
	return packages
}

func (s *Server) handleUpdatesUpgrade(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Packages []string `json:"packages"`
		All      bool     `json:"all"`
	}
	if err := decodeJSON(w, r, &body, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	available, err := listPackageUpdates(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "UPDATES", "Could not read the available updates: "+err.Error())
		return
	}
	valid := map[string]bool{}
	for _, pkg := range available {
		valid[pkg.Name] = true
	}
	packages := []string{}
	if !body.All {
		seen := map[string]bool{}
		for _, name := range body.Packages {
			name = strings.TrimSpace(name)
			if !packageNamePattern.MatchString(name) {
				writeError(w, http.StatusBadRequest, "PACKAGE", "Invalid package name "+name)
				return
			}
			if !valid[name] {
				writeError(w, http.StatusBadRequest, "PACKAGE", name+" is not in the current list of available updates")
				return
			}
			if !seen[name] {
				seen[name] = true
				packages = append(packages, name)
			}
		}
		if len(packages) == 0 {
			writeError(w, http.StatusBadRequest, "PACKAGE", "Select at least one package to update")
			return
		}
	}
	jobID, idErr := randomToken(18)
	if idErr != nil {
		writeError(w, http.StatusInternalServerError, "JOB", "Could not allocate an update job id")
		return
	}
	job := &updateJob{
		ID: "apt_" + jobID, Status: "queued", Packages: packages, All: body.All,
		StartedAt: time.Now().UTC(), output: &safeBuffer{},
	}
	s.updateMu.Lock()
	for _, existing := range s.updateJobs {
		if existing.Status == "queued" || existing.Status == "running" {
			s.updateMu.Unlock()
			writeError(w, http.StatusConflict, "BUSY", "Another package update is already running")
			return
		}
	}
	s.updateJobs[job.ID] = job
	s.updateMu.Unlock()
	actor := &adminUser{ID: sess.User.ID, Email: sess.User.Email}
	ip := s.requestIP(r)
	ua := userAgent(r)
	go s.runUpdateJob(job, actor, ip, ua)
	s.actorAudit(r, sess, "system.updates_started", "apt", "Started an OS package update",
		map[string]any{"job": job.ID, "packages": packages, "all": body.All})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "jobId": job.ID, "status": "queued",
		"message": "Package update started. Follow the live output in the terminal panel."})
}

func (s *Server) runUpdateJob(job *updateJob, actor *adminUser, ip net.IP, ua string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()
	args := []string{"-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	if job.All {
		args = append(args, "upgrade")
	} else {
		args = append(args, "install", "--only-upgrade")
		args = append(args, job.Packages...)
	}
	job.appendOutput([]byte("$ apt-get " + strings.Join(args, " ") + "\n\n"))
	cmd := exec.CommandContext(ctx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	cmd.Stdout = job.output
	cmd.Stderr = job.output
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		job.appendOutput([]byte("\n[command failed] " + err.Error() + "\n"))
	}
	remaining, listErr := listPackageUpdates(context.Background())
	job.mu.Lock()
	job.ExitCode = exitCode
	job.FinishedAt = time.Now().UTC()
	job.Remaining = len(remaining)
	if err == nil {
		job.Status = "completed"
	} else {
		job.Status = "failed"
		job.Error = err.Error()
	}
	if listErr != nil && err == nil {
		job.Status = "completed"
		job.appendOutput([]byte("\n[warning] could not re-read the package list: " + listErr.Error() + "\n"))
	}
	job.mu.Unlock()
	job.appendOutput([]byte(fmt.Sprintf("\n[%s] exit %d · %d update(s) remaining\n", job.Status, exitCode, len(remaining))))
	s.audit(context.Background(), actor, "system.updates_finished", "apt",
		"OS package update "+job.Status, ip, ua, map[string]any{
			"job": job.ID, "status": job.Status, "exitCode": exitCode, "remaining": len(remaining),
		})
	if job.Status == "completed" {
		s.notifyEvent(context.Background(), "system.updated", map[string]any{
			"job": job.ID, "remaining": len(remaining), "packages": job.Packages, "all": job.All,
		})
	} else {
		s.notifyEvent(context.Background(), "system.update_failed", map[string]any{
			"job": job.ID, "error": job.Error, "packages": job.Packages,
		})
	}
}

func (s *Server) handleUpdateJob(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(r.PathValue("id"))
	s.updateMu.Lock()
	job := s.updateJobs[id]
	var stale []string
	for key, candidate := range s.updateJobs {
		if !candidate.FinishedAt.IsZero() && time.Since(candidate.FinishedAt) > 24*time.Hour {
			stale = append(stale, key)
		}
	}
	for _, key := range stale {
		delete(s.updateJobs, key)
	}
	s.updateMu.Unlock()
	if job == nil {
		writeError(w, http.StatusNotFound, "JOB", "That update job is no longer available")
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot())
}
