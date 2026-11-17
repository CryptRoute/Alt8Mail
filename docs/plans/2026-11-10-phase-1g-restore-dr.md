# Phase 1G — Per-Mailbox/Per-Folder Restore and DR Drills

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development`. Steps use checkbox syntax.

**Goal:** Restore one mailbox or one folder from an existing full backup into the live system, with a preview/dry-run, additive conflict handling, cross-tenant protection, audit evidence and progress UI, plus a documented quarterly DR drill.

**Architecture:** Reuse the existing Control Plane backup archives (`database/a8m.dump`, `mail/vmail`, config) and add the webmail upload directory to the archive contents. A new restore service indexes the archive for the selected mailbox/folder, restores the dump into a disposable database for preview/copy, and applies only the selected rows/files additively. DB rows are copied through a staging table with `ON CONFLICT DO NOTHING`; Maildir and attachment files are merged with no overwrite. Jobs and audit records make the operation observable; the Control web UI drives preview → confirm → progress.

**Tech Stack:** Go, PostgreSQL/pgx, `tar/gzip`, `pg_restore`/`psql`, Control Plane web UI, existing backup/audit helpers.

**Spec:** `docs/superpowers/plans/2026-11-09-alt8mail-paid-platform-roadmap.md` §4 Phase 1 (Backup/restore and DR).

## Global Constraints

- Additive restore only: never delete or overwrite live messages, Maildir files, attachment bytes, users or settings. Conflicting rows/files are skipped and counted.
- Tenant isolation: tenant, user and live mailbox are resolved from the live database by IDs/email from the authenticated Control session; archive paths are derived from the live mailbox record (`domain`/`local_part`), never from user-supplied paths. Any mismatch aborts the job.
- No path traversal: every tar entry is cleaned and must remain under the staging root; symlinks are not restored; copy only regular files.
- Preview is side-effect free in the live system; it may create a disposable database/temp directory that is always removed.
- Backups must be `completed`, inside the configured backup directory, and contain both `database` and `maildirs` for a mailbox restore. Uploads are restored only when the archive contains them; missing attachment bytes are counted, not synthesized.
- Audit `backup.restore_previewed` and `backup.mailbox_restored` with tenant/email/folder/counts (no message content, no credentials). Job records never contain message bodies.
- Control permission `backups.manage` for preview/apply; `backups.read` for job status. CSRF applies.
- A restore into a live mailbox is a sensitive operation: the UI must require an explicit confirmation, show the preview counts, and warn that a snapshot/backup should be taken first.
- No release, deploy or push.

## Review Focus

- Cross-tenant/path abuse: a crafted request cannot restore another tenant's mailbox, escape the archive/staging directory, or address a live path not derived from the DB.
- Data safety: additive-only behavior; interrupted jobs leave no partial DB transaction; staging/temp databases are cleaned; live messages/files survive conflicts.
- Correct DB/file consistency: restored messages have valid mailbox/user/tenant IDs, attachment rows point to restored bytes, Maildir folder mapping matches app folders, and quota/sum views remain sane.
- Audit/observability: preview and apply are distinguishable, job status is accurate, failures are fixed/sanitized, and no message content leaks.
- Operational usability: a mailbox can be restored to “yesterday” with a preview and audit evidence in staging, and the DR runbook is executable by an operator.

Each focus line has tests in the task that owns the code.

---

### Task 1: Archive contents, restore schema and indexing helpers

**Files:**
- Modify: `backend/internal/control/backups.go`
- Modify: `backend/internal/control/config.go`
- Create: `backend/migrations/0063_restore_jobs.sql`
- Create: `backend/migrations/rollback/0063_restore_jobs_down.sql`
- Create: `backend/internal/control/restore.go`
- Create: `backend/internal/control/restore_test.go`

**Interfaces:**
- Produces config `UploadDir string` (`CONTROL_UPLOAD_DIR`; default resolved from `ALT8MAIL_UPLOAD_DIR` in `AppEnvFile`, else `/opt/a8m/uploads`).
- Produces backup content `uploads` (“Webmail attachment uploads”) in `backupContentNames`, included in the default backup set, archived under `uploads/`.
- Produces `type mailboxRestoreRequest struct { TenantID, Email, Folder, Since, Until string }`, `type mailboxRestorePreview struct { Messages, Attachments, MaildirFiles, Conflicts, Folders []…, Earliest, Latest }`.
- Produces `(s *Server) restoreBackupPath(id string) (string, error)`, `(s *Server) previewMailboxRestore(ctx, backupID string, req mailboxRestoreRequest) (mailboxRestorePreview, error)`, `(s *Server) applyMailboxRestore(ctx, jobID string, req mailboxRestoreRequest) error`.
- Job table:

```sql
CREATE TABLE IF NOT EXISTS control.restore_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_id uuid NOT NULL REFERENCES control.backups(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL,
  user_id uuid NOT NULL,
  email citext NOT NULL,
  folder text NOT NULL DEFAULT '',
  mode text NOT NULL CHECK (mode IN ('preview','apply')),
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed')),
  preview jsonb NOT NULL DEFAULT '{}'::jsonb,
  result jsonb NOT NULL DEFAULT '{}'::jsonb,
  detail text NOT NULL DEFAULT '',
  created_by uuid REFERENCES control.admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
GRANT SELECT, INSERT, UPDATE, DELETE ON control.restore_jobs TO alt8mail_control;
INSERT INTO schema_migrations (version) VALUES ('0063_restore_jobs') ON CONFLICT DO NOTHING;
```

- [ ] Add `uploads` to backups and the default content set; include it in archive size/detail; document that old archives lack uploads.
- [ ] Implement safe tar index scanning for a selected mailbox: normalize the live mailbox to `<domain>/<local>/Maildir`, map app folders to Maildir folders (`inbox`→root, `sent`→`.Sent`, `drafts`→`.Drafts`, `trash`→`.Trash`, `spam`→`.Junk`, others→`.<Folder>`), and count regular files/time ranges without extracting everything.
- [ ] Implement `restoreBackupPath` checks (completed, path under BackupDir, regular file) and a staging-root/symlink guard helper.
- [ ] Unit tests: config upload-dir resolution; backup content includes uploads; path/folder mapping; tar traversal/symlink rejection; folder selection counts; empty folder/range.
- [ ] Migration apply/rollback on disposable PostgreSQL.

### Task 2: Preview and apply service

**Files:**
- Create: `backend/internal/control/restore_apply.go`
- Create: `backend/internal/control/restore_apply_integration_test.go`
- Modify: `backend/internal/control/restore.go`

- [ ] Preview: resolve the live tenant/user/mailbox; scan Maildir and uploads entries; extract `database/a8m.dump`; create a disposable DB (`a8m_restore_<job>`), `pg_restore --no-owner --no-privileges`, and query `mail_messages` for `user_id`, folder, and date range; count attachment rows/files; return earliest/latest and conflicts against live IDs; always drop the temp DB and remove temp dirs.
- [ ] Apply: create a `preview`/`apply` job row; repeat the preview; create staging tables `restore_mail_messages`/`restore_attachments` in the live DB; `COPY` selected rows from the temp DB via `psql \copy` or a pgx copy loop; `INSERT ... SELECT ... ON CONFLICT (id) DO NOTHING`; copy attachment bytes with `os.OpenFile(O_CREATE|O_EXCL)`; merge Maildir files with `O_EXCL`; count inserted/skipped; update `mailboxes`/user usage only if the existing code requires it; mark job completed/failed; audit.
- [ ] Never insert rows whose tenant/user/mailbox does not match the resolved live mailbox; map `mailbox_id` to the live mailbox ID for native mail; preserve original folder/thread/subject/body/flags.
- [ ] Integration tests (DSN + generated tar.gz and pg_dump): restore one folder from a seeded backup, verify only that folder/mailbox changed, existing messages/files unchanged, conflicts skipped, attachments readable, audit row, job completed; restore “yesterday” range; cross-tenant/foreign path fails; failed job leaves no live rows; temp DB cleaned.
- [ ] Run focused tests with/without DSN, `go test ./...`, vet, build.

### Task 3: Control API and UI

**Files:**
- Create: `backend/internal/control/restore_api.go`
- Modify: `backend/internal/control/backups.go` (routes/registration)
- Modify: `backend/internal/control/web/views-*.js` (backup/restore view)
- Modify: `backend/internal/control/web/app.js` if a new route is needed
- Create: `docs/DR.md`

- [ ] Routes: `POST /control/api/backups/{id}/mailbox-restore/preview`, `POST /control/api/backups/{id}/mailbox-restore`, `GET /control/api/restore-jobs/{id}`, all permission/CSRF-scoped; body caps; fixed errors.
- [ ] UI: “Restore mailbox” action on completed backups; tenant/user/folder/date form; preview counts/conflicts; explicit confirm; job progress/result; never render message bodies; accessible labels and keyboard flow.
- [ ] docs/DR.md: full restore vs per-mailbox restore, prerequisites, snapshot-before-restore warning, RPO/RTO expectations, verification steps, audit evidence, quarterly drill checklist, rollback.
- [ ] Tests: API validation/permissions/cross-tenant; UI smoke with a fixture archive when available; job status transitions.

### Task 4: Security and DR gate

- [ ] Adversarial review: path traversal/symlinks, cross-tenant lookup, archive spoofing, SQL/copy injection, temp DB cleanup, interrupted apply, audit secrecy, permission/CSRF.
- [ ] Execute a staged drill on a disposable PostgreSQL + archive: backup → preview yesterday → restore one folder → verify counts/audit/unchanged other tenants → rerun idempotently.
- [ ] Run full Go tests/vet/build and `npm run test:all`; fix findings with one scoped re-review.

## Out of scope

- Point-in-time continuous recovery/WAL, cross-installation restore, restoring config/TLS/DKIM per mailbox, deleting messages as part of restore, restoring files/calendar/tasks independently (mailbox mail + attachments only), and automated restore scheduling.
