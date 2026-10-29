package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
	"time"
)

var cronSchedulePattern = regexp.MustCompile(`^(\*|[0-9,\-*/]+)\s+(\*|[0-9,\-*/]+)\s+(\*|[0-9,\-*/]+)\s+(\*|[0-9,\-*/]+)\s+(\*|[0-9,\-*/]+)$`)

type cronJob struct {
	ID           string
	Name         string
	Schedule     string
	Command      string
	RunAs        string
	Stdin        string
	StartDate    *time.Time
	EndDate      *time.Time
	Enabled      bool
	LastRunAt    *time.Time
	LastStatus   string
	LastDuration int64
	CreatedAt    time.Time
}

func (s *Server) registerCronRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/cron", s.require("cron.read", s.handleCronJobs))
	mux.HandleFunc("POST /control/api/cron", s.require("cron.manage", s.handleCronCreate))
	mux.HandleFunc("PATCH /control/api/cron/{id}", s.require("cron.manage", s.handleCronUpdate))
	mux.HandleFunc("DELETE /control/api/cron/{id}", s.require("cron.manage", s.handleCronDelete))
	mux.HandleFunc("POST /control/api/cron/{id}/run", s.require("cron.manage", s.handleCronRunNow))
	mux.HandleFunc("GET /control/api/cron/{id}/runs", s.require("cron.read", s.handleCronRuns))
}

func validateCronSchedule(schedule string) error {
	fields := strings.Fields(schedule)
	if len(fields) != 5 || !cronSchedulePattern.MatchString(strings.Join(fields, " ")) {
		return fmt.Errorf("schedule must be five cron fields")
	}
	return nil
}

func validRunAs(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	_, err := user.Lookup(value)
	return err == nil
}

func (s *Server) handleCronJobs(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,name,schedule,command,run_as,stdin,start_date,end_date,enabled,
		       last_run_at,last_status,last_duration_ms,created_at
		FROM control.cron_jobs ORDER BY name
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var id, name, schedule, command, runAs, stdin, lastStatus string
		var start, end *time.Time
		var lastRun *time.Time
		var enabled bool
		var lastDuration int64
		var created time.Time
		if rows.Scan(&id, &name, &schedule, &command, &runAs, &stdin, &start, &end, &enabled,
			&lastRun, &lastStatus, &lastDuration, &created) == nil {
			job := map[string]any{"id": id, "name": name, "schedule": schedule, "command": command,
				"runAs": runAs, "stdin": stdin, "enabled": enabled, "lastStatus": lastStatus,
				"lastDurationMs": lastDuration, "createdAt": created.UTC().Format(time.RFC3339)}
			if start != nil {
				job["startDate"] = start.Format("2006-01-02")
			}
			if end != nil {
				job["endDate"] = end.Format("2006-01-02")
			}
			if lastRun != nil {
				job["lastRunAt"] = lastRun.UTC().Format(time.RFC3339)
			}
			jobs = append(jobs, job)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "cronFile": s.cfg.CronFile})
}

func parseDatePointer(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return nil, fmt.Errorf("dates must be YYYY-MM-DD")
	}
	return &parsed, nil
}

