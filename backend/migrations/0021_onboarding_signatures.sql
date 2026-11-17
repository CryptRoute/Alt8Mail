-- First-login onboarding, named signatures and per-identity signatures.
--   * onboarding_required keeps a new mailbox inside the guided setup flow
--     until profile, regional preferences and security are confirmed.
--   * mail_signatures stores the named signature library used by Settings,
--     compose and the automatic New/Reply/Forward defaults.
--   * mail_identity_signatures lets every sending identity (primary, alias or
--     personal identity) carry its own plain-text and rich-HTML signature.
BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarding_required boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarding_completed_at timestamptz;

-- Older mailboxes were provisioned with the internal labels "Primary" and
-- "Alias", which leaked into the From line as "Primary <user@domain>". The
-- account holder's real display name is the correct sender name.
UPDATE user_aliases ua
SET label = u.name
FROM users u
WHERE ua.user_id = u.id
  AND ua.kind IN ('primary', 'alias')
  AND (trim(ua.label) = '' OR lower(ua.label) IN ('primary', 'alias'));

CREATE TABLE IF NOT EXISTS mail_signatures (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  default_new boolean NOT NULL DEFAULT false,
  default_reply boolean NOT NULL DEFAULT false,
  default_forward boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mail_signatures_user_idx ON mail_signatures (user_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS mail_signatures_default_new_idx ON mail_signatures (user_id) WHERE default_new;
CREATE UNIQUE INDEX IF NOT EXISTS mail_signatures_default_reply_idx ON mail_signatures (user_id) WHERE default_reply;
CREATE UNIQUE INDEX IF NOT EXISTS mail_signatures_default_forward_idx ON mail_signatures (user_id) WHERE default_forward;

-- Preserve the single legacy signature preference as the first named signature.
-- Older rows can hold the value in either users.preferences or the app_states
-- workspace snapshot; take whichever is populated.
INSERT INTO mail_signatures (id, tenant_id, user_id, name, body_text, body_html, default_new)
SELECT 'sig_' || replace(gen_random_uuid()::text, '-', ''), u.tenant_id, u.id, 'Default signature',
       legacy.signature, '', true
FROM users u
LEFT JOIN app_states aps ON aps.user_id = u.id
CROSS JOIN LATERAL (
  SELECT COALESCE(
    NULLIF(trim(u.preferences->>'signature'), ''),
    NULLIF(trim(aps.state->'user'->'preferences'->>'signature'), '')
  ) AS signature
) legacy
WHERE legacy.signature IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM mail_signatures s WHERE s.user_id = u.id);

CREATE TABLE IF NOT EXISTS mail_identity_signatures (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, email)
);

ALTER TABLE mail_identities ADD COLUMN IF NOT EXISTS signature_html text NOT NULL DEFAULT '';

INSERT INTO mail_identity_signatures (tenant_id, user_id, email, body_text, body_html)
SELECT tenant_id, user_id, email, signature, ''
FROM mail_identities
WHERE trim(signature) <> ''
ON CONFLICT (user_id, email) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON mail_signatures TO a8m_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mail_identity_signatures TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0021_onboarding_signatures')
ON CONFLICT (version) DO NOTHING;

COMMIT;
