package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/meridian-mail/meridian/backend/internal/auth"
)

func (s *Server) latestConfigSync(ctx context.Context) (map[string]any, error) {
	var id, trigger, status, errText string
	var started time.Time
	var finished *time.Time
	var warningsJSON, driftJSON, detailsJSON []byte
	var filesChanged int
	err := s.db.QueryRow(ctx, `
		SELECT id::text, trigger, status, started_at, finished_at, files_changed,
		       warnings, drift, details, error
		FROM control.config_sync_runs ORDER BY started_at DESC LIMIT 1
	`).Scan(&id, &trigger, &status, &started, &finished, &filesChanged, &warningsJSON, &driftJSON, &detailsJSON, &errText)
	if err == pgx.ErrNoRows {
		return map[string]any{"status": "never", "trigger": "never", "filesChanged": 0, "warnings": []any{}, "drift": []any{}, "details": map[string]any{}}, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"id": id, "trigger": trigger, "status": status, "startedAt": started.UTC().Format(time.RFC3339),
		"filesChanged": filesChanged, "error": errText,
	}
	if finished != nil {
		result["finishedAt"] = finished.UTC().Format(time.RFC3339)
	}
	var warnings, drift, details any
	_ = json.Unmarshal(warningsJSON, &warnings)
	_ = json.Unmarshal(driftJSON, &drift)
	_ = json.Unmarshal(detailsJSON, &details)
	result["warnings"], result["drift"], result["details"] = warnings, drift, details
	return result, nil
}

func (s *Server) handleConfigSync(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	latest, _ := s.latestConfigSync(r.Context())
	drift := s.configDrift(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"latest": latest, "currentDrift": drift})
}

