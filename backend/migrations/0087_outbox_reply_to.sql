-- The outbox is normalized into its own table. Reply-To was only carried in
-- the in-memory state item, so the delivery worker (which rebuilds the queue
-- from this table) dropped it and the header never reached the submitted MIME.
BEGIN;

ALTER TABLE public.outbox ADD COLUMN IF NOT EXISTS reply_to citext;

INSERT INTO schema_migrations (version) VALUES ('0087_outbox_reply_to')
ON CONFLICT (version) DO NOTHING;

COMMIT;
