-- Per-mailbox storage placement. mail_home is intentionally nullable:
-- existing mailboxes keep NULL and resolve through the exact legacy Dovecot
-- expression until an operator explicitly moves or re-places them. Do not
-- mass-backfill this column from maildir_path; that column may be stale on
-- installations where mail was moved by hand.
BEGIN;

ALTER TABLE public.mailboxes ADD COLUMN IF NOT EXISTS mail_home text;
ALTER TABLE public.mailboxes ADD COLUMN IF NOT EXISTS storage_volume_id uuid;
ALTER TABLE public.mailboxes ADD COLUMN IF NOT EXISTS storage_state text NOT NULL DEFAULT 'ready';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'mailboxes_storage_volume_fk'
  ) THEN
    ALTER TABLE public.mailboxes
      ADD CONSTRAINT mailboxes_storage_volume_fk
      FOREIGN KEY (storage_volume_id) REFERENCES control.storage_volumes(id) ON DELETE RESTRICT;
  END IF;
END $$;

ALTER TABLE public.mail_domains ADD COLUMN IF NOT EXISTS storage_volume_id uuid;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'mail_domains_storage_volume_fk'
  ) THEN
    ALTER TABLE public.mail_domains
      ADD CONSTRAINT mail_domains_storage_volume_fk
      FOREIGN KEY (storage_volume_id) REFERENCES control.storage_volumes(id) ON DELETE RESTRICT;
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS mailboxes_storage_volume_idx ON public.mailboxes (storage_volume_id);
CREATE INDEX IF NOT EXISTS mail_domains_storage_volume_idx ON public.mail_domains (storage_volume_id);

INSERT INTO schema_migrations (version) VALUES ('0089_mailbox_home')
ON CONFLICT (version) DO NOTHING;

COMMIT;
