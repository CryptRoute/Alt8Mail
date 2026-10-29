-- DKIM public material generated from the Rspamd signing key directory.
BEGIN;

ALTER TABLE mail_domains
  ADD COLUMN IF NOT EXISTS dkim_public_key text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS dkim_record text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS dkim_key_created_at timestamptz,
  ADD COLUMN IF NOT EXISTS dkim_key_error text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS mail_domains_dkim_selector_idx
  ON mail_domains (domain, dkim_selector);

-- Domains created before the ownership-token column existed need a token so
-- the DNS verification screen can publish and validate the ownership record.
UPDATE mail_domains
SET verification_token = encode(gen_random_bytes(18), 'hex')
WHERE COALESCE(verification_token, '') = '';

GRANT SELECT, INSERT, UPDATE, DELETE ON mail_domains TO meridian_app;
GRANT SELECT ON mail_domains TO meridian_mail;

INSERT INTO schema_migrations (version) VALUES ('0006_dkim_keys')
ON CONFLICT (version) DO NOTHING;

COMMIT;
