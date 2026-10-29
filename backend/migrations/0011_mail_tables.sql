-- Normalized mail storage, server-side full-text index and attachment links.
-- The earlier mail_messages/attachments tables from 0001 were never written by
-- the API; they are replaced here while preserving every message currently in
-- app_states.
BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0011_mail_tables') THEN
    DROP TABLE IF EXISTS mail_message_attachments CASCADE;
    DROP TABLE IF EXISTS mail_messages CASCADE;
    DROP TABLE IF EXISTS attachments CASCADE;
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS mail_messages (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  mailbox_id uuid REFERENCES mailboxes(id) ON DELETE CASCADE,
  folder text NOT NULL,
  imap_folder text NOT NULL DEFAULT '',
  uid bigint NOT NULL DEFAULT 0,
  uid_validity bigint NOT NULL DEFAULT 0,
  message_id text NOT NULL DEFAULT '',
  thread_id text NOT NULL,
  in_reply_to text,
  references_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  subject text NOT NULL DEFAULT '',
  from_name text NOT NULL DEFAULT '',
  from_email citext NOT NULL DEFAULT '',
  to_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  cc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  bcc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  to_text text NOT NULL DEFAULT '',
  cc_text text NOT NULL DEFAULT '',
  bcc_text text NOT NULL DEFAULT '',
  snippet text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  sent_at timestamptz NOT NULL DEFAULT now(),
  received_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  size_bytes bigint NOT NULL DEFAULT 0,
  unread boolean NOT NULL DEFAULT true,
  starred boolean NOT NULL DEFAULT false,
  flagged boolean NOT NULL DEFAULT false,
  draft boolean NOT NULL DEFAULT false,
  external boolean NOT NULL DEFAULT false,
  classification text NOT NULL DEFAULT 'Internal',
  categories text[] NOT NULL DEFAULT '{}',
  auth jsonb NOT NULL DEFAULT '{}'::jsonb,
  spam_report jsonb,
  event jsonb,
  priority text NOT NULL DEFAULT 'normal',
  receipt boolean NOT NULL DEFAULT false,
  delivery boolean NOT NULL DEFAULT false,
  save_copy boolean NOT NULL DEFAULT true,
  via_api boolean NOT NULL DEFAULT true,
  window_mode text NOT NULL DEFAULT 'floating',
  search_vector tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(subject, '') || ' ' || coalesce(from_name, '') || ' ' || coalesce(from_email::text, '')), 'A') ||
    setweight(to_tsvector('simple', coalesce(to_text, '') || ' ' || coalesce(cc_text, '') || ' ' || coalesce(bcc_text, '')), 'B') ||
    setweight(to_tsvector('english', coalesce(body_text, '')), 'C')
  ) STORED
);

CREATE INDEX IF NOT EXISTS mail_messages_user_folder_idx
  ON mail_messages (user_id, folder, sent_at DESC);
CREATE INDEX IF NOT EXISTS mail_messages_user_thread_idx
  ON mail_messages (user_id, thread_id, sent_at);
CREATE INDEX IF NOT EXISTS mail_messages_user_starred_idx
  ON mail_messages (user_id, sent_at DESC) WHERE starred OR flagged;
CREATE UNIQUE INDEX IF NOT EXISTS mail_messages_imap_uidx
  ON mail_messages (user_id, imap_folder, uid_validity, uid) WHERE uid > 0;
CREATE INDEX IF NOT EXISTS mail_messages_fts_idx
  ON mail_messages USING GIN (search_vector);

