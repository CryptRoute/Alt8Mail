-- Message rows and attachment metadata live in the normalized mail tables.
-- Older releases could copy the entire mailbox back into app_states during a
-- successful IMAP sync or folder action, which made every bootstrap parse a
-- multi-megabyte document. Remove those transitional keys, including the
-- duplicate outbox snapshot, permanently.
--
-- autovacuum reclaims the freed table pages; no locking VACUUM FULL is run here.
BEGIN;

UPDATE app_states
SET state = state - 'messages' - 'attachments' - 'outbox',
    updated_at = now()
WHERE state ? 'messages' OR state ? 'attachments' OR state ? 'outbox';

INSERT INTO schema_migrations (version) VALUES ('0027_trim_state_bloat')
ON CONFLICT (version) DO NOTHING;

COMMIT;
