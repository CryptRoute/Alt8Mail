-- Subscriptions are a logical folder: the physical IMAP copy stays in INBOX,
-- but List-Unsubscribe mail is presented only in the Subscriptions view.
BEGIN;

UPDATE public.mail_messages
SET folder = 'subscriptions', updated_at = now()
WHERE folder = 'inbox'
  AND unsubscribe IS NOT NULL
  AND unsubscribe <> '{}'::jsonb;

CREATE INDEX IF NOT EXISTS mail_messages_user_subscriptions_idx
  ON public.mail_messages (user_id, sent_at DESC)
  WHERE folder = 'subscriptions';

COMMIT;
