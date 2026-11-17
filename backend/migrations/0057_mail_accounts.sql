-- Multi-account Phase 0: account and folder foundations.
--
-- Every Alt8Mail user keeps one native account. External IMAP/SMTP accounts
-- are added alongside it. Messages, outbox rows and sending identities carry an
-- account id so later phases can scope mailbox work without changing the
-- existing native path. A BEFORE INSERT trigger fills the native account for
-- older code paths that do not know about accounts yet.
BEGIN;

CREATE TABLE IF NOT EXISTS public.mail_accounts (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'external' CHECK (kind IN ('native', 'external')),
  provider text NOT NULL DEFAULT 'imap',
  email citext NOT NULL,
  display_name text NOT NULL DEFAULT '',
  color text NOT NULL DEFAULT '#34557a',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reauth', 'error', 'disabled')),
  auth_type text NOT NULL DEFAULT 'password' CHECK (auth_type IN ('native', 'password', 'oauth2')),
  secret_sealed text NOT NULL DEFAULT '',
  oauth_refresh_sealed text NOT NULL DEFAULT '',
  oauth_access_sealed text NOT NULL DEFAULT '',
  oauth_expires_at timestamptz,
  imap_host text NOT NULL DEFAULT '',
  imap_port integer NOT NULL DEFAULT 993,
  imap_tls text NOT NULL DEFAULT 'tls' CHECK (imap_tls IN ('tls', 'starttls', 'none')),
  smtp_host text NOT NULL DEFAULT '',
  smtp_port integer NOT NULL DEFAULT 587,
  smtp_tls text NOT NULL DEFAULT 'starttls' CHECK (smtp_tls IN ('tls', 'starttls', 'none')),
  username text NOT NULL DEFAULT '',
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_sync_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, provider, email)
);
CREATE INDEX IF NOT EXISTS mail_accounts_user_idx
  ON public.mail_accounts (user_id, created_at);
