package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	updateMarker = "/opt/a8m/current/VERSION"
	releaseRoot  = "/opt/a8m/releases"
	updateBinary = "/usr/local/bin/alt8mail-update"
	updateLog    = "/var/log/alt8mail-update.log"
	rebootReady  = "/var/run/reboot-required"
)

var (
	appVersionTag  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z.-]+)?$`)
	checkInstalled = regexp.MustCompile(`installed=([0-9A-Za-z._-]+)\s+latest=([0-9A-Za-z._-]+)`)
	checkUpToDate  = regexp.MustCompile(`(?m)^up-to-date\s+([0-9A-Za-z._-]+)`)
	checkAvailable = regexp.MustCompile(`(?m)^update-available\s+([0-9A-Za-z._-]+)\s+->\s+([0-9A-Za-z._-]+)`)
)

type pendingReboot struct {
	ID             string `json:"id"`
	RequestedAt    string `json:"requestedAt"`
	ActorID        string `json:"actorId"`
	ActorEmail     string `json:"actorEmail"`
	ActorIP        string `json:"actorIp"`
	UserAgent      string `json:"userAgent"`
	PreviousBootID string `json:"previousBootId"`
}

type appUpdateJob struct {
	ID            string
	ActorID       string
	ActorEmail    string
	ActorIP       string
	UserAgent     string
	FromVersion   string
	TargetVersion string
	Status        string
	UnitName      string
	LogPath       string
	Detail        string
	RequestedAt   time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

type releaseRecord struct {
	Version     string `json:"version"`
	Path        string `json:"path"`
	InstalledAt string `json:"installedAt"`
	Current     bool   `json:"current"`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Server) registerSystemRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/system", s.require("server.read", s.handleSystemOverview))
	mux.HandleFunc("POST /control/api/system/reboot", s.require("server.manage", s.handleSystemReboot))
	mux.HandleFunc("POST /control/api/system/update/check", s.require("server.manage", s.handleAppUpdateCheck))
	mux.HandleFunc("POST /control/api/system/update", s.require("server.manage", s.handleAppUpdateStart))
	mux.HandleFunc("GET /control/api/system/update/{id}", s.require("server.read", s.handleAppUpdateJob))
}

// seedServerIdentity fills the three identity fields that the installer knows
// before System Control first opens. Existing operator choices are preserved.
func (s *Server) seedServerIdentity(ctx context.Context) {
	host := strings.TrimSpace(actualHostname())
	if host == "" {
		host = strings.TrimSpace(s.cfg.MailHostname)
	}
	mailHost := strings.TrimSpace(s.cfg.MailHostname)
	webmail := strings.TrimSpace(s.cfg.WebmailURL)
	if webmail == "" && mailHost != "" {
		webmail = "https://" + mailHost
	}
	for key, value := range map[string]string{
		"server.hostname":      host,
		"server.mail_hostname": mailHost,
		"server.webmail_url":   webmail,
	} {
		if value == "" || strings.TrimSpace(s.settingString(ctx, key, "")) != "" {
			continue
		}
		if err := s.setSetting(ctx, key, value, ""); err != nil && s.logger != nil {
			s.logger.Warn("could not seed server identity", "key", key, "err", err)
		}
	}
}

func currentBootID() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (s *Server) pendingReboot(ctx context.Context) *pendingReboot {
	raw, err := s.settingRaw(ctx, "system.pending_reboot")
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var pending pendingReboot
	if json.Unmarshal(raw, &pending) != nil || strings.TrimSpace(pending.ID) == "" {
		return nil
	}
	return &pending
}

func (s *Server) clearPendingReboot(ctx context.Context) {
	_ = s.setSetting(ctx, "system.pending_reboot", map[string]any{}, "")
}

func pendingRebootActor(p *pendingReboot) *adminUser {
	if p == nil || strings.TrimSpace(p.ActorID) == "" {
		return nil
	}
	return &adminUser{ID: p.ActorID, Email: p.ActorEmail}
}

// reconcileRebootState records the result of a reboot requested from the
// panel. It runs on control startup, after the boot ID has changed, and also
// clears a request that never produced a reboot.
func (s *Server) reconcileRebootState(ctx context.Context) {
	current := currentBootID()
	if current == "" {
		return
	}
	pending := s.pendingReboot(ctx)
	if pending != nil {
		switch {
		case pending.PreviousBootID != "" && pending.PreviousBootID != current:
			metadata := map[string]any{
				"requestId": pending.ID, "previousBootId": pending.PreviousBootID,
				"bootId": current, "requestedAt": pending.RequestedAt,
			}
			s.audit(ctx, pendingRebootActor(pending), "system.reboot_completed", "host",
				"Server returned after an operator-requested reboot", net.ParseIP(pending.ActorIP),
				pending.UserAgent, metadata)
			s.notifyEvent(ctx, "system.rebooted", map[string]any{
				"requestId": pending.ID, "bootId": current,
			})
			s.clearPendingReboot(ctx)
		case pending.PreviousBootID == current:
			requested, _ := time.Parse(time.RFC3339, pending.RequestedAt)
			if !requested.IsZero() && time.Since(requested) > 15*time.Minute {
				s.audit(ctx, pendingRebootActor(pending), "system.reboot_not_observed", "host",
					"Reboot request did not change the host boot ID", net.ParseIP(pending.ActorIP),
					pending.UserAgent, map[string]any{"requestId": pending.ID, "bootId": current})
				s.clearPendingReboot(ctx)
			}
		case pending.PreviousBootID == "":
			s.clearPendingReboot(ctx)
		}
	}
	if s.settingString(ctx, "system.last_boot_id", "") != current {
		_ = s.setSetting(ctx, "system.last_boot_id", current, "")
	}
}

func (s *Server) handleSystemReboot(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "REBOOT") {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type REBOOT to confirm the host restart")
		return
	}
	s.reconcileAppUpdateJobs(r.Context())
	if active, err := s.activeAppUpdate(r.Context()); err == nil && active != nil {
		writeError(w, http.StatusConflict, "UPDATE_RUNNING", "An application update is in progress. Wait for it to finish before rebooting.")
		return
	}
	if pending := s.pendingReboot(r.Context()); pending != nil {
		writeError(w, http.StatusConflict, "REBOOT_PENDING", "A reboot is already pending for this host.")
		return
	}
	requestID, err := randomToken(12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REBOOT", "Could not allocate a reboot request id")
		return
	}
	requestID = "rb_" + requestID
	requestedAt := time.Now().UTC().Format(time.RFC3339)
	previousBoot := currentBootID()
	pending := pendingReboot{
		ID: requestID, RequestedAt: requestedAt, ActorID: sess.User.ID,
		ActorEmail: sess.User.Email, ActorIP: s.requestIP(r).String(),
		UserAgent: userAgent(r), PreviousBootID: previousBoot,
	}
	if err := s.setSetting(r.Context(), "system.pending_reboot", pending, sess.User.ID); err != nil {
		writeFromError(w, err)
		return
	}
	if previousBoot != "" {
		_ = s.setSetting(r.Context(), "system.last_boot_id", previousBoot, sess.User.ID)
	}
	s.actorAudit(r, sess, "system.reboot_requested", "host", "Requested a host reboot from System Control",
		map[string]any{"requestId": requestID, "previousBootId": previousBoot, "requestedAt": requestedAt})

	result, err := runCommand(r.Context(), 20*time.Second, "systemd-run",
		"--quiet", "--collect", "--unit=alt8mail-reboot-"+requestID,
		"--timer-property=AccuracySec=1s", "--on-active=3",
		"--description=Alt8Mail operator-requested reboot",
		"/usr/bin/systemctl", "reboot")
	if err != nil {
		s.clearPendingReboot(r.Context())
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		s.actorAudit(r, sess, "system.reboot_failed", "host", "Could not schedule the host reboot",
			map[string]any{"requestId": requestID, "error": detail})
		writeError(w, http.StatusBadGateway, "REBOOT", "Could not schedule the reboot: "+truncateText(detail, 400))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "requestId": requestID, "requestedAt": requestedAt,
		"previousBootId": previousBoot, "bootId": previousBoot,
		"message": "The host will reboot in a few seconds. Keep this page open to watch it return.",
	})
}

func (s *Server) handleSystemOverview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	ctx := r.Context()
	s.reconcileRebootState(ctx)
	s.reconcileAppUpdateJobs(ctx)

	current := s.currentReleaseVersion()
	latest := s.settingString(ctx, "system.latest_version", "")
	checkedAt := s.settingString(ctx, "system.update_checked_at", "")
	updateAvailable := false
	if latest != "" && compareVersions(latest, current) > 0 {
		updateAvailable = true
	}
	releases, _ := listReleases(current)
	jobs, _ := s.listAppUpdateJobs(ctx, 20)
	lastUpdate := s.lastAppUpdate(jobs)
	pending := s.pendingReboot(ctx)
	uptime := readUptime()
	bootedAt := time.Now().UTC().Add(-time.Duration(uptime) * time.Second)
	kernel := ""
	if result, err := runCommand(ctx, 3*time.Second, "uname", "-r"); err == nil {
		kernel = strings.TrimSpace(result.Stdout)
	}
	_, rebootErr := os.Stat(rebootReady)
	host := s.probe.hostMetrics(ctx, false)
	activity := s.recentSystemAudit(ctx, 20)
	lastReboot := s.lastRebootAudit(ctx)

	payload := map[string]any{
		"currentVersion":  current,
		"latestVersion":   latest,
		"checkedAt":       checkedAt,
		"updateAvailable": updateAvailable,
		"channel":         "stable",
		"bootId":          currentBootID(),
		"uptimeSeconds":   uptime,
		"bootedAt":        bootedAt.Format(time.RFC3339),
		"kernel":          kernel,
		"hostname":        actualHostname(),
		"mailHostname":    s.settingString(ctx, "server.mail_hostname", s.cfg.MailHostname),
		"webmailUrl":      s.settingString(ctx, "server.webmail_url", s.cfg.WebmailURL),
		"rebootRequired":  rebootErr == nil,
		"osUpdates":       host.PackageUpdates,
		"releases":        releases,
		"updateJobs":      appUpdateJobViews(jobs, ""),
		"lastUpdate":      lastUpdate,
		"lastReboot":      lastReboot,
		"activity":        activity,
		"pendingReboot":   pending,
		"updateLog":       readLogTail(updateLog, 48<<10),
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleAppUpdateCheck(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 120*time.Second, updateBinary, "--check")
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	current, latest, ok := parseUpdateCheck(output)
	if !ok {
		s.actorAudit(r, sess, "system.app_update_check_failed", "release", "Release check failed",
			map[string]any{"error": truncateText(output, 800)})
		message := "Could not check for an Alt8Mail release"
		if err != nil {
			message += ": " + truncateText(output, 400)
		} else {
			message += ": the updater did not return a version"
		}
		writeError(w, http.StatusBadGateway, "APP_UPDATE_CHECK", message)
		return
	}
	if current == "" {
		current = s.currentReleaseVersion()
	}
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	_ = s.setSetting(r.Context(), "system.latest_version", latest, sess.User.ID)
	_ = s.setSetting(r.Context(), "system.update_checked_at", checkedAt, sess.User.ID)
	s.actorAudit(r, sess, "system.app_update_checked", "release", "Checked for an Alt8Mail release",
		map[string]any{"current": current, "latest": latest, "updateAvailable": compareVersions(latest, current) > 0})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "currentVersion": current, "latestVersion": latest,
		"updateAvailable": compareVersions(latest, current) > 0,
		"checkedAt":       checkedAt, "output": output,
	})
}

func parseUpdateCheck(output string) (current, latest string, ok bool) {
	if match := checkInstalled.FindStringSubmatch(output); len(match) == 3 {
		return strings.TrimPrefix(match[1], "v"), strings.TrimPrefix(match[2], "v"), true
	}
	if match := checkUpToDate.FindStringSubmatch(output); len(match) == 2 {
		value := strings.TrimPrefix(match[1], "v")
		return value, value, true
	}
	if match := checkAvailable.FindStringSubmatch(output); len(match) == 3 {
		return strings.TrimPrefix(match[1], "v"), strings.TrimPrefix(match[2], "v"), true
	}
	return "", "", false
}

func (s *Server) handleAppUpdateStart(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Target  string `json:"target"`
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(w, r, &body, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), "UPDATE") {
		writeError(w, http.StatusBadRequest, "CONFIRM", "Type UPDATE to confirm the release update")
		return
	}
	target := strings.TrimSpace(body.Target)
	if target == "" || strings.EqualFold(target, "latest") {
		target = s.settingString(r.Context(), "system.latest_version", "")
	}
	if target == "" {
		writeError(w, http.StatusBadRequest, "TARGET", "Check for an available release before starting the update")
		return
	}
	if !strings.HasPrefix(target, "v") {
		target = "v" + target
	}
	if !appVersionTag.MatchString(target) {
		writeError(w, http.StatusBadRequest, "TARGET", "The target release tag is invalid")
		return
	}
	if s.pendingReboot(r.Context()) != nil {
		writeError(w, http.StatusConflict, "REBOOT_PENDING", "A reboot is pending. Complete the reboot before updating the application.")
		return
	}
	s.reconcileAppUpdateJobs(r.Context())
	if active, err := s.activeAppUpdate(r.Context()); err == nil && active != nil {
		writeError(w, http.StatusConflict, "UPDATE_RUNNING", "Another Alt8Mail update is already running.")
		return
	}
	current := s.currentReleaseVersion()
	targetVersion := strings.TrimPrefix(target, "v")
	if compareVersions(targetVersion, current) == 0 {
		writeError(w, http.StatusConflict, "UP_TO_DATE", "The installed release is already "+current+".")
		return
	}
	_ = s.setSetting(r.Context(), "system.latest_version", targetVersion, sess.User.ID)

	jobID, err := randomToken(12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE", "Could not allocate an update job id")
		return
	}
	jobID = "upd_" + jobID
	unitName := "alt8mail-app-update-" + jobID
	logPath := "/var/log/a8m-app-update-" + jobID + ".log"
	if file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		_, _ = file.WriteString("[" + time.Now().UTC().Format(time.RFC3339) + "] queued Alt8Mail " + current + " -> " + targetVersion + "\n")
		_ = file.Close()
	}
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO control.app_update_jobs
		  (id,actor_id,actor_email,actor_ip,user_agent,from_version,target_version,status,unit_name,log_path)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'queued',$8,$9)
	`, jobID, sess.User.ID, sess.User.Email, nullableIP(s.requestIP(r)), userAgent(r),
		current, targetVersion, unitName, logPath)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "UPDATE_RUNNING", "Another Alt8Mail update is already running.")
			return
		}
		writeFromError(w, err)
		return
	}

	args := []string{
		"--quiet", "--collect", "--unit=" + unitName,
		"--description=Alt8Mail application update " + target,
		"--property=Environment=HOME=/root GOPATH=/root/go GOMODCACHE=/root/go/pkg/mod GOCACHE=/root/.cache/go-build",
		"--property=StandardOutput=append:" + logPath,
		"--property=StandardError=append:" + logPath,
		updateBinary, "--to", target,
	}
	result, runErr := runCommand(r.Context(), 20*time.Second, "systemd-run", args...)
	if runErr != nil {
		// Some systemd versions reject append: for transient units. The updater
		// still writes its global log, so retry without the per-job redirect.
		logPath = updateLog
		unitName = "alt8mail-app-update-retry-" + jobID
		args = []string{
			"--quiet", "--collect", "--unit=" + unitName,
			"--description=Alt8Mail application update " + target,
			"--property=Environment=HOME=/root GOPATH=/root/go GOMODCACHE=/root/go/pkg/mod GOCACHE=/root/.cache/go-build",
			updateBinary, "--to", target,
		}
		result, runErr = runCommand(r.Context(), 20*time.Second, "systemd-run", args...)
	}
	if runErr != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = runErr.Error()
		}
		_, _ = s.db.Exec(context.Background(), `
			UPDATE control.app_update_jobs
			SET status='failed',detail=$2,finished_at=now(),updated_at=now()
			WHERE id=$1 AND status IN ('queued','running')
		`, jobID, truncateText(detail, 1000))
		s.actorAudit(r, sess, "system.app_update_failed", target, "Could not start the application update",
			map[string]any{"job": jobID, "error": truncateText(detail, 800)})
		writeError(w, http.StatusBadGateway, "APP_UPDATE", "Could not start the update: "+truncateText(detail, 400))
		return
	}
	_, _ = s.db.Exec(r.Context(), `
		UPDATE control.app_update_jobs SET unit_name=$2,log_path=$3,status='running',
		  started_at=COALESCE(started_at,now()),updated_at=now()
		WHERE id=$1
	`, jobID, unitName, logPath)
	s.actorAudit(r, sess, "system.app_update_started", target, "Started an Alt8Mail application update",
		map[string]any{"job": jobID, "fromVersion": current, "targetVersion": targetVersion, "unit": unitName})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "jobId": jobID, "status": "running", "fromVersion": current,
		"targetVersion": targetVersion, "message": "The update is running outside the control-plane service. Follow the live log.",
	})
}

