-- Normalized Tasks, Files, Calendar, Contacts, storage accounts and guest shares.
BEGIN;

-- The early platform schema contained unused normalized tables with a narrower
-- shape. They were never written by the Go API (tasks/files/calendar lived in
-- app_states), so replace them with the workspace model while preserving the
-- migration backfill below.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0007_workspace') THEN
    DROP TABLE IF EXISTS file_shares CASCADE;
    DROP TABLE IF EXISTS files CASCADE;
    DROP TABLE IF EXISTS calendar_events CASCADE;
    DROP TABLE IF EXISTS contacts CASCADE;
    DROP TABLE IF EXISTS storage_providers CASCADE;
    DROP TABLE IF EXISTS tasks CASCADE;
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS tasks (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title text NOT NULL,
  notes text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  due_date date,
  due_time time,
  assignee_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  linked_thread_id text,
  linked_subject text,
  source text NOT NULL DEFAULT 'manual',
  tags text[] NOT NULL DEFAULT '{}',
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tasks_user_idx ON tasks (user_id, status, due_date);
CREATE INDEX IF NOT EXISTS tasks_assignee_idx ON tasks (assignee_user_id, status);

CREATE TABLE IF NOT EXISTS files (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  external_id text NOT NULL DEFAULT '',
  name text NOT NULL,
  mime text NOT NULL DEFAULT 'application/octet-stream',
  path text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  size_bytes bigint NOT NULL DEFAULT 0,
  storage_key text NOT NULL DEFAULT '',
  attachment_id text,
  external_url text NOT NULL DEFAULT '',
  starred boolean NOT NULL DEFAULT false,
  locked boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1,
  previewable boolean NOT NULL DEFAULT false,
  checksum text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS files_user_idx ON files (user_id, provider_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS files_external_uidx
  ON files (user_id, provider_id, external_id) WHERE external_id <> '';

CREATE TABLE IF NOT EXISTS file_shares (
  id text PRIMARY KEY,
  file_id text NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  access text NOT NULL DEFAULT 'view' CHECK (access IN ('view', 'edit')),
  expires_at timestamptz NOT NULL,
  recipient_email citext NOT NULL DEFAULT '',
  allowed_domain citext NOT NULL DEFAULT '',
  revoked_at timestamptz,
  access_count integer NOT NULL DEFAULT 0,
  last_accessed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS file_shares_file_idx ON file_shares (file_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS calendar_events (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id text NOT NULL DEFAULT 'default',
  uid text NOT NULL,
  title text NOT NULL,
  description text NOT NULL DEFAULT '',
  location text NOT NULL DEFAULT '',
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  all_day boolean NOT NULL DEFAULT false,
  timezone text NOT NULL DEFAULT 'UTC',
  status text NOT NULL DEFAULT 'confirmed',
  visibility text NOT NULL DEFAULT 'default',
  recurrence_rule text NOT NULL DEFAULT '',
  organizer_email citext NOT NULL DEFAULT '',
  etag text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, calendar_id, uid)
);
CREATE INDEX IF NOT EXISTS calendar_user_idx ON calendar_events (user_id, starts_at);

CREATE TABLE IF NOT EXISTS calendar_attendees (
  event_id text NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  email citext NOT NULL,
  name text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'needs-action',
  PRIMARY KEY (event_id, email)
);

CREATE TABLE IF NOT EXISTS contacts (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email citext NOT NULL,
  name text NOT NULL DEFAULT '',
  company text NOT NULL DEFAULT '',
  title text NOT NULL DEFAULT '',
  phone text NOT NULL DEFAULT '',
  location text NOT NULL DEFAULT '',
  notes text NOT NULL DEFAULT '',
  source text NOT NULL DEFAULT 'manual',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, email)
);
CREATE INDEX IF NOT EXISTS contacts_user_idx ON contacts (user_id, name);

CREATE TABLE IF NOT EXISTS storage_accounts (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  provider_kind text NOT NULL DEFAULT 'oauth',
  account_email citext NOT NULL DEFAULT '',
  access_token_enc text NOT NULL DEFAULT '',
  refresh_token_enc text NOT NULL DEFAULT '',
  token_expires_at timestamptz,
  scope text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'disconnected',
  used_bytes bigint NOT NULL DEFAULT 0,
  limit_bytes bigint NOT NULL DEFAULT 0,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_sync_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, provider_id)
);

CREATE TABLE IF NOT EXISTS storage_oauth_states (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_audit (
  id bigserial PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  actor text NOT NULL DEFAULT '',
  action text NOT NULL,
  target text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  ip text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workspace_audit_user_idx ON workspace_audit (user_id, created_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON tasks, files, file_shares, calendar_events,
  calendar_attendees, contacts, storage_accounts, storage_oauth_states, workspace_audit
  TO a8m_app;
GRANT USAGE, SELECT ON SEQUENCE workspace_audit_id_seq TO a8m_app;

-- Backfill normalized workspace rows from the transitional per-user documents.
INSERT INTO tasks (id, tenant_id, user_id, title, notes, status, priority, due_date, due_time,
                   assignee_user_id, linked_thread_id, linked_subject, source, tags, completed_at,
                   created_at, updated_at)
SELECT t.elem->>'id', s.tenant_id, s.user_id, COALESCE(t.elem->>'title', 'Task'),
       COALESCE(t.elem->>'notes', ''),
       CASE WHEN t.elem->>'status' = 'done' THEN 'done' ELSE 'open' END,
       CASE WHEN t.elem->>'priority' IN ('low','normal','high','urgent') THEN t.elem->>'priority' ELSE 'normal' END,
       CASE WHEN COALESCE(t.elem->>'due','') ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN (t.elem->>'due')::date END,
       CASE WHEN COALESCE(t.elem->>'dueTime','') ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$' THEN (t.elem->>'dueTime')::time END,
       (SELECT u.id FROM users u WHERE lower(u.email::text) = lower(COALESCE(t.elem->'assignee'->>'email',''))
          AND u.tenant_id = s.tenant_id LIMIT 1),
       NULLIF(t.elem->>'linkedThreadId',''), NULLIF(t.elem->>'linkedSubject',''),
       COALESCE(NULLIF(t.elem->>'source',''), 'manual'),
       ARRAY(SELECT jsonb_array_elements_text(COALESCE(t.elem->'tags','[]'::jsonb))),
       CASE WHEN COALESCE(t.elem->>'completedAt','') <> '' THEN (t.elem->>'completedAt')::timestamptz END,
       COALESCE(NULLIF(t.elem->>'createdAt','')::timestamptz, now()),
       COALESCE(NULLIF(t.elem->>'updatedAt','')::timestamptz, now())
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'tasks') = 'array' THEN s.state->'tasks' ELSE '[]'::jsonb END
) AS t(elem)
WHERE COALESCE(t.elem->>'id','') <> ''
ON CONFLICT (id) DO NOTHING;

INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path, description,
                   size_bytes, storage_key, attachment_id, external_url, starred, locked, version,
                   previewable, checksum, metadata, created_at, updated_at)
SELECT f.elem->>'id', s.tenant_id, s.user_id, COALESCE(NULLIF(f.elem->>'providerId',''), 'a8m'),
       COALESCE(f.elem->>'externalId',''), COALESCE(NULLIF(f.elem->>'name',''), 'File'),
       COALESCE(NULLIF(f.elem->>'mime',''), 'application/octet-stream'),
       COALESCE(f.elem->>'path',''), COALESCE(f.elem->>'description',''),
       COALESCE(NULLIF(f.elem->>'sizeBytes','')::bigint, 0),
       COALESCE(s.state->'attachments'->(f.elem->>'attachmentId')->>'storedName', NULLIF(f.elem->>'attachmentId',''), ''),
       NULLIF(f.elem->>'attachmentId',''), COALESCE(f.elem->>'contentUrl',''),
       COALESCE((f.elem->>'starred')::boolean, false), COALESCE((f.elem->>'locked')::boolean, false),
       COALESCE(NULLIF(f.elem->>'version','')::integer, 1), COALESCE((f.elem->>'previewable')::boolean, false),
       COALESCE(f.elem->>'checksum',''),
       jsonb_build_object('color', f.elem->>'color', 'sharedWith', COALESCE(f.elem->'sharedWith','[]'::jsonb)),
       COALESCE(NULLIF(f.elem->>'createdAt','')::timestamptz, now()),
       COALESCE(NULLIF(f.elem->>'modifiedAt','')::timestamptz, now())
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'files') = 'array' THEN s.state->'files' ELSE '[]'::jsonb END
) AS f(elem)
WHERE COALESCE(f.elem->>'id','') <> ''
ON CONFLICT (id) DO NOTHING;