func (s *Server) handleConfigSyncHistory(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	rows, err := s.db.Query(r.Context(), `
		SELECT id::text, trigger, status, started_at, finished_at, files_changed, error
		FROM control.config_sync_runs ORDER BY started_at DESC LIMIT 30
	`)
	if err != nil {
		writeFromError(w, err)
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id, trigger, status, errText string
		var started time.Time
		var finished *time.Time
		var changed int
		if rows.Scan(&id, &trigger, &status, &started, &finished, &changed, &errText) == nil {
			run := map[string]any{"id": id, "trigger": trigger, "status": status,
				"startedAt": started.UTC().Format(time.RFC3339), "filesChanged": changed, "error": errText}
			if finished != nil {
				run["finishedAt"] = finished.UTC().Format(time.RFC3339)
			}
			runs = append(runs, run)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleConfigSyncRun(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	run, err := s.performConfigSync(r.Context(), sess.User.ID, "manual")
	if err != nil {
		writeFromError(w, err)
		return
	}
	s.actorAudit(r, sess, "config.sync", "Mail configuration", "Ran a manual configuration reconcile",
		map[string]any{"filesChanged": run["filesChanged"], "status": run["status"]})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": run})
}

func (s *Server) configDrift(ctx context.Context) []string {
	drift := []string{}
	var domainsWithoutDKIM, domainsWithoutPostmaster, mailboxesNotReady int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mail_domains WHERE COALESCE(dkim_record,'')=''`).Scan(&domainsWithoutDKIM)
	_ = s.db.QueryRow(ctx, `
		SELECT count(*) FROM public.mail_domains d
		WHERE NOT EXISTS (SELECT 1 FROM public.mailboxes m WHERE m.domain=d.domain AND m.local_part='postmaster' AND m.active)
	`).Scan(&domainsWithoutPostmaster)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE active AND provision_status <> 'ready'`).Scan(&mailboxesNotReady)
	if domainsWithoutDKIM > 0 {
		drift = append(drift, fmt.Sprintf("%d domains are missing DKIM signing material", domainsWithoutDKIM))
	}
	if domainsWithoutPostmaster > 0 {
		drift = append(drift, fmt.Sprintf("%d domains are missing the postmaster mailbox", domainsWithoutPostmaster))
	}
	if mailboxesNotReady > 0 {
		drift = append(drift, fmt.Sprintf("%d active mailboxes have not been provisioned", mailboxesNotReady))
	}
	if !s.aliasMapUsesControl(ctx) {
		drift = append(drift, "Postfix virtual alias map is not using postfix_alias_lookup")
	}
	if !s.postfixHasSuppressionMaps(ctx) {
		drift = append(drift, "Postfix suppression maps are not installed")
	}
	if !s.dkimSelectorMapPresent() {
		drift = append(drift, "Rspamd DKIM selector map is missing")
	}
	if len(drift) == 0 {
		drift = append(drift, "No drift detected")
	}
	return drift
}

func (s *Server) performConfigSync(ctx context.Context, actorID, trigger string) (map[string]any, error) {
	var runID string
	err := s.db.QueryRow(ctx, `
		INSERT INTO control.config_sync_runs (trigger, status, actor_id) VALUES ($1,'running',$2)
		RETURNING id::text
	`, trigger, nullIfEmpty(actorID)).Scan(&runID)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	details := map[string]any{}
	filesChanged := 0

	domains := 0
	rows, err := s.db.Query(ctx, `
		SELECT id::text, domain::text, tenant_id::text, COALESCE(dkim_selector,'meridian'), COALESCE(dkim_record,'')
		FROM public.mail_domains ORDER BY domain
	`)
	if err != nil {
		s.finishSyncFailure(ctx, runID, err)
		return nil, err
	}
	type domainRow struct{ id, domain, tenantID, selector, record string }
	domainRows := []domainRow{}
	for rows.Next() {
		var row domainRow
		if rows.Scan(&row.id, &row.domain, &row.tenantID, &row.selector, &row.record) == nil {
			domainRows = append(domainRows, row)
		}
	}
	rows.Close()
	for _, row := range domainRows {
		domains++
		if err := s.createPostmaster(ctx, row.tenantID, row.domain); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: postmaster provisioning failed: %v", row.domain, err))
		}
		if strings.TrimSpace(row.record) == "" {
			if _, err := s.generateDKIMKey(ctx, row.id, row.domain, row.selector, false); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: DKIM generation failed: %v", row.domain, err))
			}
		}
	}
	details["domains"] = domains

	mailboxes := 0
	rows, err = s.db.Query(ctx, `
		SELECT id::text, domain::text, local_part::text FROM public.mailboxes WHERE active ORDER BY domain, local_part
	`)
	if err != nil {
		s.finishSyncFailure(ctx, runID, err)
		return nil, err
	}
	type mailboxRow struct{ id, domain, local string }
	boxes := []mailboxRow{}
	for rows.Next() {
		var box mailboxRow
		if rows.Scan(&box.id, &box.domain, &box.local) == nil {
			boxes = append(boxes, box)
		}
	}
	rows.Close()
	for _, box := range boxes {
		path, err := s.ensureMaildir(box.domain, box.local)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s@%s: Maildir provisioning failed: %v", box.local, box.domain, err))
			_, _ = s.db.Exec(ctx, `UPDATE public.mailboxes SET provision_status='error', provision_attempts=provision_attempts+1, provision_error=$2 WHERE id=$1`, box.id, truncateText(err.Error(), 500))
			continue
		}
		_, err = s.db.Exec(ctx, `
			UPDATE public.mailboxes SET maildir_path=$2, provision_status='ready', provisioned_at=now(),
			       provision_attempts=provision_attempts+1, provision_error=''
			WHERE id=$1
		`, box.id, path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s@%s: could not update Maildir state: %v", box.local, box.domain, err))
		}
		mailboxes++
	}
	details["mailboxes"] = mailboxes

	changed, err := s.ensureAliasMapQuery(ctx)
	if err != nil {
		warnings = append(warnings, "Postfix alias map: "+err.Error())
	} else if changed {
		filesChanged++
	}
	changed, err = s.writeSuppressionMaps(ctx)
	if err != nil {
		warnings = append(warnings, "Suppression maps: "+err.Error())
	} else if changed {
		filesChanged++
	}
	changed, err = s.writeRelayConfig(ctx)
	if err != nil {
		warnings = append(warnings, "Relay configuration: "+err.Error())
	} else if changed {
		filesChanged++
	}
	changed, err = s.writeSenderTransport(ctx)
	if err != nil {
		warnings = append(warnings, "Sender transport: "+err.Error())
	} else if changed {
		filesChanged++
	}
	if changed, err := s.writeSelectorMap(ctx); err == nil {
		if changed {
			filesChanged++
		}
	} else {
		warnings = append(warnings, "DKIM selector map: "+err.Error())
	}
	if filesChanged > 0 {
		if _, err := runCommand(ctx, 20*time.Second, "postmap", filepath.Join(s.cfg.PostfixDir, "suppressions")); err != nil {
			warnings = append(warnings, "postmap suppressions failed")
		}
		if _, err := runCommand(ctx, 20*time.Second, "postmap", filepath.Join(s.cfg.PostfixDir, "suppressions_in")); err != nil {
			warnings = append(warnings, "postmap suppressions_in failed")
		}
		if _, err := os.Stat(filepath.Join(s.cfg.PostfixDir, "sasl_passwd")); err == nil {
			if _, err := runCommand(ctx, 20*time.Second, "postmap", filepath.Join(s.cfg.PostfixDir, "sasl_passwd")); err != nil {
				warnings = append(warnings, "postmap sasl_passwd failed")
			}
		}
		if _, err := runCommand(ctx, 20*time.Second, "postmap", filepath.Join(s.cfg.PostfixDir, "sender_transport")); err != nil {
			warnings = append(warnings, "postmap sender_transport failed")
		}
		if err := reloadPostfix(ctx); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	if policyChanged, err := s.syncPolicyServiceConfig(ctx); err != nil {
		warnings = append(warnings, "Policy service: "+err.Error())
	} else if policyChanged {
		filesChanged++
	}
	if result, err := runCommand(ctx, 20*time.Second, "postfix", "check"); err != nil {
		warnings = append(warnings, "postfix check: "+strings.TrimSpace(result.Stderr))
	}
	drift := s.configDrift(ctx)
	warningsJSON, _ := json.Marshal(warnings)
	driftJSON, _ := json.Marshal(drift)
	detailsJSON, _ := json.Marshal(details)
	_, err = s.db.Exec(ctx, `
		UPDATE control.config_sync_runs
		SET status=$2, finished_at=now(), files_changed=$3, warnings=$4::jsonb, drift=$5::jsonb,
		    details=$6::jsonb, error=''
		WHERE id=$1
	`, runID, map[bool]string{true: "ok", false: "warning"}[len(warnings) == 0], filesChanged, warningsJSON, driftJSON, detailsJSON)
	if err != nil {
		return nil, err
	}
	s.hub.Broadcast("config.sync", map[string]any{"runId": runID, "filesChanged": filesChanged, "warnings": len(warnings)})
	latest, _ := s.latestConfigSync(ctx)
	return latest, nil
}