func (s *Server) handleAppUpdateJob(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := strings.TrimSpace(r.PathValue("id"))
	job, err := s.appUpdateJobByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "UPDATE_JOB", "Update job not found")
		return
	}
	job = s.reconcileAppUpdateJob(r.Context(), job)
	log := readLogTail(job.LogPath, 128<<10)
	writeJSON(w, http.StatusOK, job.view(log))
}

func (s *Server) appUpdateJobByID(ctx context.Context, id string) (*appUpdateJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id,COALESCE(actor_id::text,''),actor_email::text,COALESCE(host(actor_ip),''),user_agent,
		       from_version,target_version,status,unit_name,log_path,detail,requested_at,started_at,finished_at
		FROM control.app_update_jobs WHERE id=$1
	`, id)
	return scanAppUpdateJob(row)
}

func scanAppUpdateJob(row rowScanner) (*appUpdateJob, error) {
	var job appUpdateJob
	var started, finished *time.Time
	if err := row.Scan(&job.ID, &job.ActorID, &job.ActorEmail, &job.ActorIP, &job.UserAgent,
		&job.FromVersion, &job.TargetVersion, &job.Status, &job.UnitName, &job.LogPath,
		&job.Detail, &job.RequestedAt, &started, &finished); err != nil {
		return nil, err
	}
	job.StartedAt = started
	job.FinishedAt = finished
	return &job, nil
}

func (s *Server) listAppUpdateJobs(ctx context.Context, limit int) ([]appUpdateJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
		SELECT id,COALESCE(actor_id::text,''),actor_email::text,COALESCE(host(actor_ip),''),user_agent,
		       from_version,target_version,status,unit_name,log_path,detail,requested_at,started_at,finished_at
		FROM control.app_update_jobs ORDER BY requested_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []appUpdateJob{}
	for rows.Next() {
		if job, scanErr := scanAppUpdateJob(rows); scanErr == nil {
			jobs = append(jobs, *job)
		}
	}
	return jobs, rows.Err()
}

func (s *Server) activeAppUpdate(ctx context.Context) (*appUpdateJob, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id,COALESCE(actor_id::text,''),actor_email::text,COALESCE(host(actor_ip),''),user_agent,
		       from_version,target_version,status,unit_name,log_path,detail,requested_at,started_at,finished_at
		FROM control.app_update_jobs WHERE status IN ('queued','running')
		ORDER BY requested_at DESC LIMIT 1
	`)
	job, err := scanAppUpdateJob(row)
	if err != nil {
		return nil, nil
	}
	return job, nil
}

