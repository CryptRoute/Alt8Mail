-- Sending identities can now point at a named signature from the signature
-- library instead of holding a one-off body. The per-address override row
-- also stores the From display name and reply-to for organization aliases,
-- so the primary account and managed aliases can be personalised without
-- editing the shared alias record.
BEGIN;

ALTER TABLE mail_identity_signatures ADD COLUMN IF NOT EXISTS signature_id text NOT NULL DEFAULT '';
ALTER TABLE mail_identity_signatures ADD COLUMN IF NOT EXISTS display_name text NOT NULL DEFAULT '';
ALTER TABLE mail_identity_signatures ADD COLUMN IF NOT EXISTS reply_to citext;

ALTER TABLE mail_identities ADD COLUMN IF NOT EXISTS signature_id text NOT NULL DEFAULT '';

INSERT INTO schema_migrations (version) VALUES ('0086_identity_signature_choices')
ON CONFLICT (version) DO NOTHING;

COMMIT;
