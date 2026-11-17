-- Cabinet folders, storage quota policy, shared calendars, address books and
-- calendar collaboration. Everything here is additive so existing mail,
-- Cabinet files, DAV collections and calendar events keep working.
BEGIN;

-- ---------------------------------------------------------------------------
-- Cabinet folders
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS file_folders (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  parent_id text REFERENCES file_folders(id) ON DELETE SET NULL,
  name text NOT NULL,
  color text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_folders_user_idx ON file_folders (user_id, parent_id, lower(name));

ALTER TABLE files ADD COLUMN IF NOT EXISTS folder_id text REFERENCES file_folders(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS files_folder_idx ON files (user_id, folder_id, updated_at DESC);

-- ---------------------------------------------------------------------------
-- Storage policy: one combined quota by default, optional split quotas at the
-- organization, domain and individual user level. User overrides win over the
-- domain policy, which wins over the organization default.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS storage_policy_defaults (
  tenant_id uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  mode text NOT NULL DEFAULT 'combined' CHECK (mode IN ('combined', 'split')),
  email_quota_bytes bigint NOT NULL DEFAULT 0,
  storage_quota_bytes bigint NOT NULL DEFAULT 0,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS storage_policies (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scope text NOT NULL CHECK (scope IN ('domain', 'user')),
  domain citext NOT NULL DEFAULT '',
  user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  mode text NOT NULL DEFAULT 'combined' CHECK (mode IN ('combined', 'split')),
  email_quota_bytes bigint NOT NULL DEFAULT 0,
  storage_quota_bytes bigint NOT NULL DEFAULT 0,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((scope = 'domain' AND user_id IS NULL AND domain <> '')
      OR (scope = 'user' AND user_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS storage_policies_domain_uidx
  ON storage_policies (tenant_id, domain) WHERE scope = 'domain';
CREATE UNIQUE INDEX IF NOT EXISTS storage_policies_user_uidx
  ON storage_policies (tenant_id, user_id) WHERE scope = 'user';

INSERT INTO storage_policy_defaults (tenant_id, mode)
SELECT id, 'combined' FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Calendars, sharing, publications and subscriptions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS calendars (
  id text NOT NULL,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  color text NOT NULL DEFAULT '#0f6cbd',
  kind text NOT NULL DEFAULT 'personal' CHECK (kind IN ('personal', 'shared', 'holiday', 'birthday', 'subscription')),
  source_url text NOT NULL DEFAULT '',
  source_key text NOT NULL DEFAULT '',
  visible boolean NOT NULL DEFAULT true,
  is_default boolean NOT NULL DEFAULT false,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS calendars_tenant_idx ON calendars (tenant_id, user_id);

CREATE TABLE IF NOT EXISTS calendar_shares (
  calendar_id text NOT NULL,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  grantee_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  grantee_email citext NOT NULL,
  access text NOT NULL DEFAULT 'view' CHECK (access IN ('view', 'edit')),
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (calendar_id, owner_user_id, grantee_user_id)
);
CREATE INDEX IF NOT EXISTS calendar_shares_grantee_idx ON calendar_shares (grantee_user_id, owner_user_id);

CREATE TABLE IF NOT EXISTS calendar_publications (
  calendar_id text NOT NULL,
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  token_enc text NOT NULL,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (calendar_id, owner_user_id)
);

CREATE TABLE IF NOT EXISTS calendar_subscriptions (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id text NOT NULL,
  url text NOT NULL,
  name text NOT NULL DEFAULT '',
  color text NOT NULL DEFAULT '#0f6cbd',
  last_sync_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  etag text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS calendar_subscriptions_user_idx ON calendar_subscriptions (user_id, created_at);

ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS sequence integer NOT NULL DEFAULT 0;
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS transparency text NOT NULL DEFAULT 'busy';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS reminder_minutes integer NOT NULL DEFAULT 15;
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS attachments jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS meeting_url text NOT NULL DEFAULT '';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS organizer_name text NOT NULL DEFAULT '';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS response_requested boolean NOT NULL DEFAULT true;
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS source_id text NOT NULL DEFAULT '';
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS source_uid text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS calendar_events_calendar_idx ON calendar_events (user_id, calendar_id, starts_at);
CREATE INDEX IF NOT EXISTS calendar_events_source_idx ON calendar_events (user_id, source_id) WHERE source_id <> '';

ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS user_id uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS organizer boolean NOT NULL DEFAULT false;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS responded_at timestamptz;
ALTER TABLE calendar_attendees ADD COLUMN IF NOT EXISTS note text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS calendar_attendees_email_idx ON calendar_attendees (email);

-- ---------------------------------------------------------------------------
-- Address books (multiple local books; CardDAV maps to the default book)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS address_books (
  id text NOT NULL,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  color text NOT NULL DEFAULT '#3d6b62',
  is_default boolean NOT NULL DEFAULT false,
  source text NOT NULL DEFAULT 'local',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, id)
);

ALTER TABLE contacts ADD COLUMN IF NOT EXISTS address_book_id text NOT NULL DEFAULT 'default';
ALTER TABLE contacts ADD COLUMN IF NOT EXISTS birthday date;
CREATE INDEX IF NOT EXISTS contacts_book_idx ON contacts (user_id, address_book_id, name);

-- Give every existing user a default calendar and address book. The calendar
-- keeps the historical collection id "default" so CardDAV/CalDAV URLs remain
-- stable for clients that are already configured.
INSERT INTO calendars (id, tenant_id, user_id, name, color, kind, visible, is_default)
SELECT 'default', u.tenant_id, u.id, 'Calendar', '#0f6cbd', 'personal', true, true
FROM users u
WHERE u.deleted_at IS NULL
ON CONFLICT (user_id, id) DO NOTHING;

INSERT INTO address_books (id, tenant_id, user_id, name, color, is_default)
SELECT 'default', u.tenant_id, u.id, 'Contacts', '#3d6b62', true
FROM users u
WHERE u.deleted_at IS NULL
ON CONFLICT (user_id, id) DO NOTHING;

UPDATE calendar_events SET calendar_id = 'default' WHERE calendar_id = '';

GRANT SELECT, INSERT, UPDATE, DELETE ON file_folders, storage_policy_defaults, storage_policies,
  calendars, calendar_shares, calendar_publications, calendar_subscriptions, address_books TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0029_cabinet_calendar_enterprise')
ON CONFLICT (version) DO NOTHING;

COMMIT;
