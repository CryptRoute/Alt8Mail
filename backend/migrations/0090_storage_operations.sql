-- Persistent mailbox move operations. The source home is retained until the
-- operation is verified and its grace window is finalized or rolled back.
BEGIN;

CREATE TABLE IF NOT EXISTS control.storage_operations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL,
  status text NOT NULL DEFAULT 'planned'
    CHECK (status IN ('planned', 'queued', 'running', 'grace', 'succeeded', 'failed', 'rolled_back', 'canceled')),
  plan jsonb NOT NULL DEFAULT '{}'::jsonb,
  plan_hash text NOT NULL DEFAULT '',
  plan_expires_at timestamptz,
  step text NOT NULL DEFAULT '',
  progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  total_bytes bigint NOT NULL DEFAULT 0,
  moved_bytes bigint NOT NULL DEFAULT 0,
  log text NOT NULL DEFAULT '',
  error text NOT NULL DEFAULT '',
  cancel_requested boolean NOT NULL DEFAULT false,
  actor_id uuid,
  actor_email text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  finalize_at timestamptz
);
CREATE INDEX IF NOT EXISTS storage_operations_status_idx ON control.storage_operations (status, created_at DESC);
CREATE INDEX IF NOT EXISTS storage_operations_actor_idx ON control.storage_operations (actor_email);

CREATE TABLE IF NOT EXISTS control.storage_move_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operation_id uuid NOT NULL REFERENCES control.storage_operations(id) ON DELETE CASCADE,
  mailbox_id uuid NOT NULL,
  email citext NOT NULL,
  source_home text NOT NULL,
  target_home text NOT NULL,
  source_mail_home text NOT NULL DEFAULT '',
  source_volume_id uuid,
  source_volume_root text NOT NULL DEFAULT '',
  source_volume_name text NOT NULL DEFAULT '',
  target_volume_id uuid NOT NULL,
  target_volume_root text NOT NULL DEFAULT '',
  target_volume_name text NOT NULL DEFAULT '',
  target_is_legacy boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'syncing', 'switching', 'verifying', 'grace', 'finalized', 'failed', 'rolled_back', 'canceled')),
  bytes bigint NOT NULL DEFAULT 0,
  messages bigint NOT NULL DEFAULT 0,
  error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS storage_move_items_operation_idx ON control.storage_move_items (operation_id, status);
CREATE INDEX IF NOT EXISTS storage_move_items_mailbox_idx ON control.storage_move_items (mailbox_id, status);

INSERT INTO schema_migrations (version) VALUES ('0090_storage_operations')
ON CONFLICT (version) DO NOTHING;

COMMIT;
