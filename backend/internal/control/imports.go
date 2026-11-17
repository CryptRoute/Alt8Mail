package control

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/jackc/pgx/v5"
)

var importFolderPattern = regexp.MustCompile(`^[^/\\\x00]{1,200}$`)

type importJob struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	TargetMailbox    string         `json:"targetMailbox"`
	Source           map[string]any `json:"source"`
	Secret           string         `json:"-"`
	Status           string         `json:"status"`
	Progress         int            `json:"progress"`
	MessagesImported int            `json:"messagesImported"`
	Log              string         `json:"log"`
	Error            string         `json:"error"`
	StartedAt        *time.Time     `json:"startedAt,omitempty"`
	FinishedAt       *time.Time     `json:"finishedAt,omitempty"`
}

func (s *Server) registerImportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/imports", s.require("imports.read", s.handleImports))
	mux.HandleFunc("POST /control/api/imports", s.require("imports.manage", s.handleImportCreate))
	mux.HandleFunc("GET /control/api/imports/{id}", s.require("imports.read", s.handleImportDetail))
	mux.HandleFunc("POST /control/api/imports/{id}/stop", s.require("imports.manage", s.handleImportStop))
	mux.HandleFunc("POST /control/api/imports/{id}/retry", s.require("imports.manage", s.handleImportRetry))
	mux.HandleFunc("DELETE /control/api/imports/{id}", s.require("imports.manage", s.handleImportRemove))
}