CREATE TABLE IF NOT EXISTS attachments (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL DEFAULT '',
  mime text NOT NULL DEFAULT 'application/octet-stream',
  size_bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  stored_path text NOT NULL DEFAULT '',
  previewable boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS attachments_user_idx ON attachments (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS mail_message_attachments (
  message_id text NOT NULL REFERENCES mail_messages(id) ON DELETE CASCADE,
  attachment_id text NOT NULL REFERENCES attachments(id) ON DELETE CASCADE,
  position integer NOT NULL DEFAULT 0,
  content_id text NOT NULL DEFAULT '',
  disposition text NOT NULL DEFAULT '',
  PRIMARY KEY (message_id, attachment_id)
);
CREATE INDEX IF NOT EXISTS mail_message_attachments_file_idx
  ON mail_message_attachments (attachment_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON mail_messages, attachments, mail_message_attachments
  TO meridian_app;

-- Backfill messages from the transitional per-user document.
INSERT INTO mail_messages (
  id, tenant_id, user_id, mailbox_id, folder, imap_folder, uid, uid_validity,
  message_id, thread_id, in_reply_to, subject, from_name, from_email,
  to_json, cc_json, bcc_json, to_text, cc_text, bcc_text, snippet,
  body_text, body_html, sent_at, received_at, created_at, updated_at, size_bytes,
  unread, starred, flagged, draft, external, classification, categories, auth,
  spam_report, event, priority, receipt, delivery, save_copy, via_api, window_mode
)
SELECT
  m.elem->>'id',
  s.tenant_id,
  s.user_id,
  (SELECT mb.id FROM mailboxes mb WHERE mb.user_id = s.user_id LIMIT 1),
  COALESCE(NULLIF(m.elem->>'folder',''), 'inbox'),
  COALESCE(NULLIF(m.elem->>'imapFolder',''), NULLIF(m.elem->>'folder',''), 'inbox'),
  COALESCE(NULLIF(m.elem->>'imapUid','')::bigint, 0),
  COALESCE(NULLIF(m.elem->>'imapUidValidity','')::bigint, 0),
  COALESCE(m.elem->>'messageId',''),
  COALESCE(NULLIF(m.elem->>'threadId',''), m.elem->>'id'),
  NULLIF(m.elem->>'inReplyTo',''),
  COALESCE(m.elem->>'subject',''),
  COALESCE(m.elem->'from'->>'name',''),
  COALESCE(m.elem->'from'->>'email',''),
  COALESCE(m.elem->'to','[]'::jsonb),
  COALESCE(m.elem->'cc','[]'::jsonb),
  COALESCE(m.elem->'bcc','[]'::jsonb),
  COALESCE((SELECT string_agg(coalesce(p->>'name','') || ' ' || coalesce(p->>'email',''), ' ')
            FROM jsonb_array_elements(COALESCE(m.elem->'to','[]'::jsonb)) p), ''),
  COALESCE((SELECT string_agg(coalesce(p->>'name','') || ' ' || coalesce(p->>'email',''), ' ')
            FROM jsonb_array_elements(COALESCE(m.elem->'cc','[]'::jsonb)) p), ''),
  COALESCE((SELECT string_agg(coalesce(p->>'name','') || ' ' || coalesce(p->>'email',''), ' ')
            FROM jsonb_array_elements(COALESCE(m.elem->'bcc','[]'::jsonb)) p), ''),
  COALESCE(m.elem->>'snippet',''),
  COALESCE(m.elem->>'bodyText',''),
  COALESCE(m.elem->>'bodyHtml',''),
  COALESCE(NULLIF(m.elem->>'date','')::timestamptz, now()),
  COALESCE(NULLIF(m.elem->>'date','')::timestamptz, now()),
  COALESCE(NULLIF(m.elem->>'createdAt','')::timestamptz, NULLIF(m.elem->>'date','')::timestamptz, now()),
  COALESCE(NULLIF(m.elem->>'updatedAt','')::timestamptz, NULLIF(m.elem->>'date','')::timestamptz, now()),
  COALESCE(NULLIF(m.elem->>'sizeBytes','')::bigint, 0),
  COALESCE((m.elem->>'unread')::boolean, true),
  COALESCE((m.elem->>'starred')::boolean, false),
  COALESCE((m.elem->>'flagged')::boolean, false),
  COALESCE((m.elem->>'draft')::boolean, false),
  COALESCE((m.elem->>'external')::boolean, false),
  COALESCE(NULLIF(m.elem->>'classification',''), 'Internal'),
  ARRAY(SELECT jsonb_array_elements_text(COALESCE(m.elem->'categories','[]'::jsonb))),
  COALESCE(m.elem->'auth','{}'::jsonb),
  NULLIF(m.elem->'spamReport','null'::jsonb),
  NULLIF(m.elem->'event','null'::jsonb),
  COALESCE(NULLIF(m.elem->>'priority',''), 'normal'),
  COALESCE((m.elem->>'receipt')::boolean, false),
  COALESCE((m.elem->>'delivery')::boolean, false),
  COALESCE((m.elem->>'saveCopy')::boolean, true),
  COALESCE((m.elem->>'viaApi')::boolean, true),
  COALESCE(NULLIF(m.elem->>'windowMode',''), 'floating')
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'messages') = 'array' THEN s.state->'messages' ELSE '[]'::jsonb END
) AS m(elem)
WHERE COALESCE(m.elem->>'id','') <> ''
ON CONFLICT (id) DO NOTHING;

INSERT INTO attachments (id, tenant_id, user_id, name, mime, size_bytes, sha256, stored_path, previewable)
SELECT
  a.key, s.tenant_id, s.user_id,
  COALESCE(a.value->>'name','attachment'),
  COALESCE(a.value->>'mime','application/octet-stream'),
  COALESCE(NULLIF(a.value->>'sizeBytes','')::bigint, 0),
  '',
  COALESCE(a.value->>'storedName', a.key),
  COALESCE((a.value->>'previewable')::boolean, false)
FROM app_states s
CROSS JOIN LATERAL jsonb_each(
  CASE WHEN jsonb_typeof(s.state->'attachments') = 'object' THEN s.state->'attachments' ELSE '{}'::jsonb END
) AS a
WHERE a.key <> ''
ON CONFLICT (id) DO NOTHING;

INSERT INTO mail_message_attachments (message_id, attachment_id, position, content_id, disposition)
SELECT
  m.elem->>'id',
  a.elem->>'id',
  (a.ord - 1)::integer,
  COALESCE(a.elem->>'contentId',''),
  COALESCE(a.elem->>'disposition','')
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'messages') = 'array' THEN s.state->'messages' ELSE '[]'::jsonb END
) AS m(elem)
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(m.elem->'attachments') = 'array' THEN m.elem->'attachments' ELSE '[]'::jsonb END
) WITH ORDINALITY AS a(elem, ord)
WHERE COALESCE(m.elem->>'id','') <> '' AND COALESCE(a.elem->>'id','') <> ''
ON CONFLICT (message_id, attachment_id) DO NOTHING;

-- Messages and attachment metadata now live in normalized tables.
UPDATE app_states
SET state = state - 'messages' - 'attachments',
    updated_at = now();

INSERT INTO schema_migrations (version) VALUES ('0011_mail_tables')
ON CONFLICT (version) DO NOTHING;

COMMIT;
