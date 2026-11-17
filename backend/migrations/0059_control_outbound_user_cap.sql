-- Per-mailbox rolling outbound safety cap enforced by the Postfix policy
-- daemon. It applies even when per-domain rolling caps are disabled, and the
-- default matches the webmail's advertised 200-message daily limit.
BEGIN;

ALTER TABLE control.domain_settings
  ADD COLUMN IF NOT EXISTS user_daily_cap integer NOT NULL DEFAULT 200
  CHECK (user_daily_cap BETWEEN 0 AND 1000000);

COMMIT;