func (s *Server) finishSyncFailure(ctx context.Context, runID string, cause error) {
	_, _ = s.db.Exec(ctx, `UPDATE control.config_sync_runs SET status='error', finished_at=now(), error=$2 WHERE id=$1`, runID, truncateText(cause.Error(), 1000))
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Server) aliasMapUsesControl(ctx context.Context) bool {
	raw, err := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "pgsql-virtual-alias.cf"))
	return err == nil && strings.Contains(string(raw), "postfix_alias_lookup")
}

func (s *Server) postfixHasSuppressionMaps(ctx context.Context) bool {
	raw, err := os.ReadFile(filepath.Join(s.cfg.PostfixDir, "main.cf"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "suppressions_in") && strings.Contains(string(raw), "suppressions")
}

func (s *Server) dkimSelectorMapPresent() bool {
	_, err := os.Stat(filepath.Join(s.cfg.DKIMDir, "selectors.map"))
	return err == nil
}

func (s *Server) ensureAliasMapQuery(ctx context.Context) (bool, error) {
	path := filepath.Join(s.cfg.PostfixDir, "pgsql-virtual-alias.cf")
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(raw), "\n")
	changed := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "query =") {
			next := "query = SELECT public.postfix_alias_lookup('%s') AS target"
			if line != next {
				lines[i] = next
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	gid := -1
	if group, groupErr := user.LookupGroup("postfix"); groupErr == nil {
		gid, _ = strconv.Atoi(group.Gid)
	}
	_, err = writeManagedFile(path, strings.Join(lines, "\n"), 0o640, 0, gid)
	return changed, err
}

func (s *Server) handleConfigImportPreview(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	items, err := s.maildirImportPreview(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) maildirImportPreview(ctx context.Context) ([]map[string]any, error) {
	entries := []map[string]any{}
	domains, err := os.ReadDir(s.cfg.VmailRoot)
	if err != nil {
		return entries, err
	}
	for _, domainEntry := range domains {
		if !domainEntry.IsDir() {
			continue
		}
		domain := normalizeDomain(domainEntry.Name())
		if !validDomain(domain) {
			continue
		}
		users, err := os.ReadDir(filepath.Join(s.cfg.VmailRoot, domain))
		if err != nil {
			continue
		}
		for _, userEntry := range users {
			if !userEntry.IsDir() {
				continue
			}
			local := normalizeLocal(userEntry.Name())
			if !validLocalPart(local) {
				continue
			}
			maildir := filepath.Join(s.cfg.VmailRoot, domain, local, "Maildir")
			info, err := os.Stat(maildir)
			if err != nil || !info.IsDir() {
				continue
			}
			count := countMaildirMessages(maildir)
			email := local + "@" + domain
			var existing int
			_ = s.db.QueryRow(ctx, `SELECT count(*) FROM public.mailboxes WHERE email=$1`, email).Scan(&existing)
			entries = append(entries, map[string]any{
				"email": email, "domain": domain, "localPart": local, "path": maildir,
				"messages": count, "exists": existing > 0,
			})
			if len(entries) >= 5000 {
				return entries, nil
			}
		}
	}
	return entries, nil
}

func countMaildirMessages(maildir string) int {
	count := 0
	for _, dir := range []string{"cur", "new"} {
		if entries, err := os.ReadDir(filepath.Join(maildir, dir)); err == nil {
			count += len(entries)
		}
	}
	subdirs, _ := filepath.Glob(filepath.Join(maildir, ".*", "cur"))
	for _, dir := range subdirs {
		if entries, err := os.ReadDir(dir); err == nil {
			count += len(entries)
		}
	}
	subdirs, _ = filepath.Glob(filepath.Join(maildir, ".*", "new"))
	for _, dir := range subdirs {
		if entries, err := os.ReadDir(dir); err == nil {
			count += len(entries)
		}
	}
	return count
}

func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Emails []string `json:"emails"`
	}
	_ = decodeJSON(w, r, &body, 2<<20)
	requested := map[string]bool{}
	for _, email := range body.Emails {
		requested[strings.ToLower(strings.TrimSpace(email))] = true
	}
	items, err := s.maildirImportPreview(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	created := []string{}
	warnings := []string{}
	for _, item := range items {
		email := fmt.Sprint(item["email"])
		if item["exists"] == true || (len(requested) > 0 && !requested[email]) {
			continue
		}
		domain := fmt.Sprint(item["domain"])
		local := fmt.Sprint(item["localPart"])
		var tenantID string
		err := s.db.QueryRow(r.Context(), `SELECT tenant_id::text FROM public.mail_domains WHERE domain=$1`, domain).Scan(&tenantID)
		if err != nil {
			warnings = append(warnings, email+": domain is not managed by the panel")
			continue
		}
		if err := s.importShellMailbox(r.Context(), tenantID, domain, local); err != nil {
			warnings = append(warnings, email+": "+err.Error())
			continue
		}
		created = append(created, email)
	}
	s.actorAudit(r, sess, "config.import_mailboxes", "Server preview", fmt.Sprintf("Imported %d shell-created mailboxes", len(created)),
		map[string]any{"created": created, "warnings": warnings})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "warnings": warnings})
}