INSERT INTO file_shares (id, file_id, tenant_id, created_by, token_hash, access, expires_at,
                         access_count, created_at)
SELECT 'share_' || substr(md5(sh.elem->>'token'), 1, 24), f.elem->>'id', s.tenant_id, s.user_id,
       encode(digest(sh.elem->>'token', 'sha256'), 'hex'),
       CASE WHEN sh.elem->>'access' = 'edit' THEN 'edit' ELSE 'view' END,
       COALESCE(NULLIF(sh.elem->>'expiresAt','')::timestamptz, now() + interval '7 days'),
       0, COALESCE(NULLIF(sh.elem->>'createdAt','')::timestamptz, now())
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'files') = 'array' THEN s.state->'files' ELSE '[]'::jsonb END
) AS f(elem)
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(f.elem->'shares') = 'array' THEN f.elem->'shares' ELSE '[]'::jsonb END
) AS sh(elem)
WHERE COALESCE(f.elem->>'id','') <> '' AND COALESCE(sh.elem->>'token','') <> ''
ON CONFLICT (id) DO NOTHING;

INSERT INTO calendar_events (id, tenant_id, user_id, calendar_id, uid, title, description, location,
                             starts_at, ends_at, all_day, timezone, status, visibility, organizer_email,
                             etag, metadata, created_at, updated_at)
