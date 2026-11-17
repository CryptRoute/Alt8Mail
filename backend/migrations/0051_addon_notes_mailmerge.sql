-- Add-on Wave 4: Evernote notes and Mergo mail-merge handoff.
BEGIN;

ALTER TABLE public.addon_system_catalog
  DROP CONSTRAINT IF EXISTS addon_system_catalog_auth_type_check;
ALTER TABLE public.addon_system_catalog
  ADD CONSTRAINT addon_system_catalog_auth_type_check
  CHECK (auth_type IN ('api_key', 'webhook', 'oauth2', 'email'));

INSERT INTO public.addon_system_catalog (
  id, name, vendor, category, description, tagline, icon, color, logo_asset,
  website_url, support_url, privacy_url, docs_url, connector_version,
  connection_scope, auth_type, self_service, requires_tenant_setup,
  scopes, permissions, actions, fields, sort_order
) VALUES
  (
    'evernote', 'Evernote', 'Evernote Corporation', 'Notes',
    'Save the selected email as a note in Evernote.',
    'Turn an important email into a searchable Evernote note.',
    'i-file', '#00a82d', 'evernote.svg',
    'https://evernote.com', 'https://help.evernote.com',
    'https://evernote.com/privacy', 'https://help.evernote.com/hc/en-us/articles/209005347-Save-emails-into-Evernote', '1.0',
    'user', 'email', true, false,
    '["notes:write"]'::jsonb,
    '["Read the selected email''s subject, sender and content", "Send that email to your private Evernote notes address"]'::jsonb,
    '[{"id":"save-email","label":"Save to Evernote","description":"Send the selected email to your Evernote notes address.","surface":"reader"}]'::jsonb,
    '[{"key":"evernoteEmail","label":"Evernote email address","type":"text","required":true,"secret":true,"placeholder":"name.abc123@m.evernote.com","help":"Find your private Evernote email address under Account settings → Email notes to. It is encrypted before it is stored."}]'::jsonb,
    190
  ),
  (
    'mergo', 'Mergo', 'Mergo', 'Marketing',
    'Add the selected sender to the Google Sheet used by a Mergo mail-merge campaign.',
    'Put a new contact into the Mergo campaign sheet without leaving the inbox.',
    'i-send', '#0fa37f', 'mergo.png',
    'https://www.mergo.app', 'https://help.mergo.app',
    'https://www.mergo.app/privacy-policy/', 'https://help.mergo.app/article/66-automatically-send-emails-when-a-row-is-populated', '1.0',
    'user', 'webhook', true, false,
    '["campaigns:write"]'::jsonb,
    '["Read the selected email''s sender and subject", "Send the sender''s name and email to your Mergo campaign sheet webhook"]'::jsonb,
    '[{"id":"add-contact","label":"Add to Mergo campaign","description":"Add the sender to the configured Mergo campaign sheet.","surface":"reader"}]'::jsonb,
    '[{"key":"webhookUrl","label":"Mergo sheet webhook URL","type":"password","required":true,"secret":true,"placeholder":"https://script.google.com/macros/s/…/exec","help":"Mergo reads campaign recipients from Google Sheets. Deploy the documented Apps Script web app for your campaign sheet and paste its /exec URL here. The URL is encrypted before it is stored."}]'::jsonb,
    200
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('0051_addon_notes_mailmerge') ON CONFLICT DO NOTHING;

COMMIT;
