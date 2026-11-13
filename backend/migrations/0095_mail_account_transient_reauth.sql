-- Connected-account IMAP logins used to classify any dropped connection as a
-- credential problem, which parked the account in 'reauth' and stopped the
-- sync worker from retrying. Move those accounts back into the retry pool;
-- genuine invalid_grant / invalid credentials rows keep their status.
BEGIN;

UPDATE public.mail_accounts
SET status = 'error', updated_at = now()
WHERE kind = 'external'
  AND status = 'reauth'
  AND (
    lower(COALESCE(last_error, '')) LIKE '%connection closed%'
    OR lower(COALESCE(last_error, '')) LIKE '%connection reset%'
    OR lower(COALESCE(last_error, '')) LIKE '%temporary system problem%'
    OR lower(COALESCE(last_error, '')) LIKE '%temporarily unavailable%'
    OR lower(COALESCE(last_error, '')) LIKE '%try again later%'
    OR lower(COALESCE(last_error, '')) LIKE '%timed out%'
    OR lower(COALESCE(last_error, '')) LIKE '%timeout%'
    OR lower(COALESCE(last_error, '')) LIKE '%service unavailable%'
    OR lower(COALESCE(last_error, '')) LIKE '%rate limit%'
    OR lower(COALESCE(last_error, '')) LIKE '%too many%'
    OR lower(COALESCE(last_error, '')) LIKE '%unexpected eof%'
  );

INSERT INTO schema_migrations (version) VALUES ('0095_mail_account_transient_reauth') ON CONFLICT DO NOTHING;

COMMIT;
