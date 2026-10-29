-- Real second-factor enrollment and session-scoped settings.
BEGIN;

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS enrollment_required boolean NOT NULL DEFAULT false;

ALTER TABLE auth_challenges
  ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'two_factor';

CREATE INDEX IF NOT EXISTS auth_challenges_user_kind_idx
  ON auth_challenges (user_id, kind);

INSERT INTO schema_migrations (version) VALUES ('0003_real_auth')
ON CONFLICT (version) DO NOTHING;

COMMIT;
