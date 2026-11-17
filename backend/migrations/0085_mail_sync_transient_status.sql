-- Older builds treated any IMAP "login failed" as a reconnect requirement.
-- Gmail's "Temporary system problem" is a retryable provider incident, not a
-- credential failure; move those accounts back into the automatic sync worker.
BEGIN;

UPDATE public.mail_accounts
SET status = 'error', last_error = '', updated_at = now()
WHERE status = 'reauth'
  AND (
    last_error ILIKE '%temporary system problem%'
    OR last_error ILIKE '%temporarily unavailable%'
    OR last_error ILIKE '%temporary failure%'
    OR last_error ILIKE '%try again later%'
  );

INSERT INTO schema_migrations (version) VALUES ('0085_mail_sync_transient_status')
ON CONFLICT DO NOTHING;

COMMIT;