func (s *Server) handleImports(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text,kind,target_mailbox::text,source,status,progress,messages_imported,
		       left(log,2000),created_at,started_at,finished_at,error
		FROM control.import_jobs ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var id, kind, target, status, log, errText string
		var sourceJSON []byte
		var progress, imported int
		var created time.Time
		var started, finished *time.Time
		if rows.Scan(&id, &kind, &target, &sourceJSON, &status, &progress, &imported, &log, &created, &started, &finished, &errText) == nil {
			var source any
			_ = json.Unmarshal(sourceJSON, &source)
			job := map[string]any{"id": id, "kind": kind, "targetMailbox": target, "source": source,
				"status": status, "progress": progress, "messagesImported": imported, "log": log,
				"createdAt": created.UTC().Format(time.RFC3339), "error": errText}
			if started != nil {
				job["startedAt"] = started.UTC().Format(time.RFC3339)
			}
			if finished != nil {
				job["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			jobs = append(jobs, job)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleImportCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Kind          string `json:"kind"`
		TargetMailbox string `json:"targetMailbox"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		Security      string `json:"security"`
		Insecure      bool   `json:"insecure"`
		Path          string `json:"path"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.TargetMailbox = strings.ToLower(strings.TrimSpace(body.TargetMailbox))
	var exists int
	_ = s.db.QueryRow(r.Context(), `SELECT count(*) FROM public.mailboxes WHERE email=$1 AND active`, body.TargetMailbox).Scan(&exists)
	if exists == 0 {
		writeError(w, http.StatusBadRequest, "TARGET", "Choose an active local mailbox")
		return
	}
	source := map[string]any{}
	secret := ""
	switch body.Kind {
	case "imap":
		body.Host = strings.TrimSpace(body.Host)
		if body.Host == "" || body.Port < 1 || body.Port > 65535 || body.Username == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "IMAP", "Host, port, username and password are required")
			return
		}
		if body.Security != "ssl" && body.Security != "starttls" && body.Security != "plain" {
			writeError(w, http.StatusBadRequest, "IMAP", "Security must be ssl, starttls or plain")
			return
		}
		source = map[string]any{"host": body.Host, "port": body.Port, "username": body.Username,
			"security": body.Security, "insecure": body.Insecure}
		secret = body.Password
		if sealed, err := s.secrets.Seal(secret); err != nil {
			writeError(w, http.StatusInternalServerError, "IMPORT", "Could not protect the import password")
			return
		} else {
			secret = sealed
		}
	case "mbox":
		if body.Path == "" {
			writeError(w, http.StatusBadRequest, "PATH", "Enter a local mbox file or directory")
			return
		}
		info, err := os.Stat(body.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PATH", err.Error())
			return
		}
		if info.IsDir() {
			source = map[string]any{"path": body.Path, "directory": true}
		} else {
			source = map[string]any{"path": body.Path, "directory": false}
		}
	case "maildir":
		path := strings.TrimSuffix(body.Path, "/")
		if path == "" {
			writeError(w, http.StatusBadRequest, "PATH", "Enter a Maildir path")
			return
		}
		if _, err := os.Stat(filepath.Join(path, "cur")); err != nil {
			if _, innerErr := os.Stat(filepath.Join(path, "Maildir", "cur")); innerErr == nil {
				path = filepath.Join(path, "Maildir")
			} else {
				writeError(w, http.StatusBadRequest, "PATH", "Path does not look like a Maildir")
				return
			}
		}
		source = map[string]any{"path": path}
	default:
		writeError(w, http.StatusBadRequest, "KIND", "kind must be imap, mbox or maildir")
		return
	}
	rawSource, _ := json.Marshal(source)
	var id string
	err := s.db.QueryRow(r.Context(), `
		INSERT INTO control.import_jobs (kind,target_mailbox,source,secret_sealed,status,created_by)
		VALUES ($1,$2,$3::jsonb,$4,'queued',$5) RETURNING id::text
	`, body.Kind, body.TargetMailbox, rawSource, secret, sess.User.ID).Scan(&id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "import.queued", body.TargetMailbox, "Queued a mailbox import", map[string]any{"kind": body.Kind})
	s.wakeImportWorker()
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": id, "status": "queued"})
}

func (s *Server) handleImportDetail(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	job, err := s.loadImportJob(r.Context(), pathID(r))
	if err != nil {
		writeFromError(w, err)
		return
	}
	job.Secret = ""
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) loadImportJob(ctx context.Context, id string) (importJob, error) {
	var job importJob
	var raw []byte
	err := s.db.QueryRow(ctx, `
		SELECT id::text,kind,target_mailbox::text,source,secret_sealed,status,progress,messages_imported,log,error,started_at,finished_at
		FROM control.import_jobs WHERE id=$1
	`, id).Scan(&job.ID, &job.Kind, &job.TargetMailbox, &raw, &job.Secret, &job.Status, &job.Progress, &job.MessagesImported, &job.Log, &job.Error, &job.StartedAt, &job.FinishedAt)
	if err != nil {
		return job, err
	}
	_ = json.Unmarshal(raw, &job.Source)
	return job, nil
}

func (s *Server) handleImportStop(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	s.importMu.Lock()
	cancel := s.importCancel[id]
	s.importMu.Unlock()
	if cancel != nil {
		cancel()
	} else {
		_, _ = s.db.Exec(r.Context(), `UPDATE control.import_jobs SET status='stopped', finished_at=now() WHERE id=$1 AND status='queued'`, id)
	}
	s.actorAudit(r, sess, "import.stopped", id, "Requested an import stop", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleImportRetry(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	tag, err := s.db.Exec(r.Context(), `
		UPDATE control.import_jobs SET status='queued', progress=0, messages_imported=0, error='', log='',
		  started_at=NULL, finished_at=NULL WHERE id=$1 AND status IN ('failed','stopped')
	`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "IMPORT", "Only failed or stopped jobs can be retried")
		return
	}
	s.wakeImportWorker()
	s.actorAudit(r, sess, "import.retried", id, "Queued an import retry", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleImportRemove(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	id := pathID(r)
	s.importMu.Lock()
	_, running := s.importCancel[id]
	s.importMu.Unlock()
	if running {
		writeError(w, http.StatusConflict, "IMPORT", "Stop the import before removing it")
		return
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM control.import_jobs WHERE id=$1`, id)
	if err != nil {
		writeFromError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Import job not found")
		return
	}
	s.actorAudit(r, sess, "import.removed", id, "Removed an import job", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) wakeImportWorker() {
	select {
	case s.importWake <- struct{}{}:
	default:
	}
}

func (s *Server) StartImportWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.importWake:
			case <-ticker.C:
			}
			if !s.claimOneImport() {
				continue
			}
		}
	}()
}

func (s *Server) claimOneImport() bool {
	s.importMu.Lock()
	running := len(s.importCancel) > 0
	s.importMu.Unlock()
	if running {
		return false
	}
	tx, err := s.db.Begin(context.Background())
	if err != nil {
		return false
	}
	defer tx.Rollback(context.Background())
	var job importJob
	var raw []byte
	err = tx.QueryRow(context.Background(), `
		SELECT id::text,kind,target_mailbox::text,source,secret_sealed,status,progress,messages_imported,log
		FROM control.import_jobs WHERE status='queued'
		ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED
	`).Scan(&job.ID, &job.Kind, &job.TargetMailbox, &raw, &job.Secret, &job.Status, &job.Progress, &job.MessagesImported, &job.Log)
	if err == pgx.ErrNoRows {
		return false
	}
	if err != nil {
		return false
	}
	_ = json.Unmarshal(raw, &job.Source)
	_, err = tx.Exec(context.Background(), `UPDATE control.import_jobs SET status='running', started_at=now(), progress=1, log='Import started\\n' WHERE id=$1`, job.ID)
	if err != nil {
		return false
	}
	if err := tx.Commit(context.Background()); err != nil {
		return false
	}
	jctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	s.importMu.Lock()
	s.importCancel[job.ID] = cancel
	s.importMu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			s.importMu.Lock()
			delete(s.importCancel, job.ID)
			s.importMu.Unlock()
			s.wakeImportWorker()
		}()
		s.runImportJob(jctx, job)
	}()
	return true
}

func (s *Server) appendImportLog(ctx context.Context, id string, progress, imported int, line string) {
	line = time.Now().UTC().Format("15:04:05") + " " + line + "\n"
	_, _ = s.db.Exec(ctx, `
		UPDATE control.import_jobs SET progress=$2, messages_imported=$3,
		  log = CASE WHEN length(log) < 180000 THEN log || $4 ELSE right(log, 100000) || $4 END
		WHERE id=$1
	`, id, progress, imported, line)
	s.hub.Broadcast("import.progress", map[string]any{"id": id, "progress": progress, "imported": imported, "line": line})
}

func (s *Server) failImport(ctx context.Context, id string, err error) {
	_, _ = s.db.Exec(ctx, `UPDATE control.import_jobs SET status='failed', finished_at=now(), error=$2, log=log||$3 WHERE id=$1`,
		id, truncateText(err.Error(), 1000), time.Now().UTC().Format("[15:04:05] failed: ")+truncateText(err.Error(), 500)+"\n")
}

func (s *Server) runImportJob(ctx context.Context, job importJob) {
	var err error
	switch job.Kind {
	case "imap":
		err = s.importIMAP(ctx, job)
	case "mbox":
		err = s.importMbox(ctx, job)
	case "maildir":
		err = s.importMaildir(ctx, job)
	default:
		err = fmt.Errorf("unknown import kind")
	}
	if err != nil {
		if ctx.Err() == context.Canceled {
			_, _ = s.db.Exec(context.Background(), `UPDATE control.import_jobs SET status='stopped', finished_at=now(), progress=progress WHERE id=$1`, job.ID)
			return
		}
		s.failImport(context.Background(), job.ID, err)
		return
	}
	_, _ = s.db.Exec(context.Background(), `UPDATE control.import_jobs SET status='completed', progress=100, finished_at=now(), log=log||$2 WHERE id=$1`,
		job.ID, time.Now().UTC().Format("[15:04:05] import completed\n"))
}

func (s *Server) importMaildir(ctx context.Context, job importJob) error {
	path := fmt.Sprint(job.Source["path"])
	count := countMaildirMessages(path)
	stage, err := s.prepareImportStage(job.ID)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, "maildir")
	if _, err := copyTree(path, staged); err != nil {
		return fmt.Errorf("stage Maildir: %w", err)
	}
	if err := s.importStageOwner(staged); err != nil {
		return err
	}
	s.appendImportLog(ctx, job.ID, 10, 0, "Importing Maildir "+path)
	if _, err := s.runDoveadmImport(ctx, job.TargetMailbox, "maildir:"+staged, "", "ALL"); err != nil {
		return err
	}
	s.appendImportLog(ctx, job.ID, 100, count, fmt.Sprintf("doveadm import completed (%d messages)", count))
	return nil
}

func (s *Server) prepareImportStage(jobID string) (string, error) {
	uid, gid, err := vmailOwner()
	if err != nil {
		return "", fmt.Errorf("Dovecot mail user is unavailable: %w", err)
	}
	root := filepath.Join(s.cfg.VmailRoot, ".control-imports")
	if err := os.MkdirAll(root, 0o711); err != nil {
		return "", err
	}
	if err := os.Chown(root, 0, 0); err != nil {
		return "", err
	}
	stage := filepath.Join(root, jobID)
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return "", err
	}
	if err := os.Chown(stage, uid, gid); err != nil {
		return "", err
	}
	return stage, nil
}

func (s *Server) importStageOwner(path string) error {
	uid, gid, err := vmailOwner()
	if err != nil {
		return err
	}
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Chown(current, uid, gid); err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(current, 0o700)
		}
		return os.Chmod(current, 0o600)
	})
}

func (s *Server) runDoveadmImport(ctx context.Context, user, source, destination, query string) (commandResult, error) {
	result, err := runCommand(ctx, 12*time.Hour, "doveadm", "import", "-s", "-u", user, source, destination, query)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return result, fmt.Errorf("doveadm import failed: %s", truncateText(detail, 800))
	}
	combined := result.Stdout + "\n" + result.Stderr
	if strings.Contains(combined, "Error:") {
		line := combined
		for _, candidate := range strings.Split(combined, "\n") {
			if strings.Contains(candidate, "Error:") {
				line = candidate
				break
			}
		}
		return result, fmt.Errorf("doveadm import reported an error: %s", truncateText(strings.TrimSpace(line), 800))
	}
	return result, nil
}

func countMboxMessages(path string) int {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "From ") {
			count++
		}
	}
	return count
}

func (s *Server) importMbox(ctx context.Context, job importJob) error {
	path := fmt.Sprint(job.Source["path"])
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stage, err := s.prepareImportStage(job.ID)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if !info.IsDir() {
		count := countMboxMessages(path)
		sourceDir := filepath.Join(stage, "source")
		if err := os.MkdirAll(sourceDir, 0o700); err != nil {
			return err
		}
		staged := filepath.Join(sourceDir, "INBOX")
		if err := copyFileForImport(path, staged); err != nil {
			return fmt.Errorf("stage mbox: %w", err)
		}
		if err := s.importStageOwner(stage); err != nil {
			return err
		}
		s.appendImportLog(ctx, job.ID, 10, 0, "Importing mbox "+path)
		sourceSpec := "mbox:" + sourceDir + ":INBOX=" + staged
		if _, err := s.runDoveadmImport(ctx, job.TargetMailbox, sourceSpec, "", "ALL"); err != nil {
			return err
		}
		s.appendImportLog(ctx, job.ID, 100, count, fmt.Sprintf("mbox import completed (%d messages)", count))
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	stagedRoot := filepath.Join(stage, "mbox")
	if err := os.MkdirAll(stagedRoot, 0o700); err != nil {
		return err
	}
	usedNames := map[string]int{}
	totalMessages := 0
	inboxFile := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		sourcePath := filepath.Join(path, entry.Name())
		count := countMboxMessages(sourcePath)
		folder := sanitizeImportFolder(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if folder == "" {
			folder = fmt.Sprintf("Imported-%d", len(usedNames)+1)
		}
		folder = strings.NewReplacer("/", "-", "\\", "-").Replace(folder)
		baseName := folder
		usedNames[strings.ToLower(baseName)]++
		if usedNames[strings.ToLower(baseName)] > 1 {
			baseName = fmt.Sprintf("%s-%d", folder, usedNames[strings.ToLower(folder)])
		}
		staged := filepath.Join(stagedRoot, baseName)
		if err := copyFileForImport(sourcePath, staged); err != nil {
			return fmt.Errorf("stage mbox %s: %w", entry.Name(), err)
		}
		if strings.EqualFold(baseName, "INBOX") {
			inboxFile = staged
		}
		totalMessages += count
	}
	if err := s.importStageOwner(stage); err != nil {
		return err
	}
	s.appendImportLog(ctx, job.ID, 10, 0, "Importing mbox directory "+path)
	sourceSpec := "mbox:" + stagedRoot
	if inboxFile != "" {
		sourceSpec += ":INBOX=" + inboxFile
	}
	if _, err := s.runDoveadmImport(ctx, job.TargetMailbox, sourceSpec, "", "ALL"); err != nil {
		return err
	}
	s.appendImportLog(ctx, job.ID, 100, totalMessages, fmt.Sprintf("mbox directory import completed (%d messages)", totalMessages))
	return nil
}

func copyFileForImport(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func sanitizeImportFolder(value string) string {
	value = strings.TrimSpace(value)
	if !importFolderPattern.MatchString(value) {
		return ""
	}
	return value
}

func (s *Server) importIMAP(ctx context.Context, job importJob) error {
	password, err := s.secrets.Open(job.Secret)
	if err != nil || password == "" {
		return fmt.Errorf("could not decrypt the import password")
	}
	host := fmt.Sprint(job.Source["host"])
	port := intFromAny(job.Source["port"], 993)
	security := fmt.Sprint(job.Source["security"])
	username := fmt.Sprint(job.Source["username"])
	insecure := false
	if value, ok := job.Source["insecure"].(bool); ok {
		insecure = value
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	tlsConfig := &tls.Config{ServerName: host, InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var client *imapclient.Client
	switch security {
	case "ssl":
		client, err = imapclient.DialWithDialerTLS(dialer, addr, tlsConfig)
	case "starttls":
		client, err = imapclient.DialWithDialer(dialer, addr)
		if err == nil {
			err = client.StartTLS(tlsConfig)
		}
	default:
		client, err = imapclient.DialWithDialer(dialer, addr)
	}
	if err != nil {
		return fmt.Errorf("IMAP connection failed: %w", err)
	}
	defer client.Logout()
	connectionDone := make(chan struct{})
	defer close(connectionDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Terminate()
		case <-connectionDone:
		}
	}()
	if err := client.Login(username, password); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}
	mailboxes := make(chan *imap.MailboxInfo, 32)
	listDone := make(chan error, 1)
	go func() { listDone <- client.List("", "*", mailboxes) }()
	names := []string{}
	for mailbox := range mailboxes {
		if mailbox == nil {
			continue
		}
		selectable := true
		for _, attribute := range mailbox.Attributes {
			if strings.EqualFold(attribute, "\\Noselect") {
				selectable = false
			}
		}
		if selectable {
			names = append(names, mailbox.Name)
		}
	}
	if err := <-listDone; err != nil {
		return fmt.Errorf("IMAP folder listing failed: %w", err)
	}
	tempRoot, err := s.prepareImportStage(job.ID + "-imap")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempRoot)
	totalImported := 0
	for folderIndex, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, err := client.Select(name, true)
		if err != nil {
			s.appendImportLog(ctx, job.ID, 5+int(float64(folderIndex)/float64(len(names))*85), totalImported, "Skipped folder "+name+": "+err.Error())
			continue
		}
		if status.Messages == 0 {
			continue
		}
		folderDir := maildirFolderFor(name, tempRoot)
		if err := os.MkdirAll(filepath.Join(folderDir, "cur"), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(folderDir, "new"), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(folderDir, "tmp"), 0o700); err != nil {
			return err
		}
		seqset := new(imap.SeqSet)
		seqset.AddRange(1, status.Messages)
		section := &imap.BodySectionName{Peek: true}
		items := []imap.FetchItem{section.FetchItem(), imap.FetchFlags, imap.FetchInternalDate, imap.FetchUid}
		messages := make(chan *imap.Message, 16)
		fetchDone := make(chan error, 1)
		go func() { fetchDone <- client.Fetch(seqset, items, messages) }()
		folderCount := 0
		for message := range messages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if message == nil {
				continue
			}
			body := message.GetBody(section)
			if body == nil {
				continue
			}
			filename := maildirFileName(message)
			target := filepath.Join(folderDir, "cur", filename)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, body)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
			if !message.InternalDate.IsZero() {
				_ = os.Chtimes(target, message.InternalDate, message.InternalDate)
			}
			folderCount++
			totalImported++
			if folderCount == 1 || folderCount%50 == 0 {
				s.appendImportLog(ctx, job.ID, 5+int(float64(folderIndex)/float64(len(names))*85), totalImported, fmt.Sprintf("Fetched %s message %d", name, folderCount))
			}
		}
		if err := <-fetchDone; err != nil {
			return fmt.Errorf("IMAP fetch %s failed: %w", name, err)
		}
	}
	if totalImported == 0 {
		s.appendImportLog(ctx, job.ID, 100, 0, "No messages found")
		return nil
	}
	s.appendImportLog(ctx, job.ID, 92, totalImported, fmt.Sprintf("Importing %d messages into %s", totalImported, job.TargetMailbox))
	if err := s.importStageOwner(tempRoot); err != nil {
		return err
	}
	if _, err := s.runDoveadmImport(ctx, job.TargetMailbox, "maildir:"+tempRoot, "", "ALL"); err != nil {
		return err
	}
	s.appendImportLog(ctx, job.ID, 98, totalImported, "Dovecot import completed")
	return nil
}

func intFromAny(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	}
	return fallback
}

func maildirFolderFor(mailbox, root string) string {
	if strings.EqualFold(mailbox, "INBOX") {
		return root
	}
	name := strings.ReplaceAll(mailbox, "/", ".")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", ".")
	return filepath.Join(root, "."+name)
}

func maildirFileName(message *imap.Message) string {
	flags := ""
	for _, flag := range message.Flags {
		switch strings.ToLower(flag) {
		case "\\seen":
			flags += "S"
		case "\\flagged":
			flags += "F"
		case "\\deleted":
			flags += "T"
		case "\\draft":
			flags += "D"
		case "\\answered":
			flags += "R"
		}
	}
	unix := message.InternalDate.Unix()
	if unix <= 0 {
		unix = time.Now().Unix()
	}
	uid := message.Uid
	if uid == 0 {
		uid = message.SeqNum
	}
	return fmt.Sprintf("%d.M%dP%d.alt8mail:2,%s", unix, uid, message.SeqNum, flags)
}