func (s *Server) handleCronCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Name      string `json:"name"`
		Schedule  string `json:"schedule"`
		Command   string `json:"command"`
		RunAs     string `json:"runAs"`
		Stdin     string `json:"stdin"`
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Command) == "" {
		writeError(w, http.StatusBadRequest, "CRON", "Name and command are required")
		return
	}
	if err := validateCronSchedule(body.Schedule); err != nil {
		writeError(w, http.StatusBadRequest, "SCHEDULE", err.Error())
		return
	}
	if body.RunAs == "" {
		body.RunAs = "root"
	}
	if !validRunAs(body.RunAs) {
		writeError(w, http.StatusBadRequest, "RUN_AS", "Run-as user does not exist")
		return
	}
	start, err := parseDatePointer(body.StartDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, "DATES", err.Error())
		return
	}
	end, err := parseDatePointer(body.EndDate)
	if err != nil || (start != nil && end != nil && end.Before(*start)) {
		writeError(w, http.StatusBadRequest, "DATES", "End date must be after the start date")
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	var id string
	err = s.db.QueryRow(r.Context(), `
		INSERT INTO control.cron_jobs (name,schedule,command,run_as,stdin,start_date,end_date,enabled,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text
	`, strings.TrimSpace(body.Name), strings.Join(strings.Fields(body.Schedule), " "), body.Command,
		body.RunAs, body.Stdin, start, end, enabled, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := s.writeCronFile(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "CRON", "Job saved, but cron file write failed: "+err.Error())
		return
	}
	s.actorAudit(r, sess, "cron.created", body.Name, "Created a scheduled task", map[string]any{"schedule": body.Schedule, "runAs": body.RunAs})
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleCronUpdate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var body struct {
		Name      *string `json:"name"`
		Schedule  *string `json:"schedule"`
		Command   *string `json:"command"`
		RunAs     *string `json:"runAs"`
		Stdin     *string `json:"stdin"`
		StartDate *string `json:"startDate"`
		EndDate   *string `json:"endDate"`
		Enabled   *bool   `json:"enabled"`
	}
	if err := decodeJSON(w, r, &body, 512<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	job, err := s.loadCronJob(r.Context(), id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if body.Name != nil {
		job.Name = strings.TrimSpace(*body.Name)
	}
	if body.Schedule != nil {
		job.Schedule = strings.Join(strings.Fields(*body.Schedule), " ")
	}
	if body.Command != nil {
		job.Command = *body.Command
	}
	if body.RunAs != nil {
		job.RunAs = strings.TrimSpace(*body.RunAs)
	}
	if body.Stdin != nil {
		job.Stdin = *body.Stdin
	}
	if body.StartDate != nil {
		job.StartDate, _ = parseDatePointer(*body.StartDate)
	}
	if body.EndDate != nil {
		job.EndDate, _ = parseDatePointer(*body.EndDate)
	}
	if body.Enabled != nil {
		job.Enabled = *body.Enabled
	}
	if job.Name == "" || job.Command == "" || validateCronSchedule(job.Schedule) != nil || !validRunAs(job.RunAs) {
		writeError(w, http.StatusBadRequest, "CRON", "Name, five-field schedule, command and a valid run-as user are required")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		UPDATE control.cron_jobs SET name=$2,schedule=$3,command=$4,run_as=$5,stdin=$6,
		  start_date=$7,end_date=$8,enabled=$9,updated_at=now() WHERE id=$1
	`, id, job.Name, job.Schedule, job.Command, job.RunAs, job.Stdin, job.StartDate, job.EndDate, job.Enabled)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if err := s.writeCronFile(r.Context()); err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "cron.updated", job.Name, "Updated a scheduled task", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) loadCronJob(ctx context.Context, id string) (cronJob, error) {
	var job cronJob
	err := s.db.QueryRow(ctx, `
		SELECT id::text,name,schedule,command,run_as,stdin,start_date,end_date,enabled,
		       last_run_at,last_status,last_duration_ms,created_at
		FROM control.cron_jobs WHERE id=$1
	`, id).Scan(&job.ID, &job.Name, &job.Schedule, &job.Command, &job.RunAs, &job.Stdin,
		&job.StartDate, &job.EndDate, &job.Enabled, &job.LastRunAt, &job.LastStatus,
		&job.LastDuration, &job.CreatedAt)
	return job, err
}

func (s *Server) handleCronDelete(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	var name string
	_ = s.db.QueryRow(r.Context(), `SELECT name FROM control.cron_jobs WHERE id=$1`, id).Scan(&name)
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.cron_jobs WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Scheduled task not found")
		return
	}
	_ = s.writeCronFile(r.Context())
	s.actorAudit(r, sess, "cron.deleted", name, "Deleted a scheduled task", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCronRunNow(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	job, err := s.loadCronJob(r.Context(), id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	go s.executeCronJob(context.Background(), job, "manual")
	s.actorAudit(r, sess, "cron.run_now", job.Name, "Started a scheduled task manually", nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "job": job.Name})
}

func (s *Server) handleCronRuns(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	rows, err := s.db.Query(r.Context(), `
		SELECT id,status,output,started_at,finished_at,duration_ms
		FROM control.cron_runs WHERE job_id=$1 ORDER BY started_at DESC LIMIT 100
	`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var runID int64
		var status, output string
		var started time.Time
		var finished *time.Time
		var duration int64
		if rows.Scan(&runID, &status, &output, &started, &finished, &duration) == nil {
			item := map[string]any{"id": runID, "status": status, "output": output,
				"startedAt": started.UTC().Format(time.RFC3339), "durationMs": duration}
			if finished != nil {
				item["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			runs = append(runs, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) writeCronFile(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id::text,schedule,run_as FROM control.cron_jobs WHERE enabled ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	executable, _ := os.Executable()
	if executable == "" {
		executable = "/opt/alt8mail/bin/alt8mail-control"
	}
	lines := []string{
		"SHELL=/bin/sh",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"# Managed by Alt8Mail System Control. Do not edit by hand.",
	}
	for rows.Next() {
		var id, schedule, runAs string
		if rows.Scan(&id, &schedule, &runAs) == nil {
			if validateCronSchedule(schedule) != nil || !validRunAs(runAs) || !validUUID(id) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s %s %s cron run %s >/dev/null 2>&1", schedule, runAs, shellQuotePath(executable), id))
		}
	}
	_, err = writeManagedFile(s.cfg.CronFile, strings.Join(lines, "\n")+"\n", 0o644, 0, 0)
	return err
}

func shellQuotePath(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (s *Server) executeCronJob(ctx context.Context, job cronJob, trigger string) {
	now := time.Now()
	if !job.Enabled {
		s.recordCronRun(ctx, job, "skipped", "Job is disabled", 0)
		return
	}
	if job.StartDate != nil && now.Before(*job.StartDate) {
		s.recordCronRun(ctx, job, "skipped", "Before start date", 0)
		return
	}
	if job.EndDate != nil && now.After(job.EndDate.Add(24*time.Hour-time.Second)) {
		s.recordCronRun(ctx, job, "skipped", "After end date", 0)
		return
	}
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	var command *exec.Cmd
	if job.RunAs == "root" {
		command = exec.CommandContext(runCtx, "/bin/sh", "-c", job.Command)
	} else {
		command = exec.CommandContext(runCtx, "runuser", "-u", job.RunAs, "--", "/bin/sh", "-c", job.Command)
	}
	command.Stdin = strings.NewReader(job.Stdin)
	command.Env = append(os.Environ(), "ALT8MAIL_CRON_JOB="+job.ID)
	output, err := command.CombinedOutput()
	status := "success"
	detail := string(output)
	if err != nil {
		status = "failed"
		detail += "\n" + err.Error()
	}
	detail = truncateText(detail, 200_000)
	duration := time.Since(started)
	s.recordCronRun(ctx, job, status, detail, duration)
}

func (s *Server) recordCronRun(ctx context.Context, job cronJob, status, output string, duration time.Duration) {
	var runID int64
	_ = s.db.QueryRow(ctx, `
		INSERT INTO control.cron_runs (job_id,status,output,started_at,finished_at,duration_ms)
		VALUES ($1,$2,$3,now(),now(),$4) RETURNING id
	`, job.ID, status, output, duration.Milliseconds()).Scan(&runID)
	_, _ = s.db.Exec(ctx, `UPDATE control.cron_jobs SET last_run_at=now(), last_status=$2, last_duration_ms=$3 WHERE id=$1`,
		job.ID, status, duration.Milliseconds())
	s.hub.Broadcast("cron.run", map[string]any{"jobId": job.ID, "runId": runID, "status": status, "durationMs": duration.Milliseconds()})
}

func (s *Server) runCronFromCLI(jobID string) error {
	job, err := s.loadCronJob(context.Background(), jobID)
	if err != nil {
		return err
	}
	s.executeCronJob(context.Background(), job, "cron")
	return nil
}
