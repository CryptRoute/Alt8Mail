-- Normalize the scheduled/undo-send queue. Outbox rows contain the queued
-- message body; they must not live in the per-user app_states document.
BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0012_outbox') THEN
    DROP TABLE IF EXISTS outbox CASCADE;
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  draft_id text NOT NULL DEFAULT '',
  from_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  to_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  cc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  bcc_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  subject text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  attachments jsonb NOT NULL DEFAULT '[]'::jsonb,
  thread_id text NOT NULL DEFAULT '',
  in_reply_to text,
  priority text NOT NULL DEFAULT 'normal',
  receipt boolean NOT NULL DEFAULT false,
  delivery boolean NOT NULL DEFAULT false,
  save_copy boolean NOT NULL DEFAULT true,
  scheduled boolean NOT NULL DEFAULT false,
  send_at timestamptz NOT NULL DEFAULT now(),
  queued_at timestamptz NOT NULL DEFAULT now(),
  hold_ms bigint NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'queued',
  delivery_state text NOT NULL DEFAULT '',
  delivery_claimed_at timestamptz,
  retry_at timestamptz,
  attempts integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  mime_message_id text NOT NULL DEFAULT '',
  draft_snapshot jsonb,
  ip text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS outbox_due_idx ON outbox (send_at) WHERE status = 'queued';
CREATE INDEX IF NOT EXISTS outbox_user_idx ON outbox (user_id, send_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON outbox TO a8m_app;

INSERT INTO outbox (
  id, tenant_id, user_id, draft_id, from_json, to_json, cc_json, bcc_json, subject,
  body_text, attachments, thread_id, in_reply_to, priority, receipt, delivery, save_copy,
  scheduled, send_at, queued_at, hold_ms, status, delivery_state, delivery_claimed_at,
  retry_at, attempts, last_error, mime_message_id, draft_snapshot, ip, user_agent,
  created_at, updated_at
)
SELECT
  o->>'id', s.tenant_id, s.user_id, COALESCE(o->>'draftId',''),
  COALESCE(o->'from','{}'::jsonb), COALESCE(o->'to','[]'::jsonb),
  COALESCE(o->'cc','[]'::jsonb), COALESCE(o->'bcc','[]'::jsonb),
  COALESCE(o->>'subject',''), COALESCE(o->>'bodyText',''),
  COALESCE(o->'attachments','[]'::jsonb), COALESCE(o->>'threadId',''),
  NULLIF(o->>'inReplyTo',''), COALESCE(NULLIF(o->>'priority',''),'normal'),
  COALESCE((o->>'receipt')::boolean, false),
  COALESCE((o->>'delivery')::boolean, false),
  COALESCE((o->>'saveCopy')::boolean, true),
  COALESCE((o->>'scheduled')::boolean, false),
  COALESCE(NULLIF(o->>'sendAt','')::timestamptz, now()),
  COALESCE(NULLIF(o->>'queuedAt','')::timestamptz, now()),
  COALESCE(NULLIF(o->>'holdMs','')::bigint, 0),
  'queued',
  COALESCE(o->>'deliveryState',''),
  NULLIF(o->>'deliveryClaimedAt','')::timestamptz,
  NULLIF(o->>'retryAt','')::timestamptz,
  COALESCE(NULLIF(o->>'attempts','')::integer, 0),
  COALESCE(o->>'lastError',''),
  COALESCE(o->>'mimeMessageId',''),
  NULLIF(o->'draftSnapshot','null'::jsonb),
  COALESCE(o->>'ip',''), COALESCE(o->>'userAgent',''),
  COALESCE(NULLIF(o->>'queuedAt','')::timestamptz, now()),
  COALESCE(NULLIF(o->>'queuedAt','')::timestamptz, now())
FROM app_states s
CROSS JOIN LATERAL jsonb_array_elements(
  CASE WHEN jsonb_typeof(s.state->'outbox') = 'array' THEN s.state->'outbox' ELSE '[]'::jsonb END
) AS o
WHERE COALESCE(o->>'id','') <> ''
ON CONFLICT (id) DO NOTHING;

UPDATE app_states
SET state = state - 'outbox', updated_at = now();

INSERT INTO schema_migrations (version) VALUES ('0012_outbox')
ON CONFLICT (version) DO NOTHING;

COMMIT;
