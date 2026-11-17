# Backup Restore and Disaster Recovery

## What the Control Plane backs up

A completed archive can include the database dump, Postfix/Dovecot/DKIM/TLS
configuration and Maildirs. The default set now also includes webmail attachment
uploads (`uploads/`). Older archives may lack uploads; a mailbox restore still
works because restored messages are re-indexed from their MIME content.

## Full restore vs mailbox restore

- **Restore archive** (Backups → restore icon) replaces selected components. It
  uses `pg_restore --clean` and copies managed configuration; treat it as a
  disaster-recovery operation, not a per-user fix.
- **Restore one mailbox or folder** (Backups → people icon) is additive. It
  resolves the live tenant/user/mailbox by email, derives the Maildir path from
  the database (never from a request path), extracts the selected folder from
  the archive into a staging Maildir, and runs `doveadm import` into the live
  folder. Existing messages/files are preserved; duplicates are skipped. The
  webmail indexes imported messages on its next mailbox sync.

The request may include a tenant ID; when present it must match the live
mailbox tenant. Folder names map to Maildir folders: Inbox, Sent, Drafts,
Archive, Junk/Spam and Trash; other safe names map to their own folder.

## Restore to yesterday

1. Choose a completed archive created after the desired point in time.
2. Open **Restore one mailbox or folder**.
3. Enter the mailbox email, folder, and `Since`/`Until` dates (for “yesterday”,
   set both to yesterday's date).
4. Click **Preview**. Confirm the message count, conflicts and archive range.
5. Take a fresh backup if possible, then confirm **Restore mailbox**.
6. Watch the job toast/status. A completed job records the backup, folder,
   counts, conflicts and staging result in `control.restore_jobs` and the audit
   log (`backup.restore_previewed`, `backup.restore_started`,
   `backup.mailbox_restored`).
7. Sign in to the mailbox, verify the restored messages, and confirm another
   tenant was not changed.

## Safety properties

- Preview does not modify live data or the live mailbox.
- Archive entries are cleaned, restricted to the selected Maildir folder,
  regular files only, and staged under the managed Maildir root; symlinks and
  path traversal are rejected.
- A backup must be `completed` and its path must stay under
  `CONTROL_BACKUP_DIR`.
- The live mailbox is resolved from PostgreSQL; a request cannot address an
  arbitrary path or another tenant's mailbox.
- Additive import never deletes or overwrites live messages. Missing/failed
  imports leave the job `failed` and do not report success.

## Quarterly drill

1. Restore a disposable copy of the database and Maildirs (or use staging).
2. Preview a mailbox/folder from yesterday's archive.
3. Apply the restore, verify counts and audit evidence.
4. Verify a second tenant and a deleted message were unaffected.
5. Record RPO/RTO, archive integrity, and any operator steps that needed
   adjustment. Keep the drill record with the backup inventory.
6. Re-run the drill after changing backup contents, Dovecot layout, or the
   restore code.

## Limits

No WAL/point-in-time continuous recovery, cross-installation restore, per-mailbox
configuration/TLS restore, deletion during restore, or scheduled automatic
restores. A backup stored only on the same host is not disaster recovery; copy
archives off-host.