func (s *Server) importShellMailbox(ctx context.Context, tenantID, domain, local string) error {
	email := local + "@" + domain
	random, err := randomPassword(28)
	if err != nil {
		return err
	}
	webHash, _ := auth.HashPassword(random)
	mailHash, err := hashMailPassword(random)
	if err != nil {
		return err
	}
	maildir, err := s.ensureMaildir(domain, local)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM public.users WHERE email=$1`, email).Scan(&userID)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `
			INSERT INTO public.users (tenant_id,email,name,initials,role,status,password_hash,mail_password_hash,password_changed_at)
			VALUES ($1,$2,$3,$4,'member','active',$5,$6,now()) RETURNING id::text
		`, tenantID, email, titleWords(strings.ReplaceAll(local, ".", " ")), strings.ToUpper(local[:minInt(2, len(local))]), webHash, mailHash).Scan(&userID)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO public.mailboxes (tenant_id,user_id,email,domain,local_part,maildir_path,active,provision_status,provisioned_at)
		VALUES ($1,$2,$3,$4,$5,$6,true,'ready',now())
		ON CONFLICT (email) DO UPDATE SET maildir_path=EXCLUDED.maildir_path, active=true, provision_status='ready', provisioned_at=now()
	`, tenantID, userID, email, domain, local, maildir)
	if err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `
		INSERT INTO public.user_aliases (tenant_id,user_id,email,label,kind,can_send,active)
		VALUES ($1,$2,$3,'Primary','primary',true,true) ON CONFLICT (email) DO NOTHING
	`, tenantID, userID, email)
	return tx.Commit(ctx)
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func titleWords(value string) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