SELECT c.elem->>'id', s.tenant_id, s.user_id, 'default', COALESCE(NULLIF(c.elem->>'uid',''), c.elem->>'id'),
       COALESCE(NULLIF(c.elem->>'title',''), 'Event'), COALESCE(c.elem->>'description',''),
       COALESCE(c.elem->>'where',''),
       ((c.elem->>'date') || ' ' || COALESCE(NULLIF(c.elem->>'start',''), '00:00'))::timestamptz,
       ((c.elem->>'date') || ' ' || COALESCE(NULLIF(c.elem->>'end',''), c.elem->>'start', '01:00'))::timestamptz,
       false, COALESCE(NULLIF(c.elem->>'timezone',''), 'UTC'), 'confirmed', 'default',
       (SELECT u.email FROM users u WHERE u.id = s.user_id),
       md5(c.elem::text), jsonb_build_object('color', c.elem->>'color', 'attendeesCount', c.elem->'attendees'),
       COALESCE(NULLIF(c.elem->>'createdAt','')::timestamptz, now()),
       COALESCE(NULLIF(c.elem->>'updatedAt','')::timestamptz, now())
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'calendarEvents') = 'array' THEN s.state->'calendarEvents' ELSE '[]'::jsonb END
) AS c(elem)
WHERE COALESCE(c.elem->>'id','') <> ''
  AND COALESCE(c.elem->>'date','') ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
  AND COALESCE(c.elem->>'start','') ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'
  AND COALESCE(NULLIF(c.elem->>'end',''), c.elem->>'start') ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'
ON CONFLICT (id) DO NOTHING;

-- Workspace data is now table-backed. Keep mail state and audit documents.
UPDATE app_states
SET state = state - 'tasks' - 'files' - 'calendarEvents' - 'storageProviders',
    updated_at = now();

INSERT INTO schema_migrations (version) VALUES ('0007_workspace')
ON CONFLICT (version) DO NOTHING;

COMMIT;