func (s *Server) reconcileAppUpdateJobs(ctx context.Context) {
	jobs, err := s.listAppUpdateJobs(ctx, 50)
	if err != nil {
		return
	}
	for index := range jobs {
		if jobs[index].Status == "queued" || jobs[index].Status == "running" {
			_ = s.reconcileAppUpdateJob(ctx, &jobs[index])
		}
	}
}

func (s *Server) reconcileAppUpdateJob(ctx context.Context, job *appUpdateJob) *appUpdateJob {
	if job == nil || (job.Status != "queued" && job.Status != "running") {
		return job
	}
	state, result, showErr := unitState(ctx, job.UnitName)
	if state == "active" || state == "activating" || state == "reloading" {
		if job.Status == "queued" {
			_, _ = s.db.Exec(ctx, `
				UPDATE control.app_update_jobs SET status='running',
				  started_at=COALESCE(started_at,now()),updated_at=now()
				WHERE id=$1 AND status='queued'
			`, job.ID)
			job.Status = "running"
		}
		return job
	}
	// systemd-run returns before the transient unit is necessarily visible.
	// Do not call a fast start a failure.
	if showErr != nil && time.Since(job.RequestedAt) < 25*time.Second {
		return job
	}
	current := s.currentReleaseVersion()
	status := "failed"
	detail := "Updater did not reach the target release"
	if current == job.TargetVersion {
		status = "completed"
		detail = "Updated to " + job.TargetVersion
	} else if strings.TrimSpace(result) == "timeout" {
		detail = "Updater timed out; the previous release remains active"
	} else if last := lastLogLine(job.LogPath); last != "" {
		detail = truncateText(last, 500)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE control.app_update_jobs
		SET status=$2,detail=$3,finished_at=now(),updated_at=now()
		WHERE id=$1 AND status IN ('queued','running')
	`, job.ID, status, detail)
	if err != nil || tag.RowsAffected() == 0 {
		return s.reloadAppUpdateJob(ctx, job.ID, job)
	}
	refreshed := s.reloadAppUpdateJob(ctx, job.ID, job)
	if refreshed == nil {
		return job
	}
	var actor *adminUser
	if strings.TrimSpace(job.ActorID) != "" {
		actor = &adminUser{ID: job.ActorID, Email: job.ActorEmail}
	}
	s.audit(ctx, actor, "system.app_update_finished", job.TargetVersion, detail,
		net.ParseIP(job.ActorIP), job.UserAgent, map[string]any{
			"job": job.ID, "status": status, "fromVersion": job.FromVersion,
			"targetVersion": job.TargetVersion, "currentVersion": current,
		})
	if status == "completed" {
		_ = s.setSetting(ctx, "system.latest_version", job.TargetVersion, "")
		_ = s.setSetting(ctx, "system.update_checked_at", time.Now().UTC().Format(time.RFC3339), "")
		s.notifyEvent(ctx, "system.updated", map[string]any{
			"source": "app", "job": job.ID, "fromVersion": job.FromVersion,
			"version": job.TargetVersion,
		})
	} else {
		s.notifyEvent(ctx, "system.update_failed", map[string]any{
			"source": "app", "job": job.ID, "version": job.TargetVersion, "error": detail,
		})
	}
	return refreshed
}

// runAppUpdateMonitor reconciles jobs after the updater restarts the control
// plane. Without it, the completion audit would wait for an operator to open
// the System page or poll the job endpoint.
func (s *Server) runAppUpdateMonitor(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileAppUpdateJobs(ctx)
		}
	}
}

func (s *Server) reloadAppUpdateJob(ctx context.Context, id string, fallback *appUpdateJob) *appUpdateJob {
	job, err := s.appUpdateJobByID(ctx, id)
	if err != nil {
		return fallback
	}
	return job
}

func unitState(ctx context.Context, unit string) (activeState, result string, err error) {
	if strings.TrimSpace(unit) == "" {
		return "", "", fmt.Errorf("empty unit")
	}
	out, err := runCommand(ctx, 5*time.Second, "systemctl", "show", unit, "--no-page",
		"-p", "ActiveState", "-p", "Result")
	if err != nil {
		return "", "", err
	}
	values := map[string]string{}
	for _, line := range strings.Split(out.Stdout, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	return values["ActiveState"], values["Result"], nil
}

func (s *Server) currentReleaseVersion() string {
	raw, err := os.ReadFile(updateMarker)
	if err == nil {
		if version := strings.TrimSpace(string(raw)); version != "" {
			return strings.TrimPrefix(version, "v")
		}
	}
	return strings.TrimPrefix(strings.TrimSpace(s.version), "v")
}

func listReleases(current string) ([]releaseRecord, error) {
	entries, err := os.ReadDir(releaseRoot)
	if err != nil {
		return []releaseRecord{}, nil
	}
	releases := []releaseRecord{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(releaseRoot, entry.Name())
		raw, err := os.ReadFile(filepath.Join(path, "VERSION"))
		if err != nil {
			continue
		}
		version := strings.TrimPrefix(strings.TrimSpace(string(raw)), "v")
		if version == "" {
			version = entry.Name()
		}
		info, _ := entry.Info()
		installed := ""
		if info != nil {
			installed = info.ModTime().UTC().Format(time.RFC3339)
		}
		releases = append(releases, releaseRecord{
			Version: version, Path: path, InstalledAt: installed,
			Current: version == strings.TrimPrefix(current, "v"),
		})
	}
	sort.SliceStable(releases, func(i, j int) bool {
		if releases[i].Current != releases[j].Current {
			return releases[i].Current
		}
		return releases[i].InstalledAt > releases[j].InstalledAt
	})
	return releases, nil
}

func (j *appUpdateJob) view(logText string) map[string]any {
	out := map[string]any{
		"id": j.ID, "status": j.Status, "fromVersion": j.FromVersion,
		"targetVersion": j.TargetVersion, "unit": j.UnitName,
		"actorEmail": j.ActorEmail, "detail": j.Detail,
		"requestedAt": j.RequestedAt.UTC().Format(time.RFC3339),
	}
	if j.StartedAt != nil {
		out["startedAt"] = j.StartedAt.UTC().Format(time.RFC3339)
	}
	if j.FinishedAt != nil {
		out["finishedAt"] = j.FinishedAt.UTC().Format(time.RFC3339)
	}
	if logText != "" {
		out["log"] = logText
	}
	return out
}

func appUpdateJobViews(jobs []appUpdateJob, logText string) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for index := range jobs {
		out = append(out, jobs[index].view(logText))
	}
	return out
}

func (s *Server) lastAppUpdate(jobs []appUpdateJob) map[string]any {
	for _, job := range jobs {
		if job.Status == "completed" || job.Status == "failed" {
			return job.view(readLogTail(job.LogPath, 32<<10))
		}
	}
	return nil
}

func (s *Server) recentSystemAudit(ctx context.Context, limit int) []map[string]any {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
		SELECT action, actor_email::text, target, detail, created_at
		FROM control.audit_log
		WHERE action LIKE 'system.%'
		ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	events := []map[string]any{}
	for rows.Next() {
		var action, actor, target, detail string
		var createdAt time.Time
		if rows.Scan(&action, &actor, &target, &detail, &createdAt) == nil {
			events = append(events, map[string]any{
				"action": action, "actor": actor, "target": target,
				"detail": detail, "createdAt": createdAt.UTC().Format(time.RFC3339),
			})
		}
	}
	return events
}

func (s *Server) lastRebootAudit(ctx context.Context) map[string]any {
	var action, actor, detail string
	var createdAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT action, actor_email::text, detail, created_at
		FROM control.audit_log
		WHERE action LIKE 'system.reboot%'
		ORDER BY created_at DESC LIMIT 1
	`).Scan(&action, &actor, &detail, &createdAt)
	if err != nil {
		return nil
	}
	return map[string]any{
		"action": action, "actor": actor, "detail": detail,
		"createdAt": createdAt.UTC().Format(time.RFC3339),
	}
}

func readLogTail(path string, limit int64) string {
	if limit <= 0 {
		limit = 64 << 10
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - limit
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(string(raw), "")
}

func lastLogLine(path string) string {
	text := strings.TrimSpace(readLogTail(path, 16<<10))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

func compareVersions(left, right string) int {
	left = strings.TrimPrefix(strings.TrimSpace(left), "v")
	right = strings.TrimPrefix(strings.TrimSpace(right), "v")
	leftParts := strings.FieldsFunc(left, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	rightParts := strings.FieldsFunc(right, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		var a, b string
		if index < len(leftParts) {
			a = leftParts[index]
		}
		if index < len(rightParts) {
			b = rightParts[index]
		}
		if a == b {
			continue
		}
		ai, aerr := strconv.Atoi(a)
		bi, berr := strconv.Atoi(b)
		if aerr == nil && berr == nil {
			if ai < bi {
				return -1
			}
			return 1
		}
		if a < b {
			return -1
		}
		return 1
	}
	return 0
}