CREATE INDEX IF NOT EXISTS mail_accounts_tenant_idx
  ON public.mail_accounts (tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS mail_accounts_native_uidx
  ON public.mail_accounts (user_id) WHERE kind = 'native';

CREATE TABLE IF NOT EXISTS public.mail_folders (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  account_id text NOT NULL REFERENCES public.mail_accounts(id) ON DELETE CASCADE,
  remote_name text NOT NULL,
  display_name text NOT NULL DEFAULT '',
  parent_name text NOT NULL DEFAULT '',
  delimiter text NOT NULL DEFAULT '/',
  role text NOT NULL DEFAULT 'custom'
    CHECK (role IN ('inbox', 'sent', 'drafts', 'archive', 'junk', 'trash', 'all', 'flagged', 'custom')),
  special_use text NOT NULL DEFAULT '',
  selectable boolean NOT NULL DEFAULT true,
  uid_validity bigint NOT NULL DEFAULT 0,
  uid_next bigint NOT NULL DEFAULT 0,
  last_synced_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (account_id, remote_name)
);
CREATE INDEX IF NOT EXISTS mail_folders_account_idx
  ON public.mail_folders (account_id, role);
CREATE INDEX IF NOT EXISTS mail_folders_user_idx
  ON public.mail_folders (user_id);

-- The native account is the existing Dovecot mailbox. Its id is deterministic
-- from the user id so the backfill and future inserts agree.
INSERT INTO public.mail_accounts (
  id, tenant_id, user_id, kind, provider, email, display_name, color, status, auth_type
)
SELECT 'macct_' || replace(u.id::text, '-', ''), u.tenant_id, u.id,
       'native', 'alt8mail', u.email, u.name, u.color, 'active', 'native'
FROM public.users u
ON CONFLICT DO NOTHING;

-- Seed the standard native folders. Custom folders are discovered by the
-- IMAP sync in a later phase.
INSERT INTO public.mail_folders (
  id, tenant_id, user_id, account_id, remote_name, display_name, role, special_use, delimiter
)
SELECT 'mfld_' || replace(gen_random_uuid()::text, '-', ''),
       a.tenant_id, a.user_id, a.id, f.remote_name, f.display_name, f.role, f.special_use, '/'
FROM public.mail_accounts a
JOIN (VALUES
  ('INBOX', 'Inbox', 'inbox', '\Inbox'),
  ('Sent', 'Sent', 'sent', '\Sent'),
  ('Drafts', 'Drafts', 'drafts', '\Drafts'),
  ('Archive', 'Archive', 'archive', '\Archive'),
  ('Junk', 'Junk', 'junk', '\Junk'),
  ('Trash', 'Trash', 'trash', '\Trash')
) AS f(remote_name, display_name, role, special_use) ON true
WHERE a.kind = 'native'
ON CONFLICT (account_id, remote_name) DO NOTHING;

-- A newly created user gets a native account and the six standard folders.
CREATE OR REPLACE FUNCTION public.a8m_create_native_mail_account() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
  INSERT INTO public.mail_accounts (
    id, tenant_id, user_id, kind, provider, email, display_name, color, status, auth_type
  ) VALUES (
    'macct_' || replace(NEW.id::text, '-', ''), NEW.tenant_id, NEW.id,
    'native', 'alt8mail', NEW.email, NEW.name, NEW.color, 'active', 'native'
  )
  ON CONFLICT DO NOTHING;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.a8m_seed_native_mail_folders() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
  IF NEW.kind = 'native' THEN
    INSERT INTO public.mail_folders (
      id, tenant_id, user_id, account_id, remote_name, display_name, role, special_use, delimiter
    ) VALUES
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'INBOX', 'Inbox', 'inbox', '\Inbox', '/'),
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'Sent', 'Sent', 'sent', '\Sent', '/'),
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'Drafts', 'Drafts', 'drafts', '\Drafts', '/'),
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'Archive', 'Archive', 'archive', '\Archive', '/'),
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'Junk', 'Junk', 'junk', '\Junk', '/'),
      ('mfld_' || replace(gen_random_uuid()::text, '-', ''), NEW.tenant_id, NEW.user_id, NEW.id, 'Trash', 'Trash', 'trash', '\Trash', '/')
    ON CONFLICT (account_id, remote_name) DO NOTHING;
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.a8m_sync_native_mail_account() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
  UPDATE public.mail_accounts
  SET email = NEW.email, display_name = NEW.name, color = NEW.color, updated_at = now()
  WHERE user_id = NEW.id AND kind = 'native';
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS a8m_users_create_native_mail_account ON public.users;
CREATE TRIGGER a8m_users_create_native_mail_account
AFTER INSERT ON public.users
FOR EACH ROW EXECUTE FUNCTION public.a8m_create_native_mail_account();

DROP TRIGGER IF EXISTS a8m_users_sync_native_mail_account ON public.users;
CREATE TRIGGER a8m_users_sync_native_mail_account
AFTER UPDATE OF email, name, color ON public.users
FOR EACH ROW EXECUTE FUNCTION public.a8m_sync_native_mail_account();

DROP TRIGGER IF EXISTS a8m_mail_accounts_seed_folders ON public.mail_accounts;
CREATE TRIGGER a8m_mail_accounts_seed_folders
AFTER INSERT ON public.mail_accounts
FOR EACH ROW EXECUTE FUNCTION public.a8m_seed_native_mail_folders();

-- Older mail paths do not pass an account id. Fill the native account
-- automatically and reject an account that belongs to a different user.
CREATE OR REPLACE FUNCTION public.a8m_fill_mail_account_id() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
BEGIN
  IF NEW.account_id IS NULL OR NEW.account_id = '' THEN
    SELECT a.id INTO NEW.account_id
    FROM public.mail_accounts a
    WHERE a.user_id = NEW.user_id AND a.kind = 'native'
    LIMIT 1;
  ELSIF NOT EXISTS (
    SELECT 1 FROM public.mail_accounts a
    WHERE a.id = NEW.account_id AND a.user_id = NEW.user_id
  ) THEN
    RAISE EXCEPTION 'mail account % does not belong to user %', NEW.account_id, NEW.user_id;
  END IF;
  RETURN NEW;
END;
$$;

