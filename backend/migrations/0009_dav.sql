-- CalDAV/CardDAV resource identity and change tags.
BEGIN;

ALTER TABLE calendar_events
  ADD COLUMN IF NOT EXISTS resource_name text NOT NULL DEFAULT '';

UPDATE calendar_events
SET resource_name = id || '.ics'
WHERE resource_name = '';

CREATE UNIQUE INDEX IF NOT EXISTS calendar_resource_uidx
  ON calendar_events (user_id, calendar_id, resource_name)
  WHERE resource_name <> '';

ALTER TABLE contacts
  ADD COLUMN IF NOT EXISTS uid text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS resource_name text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS etag text NOT NULL DEFAULT '';

UPDATE contacts
SET uid = id
WHERE uid = '';

UPDATE contacts
SET resource_name = id || '.vcf'
WHERE resource_name = '';

UPDATE contacts
SET etag = md5(id || ':' || updated_at::text)
WHERE etag = '';

CREATE UNIQUE INDEX IF NOT EXISTS contacts_resource_uidx
  ON contacts (user_id, resource_name)
  WHERE resource_name <> '';

CREATE INDEX IF NOT EXISTS contacts_uid_idx
  ON contacts (user_id, uid)
  WHERE uid <> '';

GRANT SELECT, INSERT, UPDATE, DELETE ON contacts, calendar_events TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0009_dav')
ON CONFLICT (version) DO NOTHING;

COMMIT;