ALTER TABLE public.mail_messages ADD COLUMN IF NOT EXISTS account_id text;
UPDATE public.mail_messages m
SET account_id = a.id
FROM public.mail_accounts a
WHERE a.user_id = m.user_id AND a.kind = 'native' AND m.account_id IS NULL;
ALTER TABLE public.mail_messages ALTER COLUMN account_id SET NOT NULL;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'mail_messages_account_id_fkey') THEN
    ALTER TABLE public.mail_messages
      ADD CONSTRAINT mail_messages_account_id_fkey
      FOREIGN KEY (account_id) REFERENCES public.mail_accounts(id) ON DELETE CASCADE;
  END IF;
END $$;

ALTER TABLE public.outbox ADD COLUMN IF NOT EXISTS account_id text;
UPDATE public.outbox o
SET account_id = a.id
FROM public.mail_accounts a
WHERE a.user_id = o.user_id AND a.kind = 'native' AND o.account_id IS NULL;
ALTER TABLE public.outbox ALTER COLUMN account_id SET NOT NULL;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'outbox_account_id_fkey') THEN
    ALTER TABLE public.outbox
      ADD CONSTRAINT outbox_account_id_fkey
      FOREIGN KEY (account_id) REFERENCES public.mail_accounts(id) ON DELETE CASCADE;
  END IF;
END $$;

ALTER TABLE public.mail_identities ADD COLUMN IF NOT EXISTS account_id text;
UPDATE public.mail_identities i
SET account_id = a.id
FROM public.mail_accounts a
WHERE a.user_id = i.user_id AND a.kind = 'native' AND i.account_id IS NULL;
ALTER TABLE public.mail_identities ALTER COLUMN account_id SET NOT NULL;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'mail_identities_account_id_fkey') THEN
    ALTER TABLE public.mail_identities
      ADD CONSTRAINT mail_identities_account_id_fkey
      FOREIGN KEY (account_id) REFERENCES public.mail_accounts(id) ON DELETE CASCADE;
  END IF;
END $$;

DROP TRIGGER IF EXISTS a8m_mail_messages_fill_account ON public.mail_messages;
CREATE TRIGGER a8m_mail_messages_fill_account
BEFORE INSERT OR UPDATE OF account_id, user_id ON public.mail_messages
FOR EACH ROW EXECUTE FUNCTION public.a8m_fill_mail_account_id();

DROP TRIGGER IF EXISTS a8m_outbox_fill_account ON public.outbox;
CREATE TRIGGER a8m_outbox_fill_account
BEFORE INSERT OR UPDATE OF account_id, user_id ON public.outbox
FOR EACH ROW EXECUTE FUNCTION public.a8m_fill_mail_account_id();

DROP TRIGGER IF EXISTS a8m_mail_identities_fill_account ON public.mail_identities;
CREATE TRIGGER a8m_mail_identities_fill_account
BEFORE INSERT OR UPDATE OF account_id, user_id ON public.mail_identities
FOR EACH ROW EXECUTE FUNCTION public.a8m_fill_mail_account_id();

-- UIDs are unique per IMAP account, not per Alt8Mail user. The old index would
-- collide as soon as two external accounts report the same folder/UID.
DROP INDEX IF EXISTS public.mail_messages_imap_uidx;
CREATE UNIQUE INDEX mail_messages_imap_uidx
  ON public.mail_messages (account_id, imap_folder, uid_validity, uid)
  WHERE uid > 0;

CREATE INDEX IF NOT EXISTS mail_messages_account_folder_idx
  ON public.mail_messages (account_id, folder, sent_at DESC);
CREATE INDEX IF NOT EXISTS mail_messages_account_thread_idx
  ON public.mail_messages (account_id, thread_id, sent_at);
CREATE INDEX IF NOT EXISTS outbox_account_idx
  ON public.outbox (account_id, send_at);
CREATE INDEX IF NOT EXISTS mail_identities_account_idx
  ON public.mail_identities (account_id, created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON public.mail_accounts, public.mail_folders TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0057_mail_accounts')
ON CONFLICT (version) DO NOTHING;

COMMIT;
