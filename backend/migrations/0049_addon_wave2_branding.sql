-- Add-on phase 3: provider identity for every connector and the CRM, project
-- management and support wave (Pipedrive, Zoho CRM, ClickUp, Zendesk).
BEGIN;

ALTER TABLE public.addon_system_catalog
  ADD COLUMN IF NOT EXISTS tagline text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS website_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS support_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS privacy_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS docs_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS logo_asset text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS connector_version text NOT NULL DEFAULT '1.0';

UPDATE public.addon_system_catalog AS a SET
  tagline = v.tagline,
  website_url = v.website_url,
  support_url = v.support_url,
  privacy_url = v.privacy_url,
  docs_url = v.docs_url,
  logo_asset = v.logo_asset,
  connector_version = '1.0',
  updated_at = now()
FROM (VALUES
  ('deepl', 'Translate any email without leaving the reader.', 'https://www.deepl.com', 'https://support.deepl.com', 'https://www.deepl.com/privacy', 'https://developers.deepl.com/docs', 'deepl.svg'),
  ('todoist', 'Turn an email into a task with the context attached.', 'https://todoist.com', 'https://todoist.com/help', 'https://doist.com/privacy', 'https://developer.todoist.com', 'todoist.svg'),
  ('slack', 'Share an email summary with the right Slack channel.', 'https://slack.com', 'https://slack.com/help', 'https://slack.com/privacy-policy', 'https://api.slack.com', 'slack.png'),
  ('hubspot', 'Log an email to the matching contact''s CRM timeline.', 'https://www.hubspot.com', 'https://help.hubspot.com', 'https://legal.hubspot.com/privacy-policy', 'https://developers.hubspot.com', 'hubspot.svg'),
  ('asana', 'Create a project task from the email you are reading.', 'https://asana.com', 'https://asana.com/support', 'https://asana.com/privacy', 'https://developers.asana.com', 'asana.svg'),
  ('pagerduty', 'Raise an incident from the email that needs attention.', 'https://www.pagerduty.com', 'https://support.pagerduty.com', 'https://www.pagerduty.com/privacy/', 'https://developer.pagerduty.com', 'pagerduty.svg'),
  ('zoom', 'Create a meeting link from any conversation.', 'https://zoom.us', 'https://support.zoom.us', 'https://zoom.us/privacy', 'https://developers.zoom.us', 'zoom.svg')
) AS v(id, tagline, website_url, support_url, privacy_url, docs_url, logo_asset)
WHERE a.id = v.id;

INSERT INTO public.addon_system_catalog (
  id, name, vendor, category, description, tagline, icon, color, connection_scope,
  auth_type, self_service, requires_tenant_setup, scopes, permissions, actions,
  fields, sort_order, website_url, support_url, privacy_url, docs_url, logo_asset,
  connector_version
) VALUES
  (
    'pipedrive', 'Pipedrive', 'Pipedrive, Inc.', 'CRM',
    'Log the selected email to the matching person in Pipedrive.',
    'Keep every customer email attached to the right Pipedrive record.',
    'i-people', '#017737', 'tenant', 'api_key', false, true,
    '["contacts:read","notes:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Find the sender as a Pipedrive person", "Create a note on that person''s timeline"]'::jsonb,
    '[{"id":"log-email","label":"Log email to Pipedrive","description":"Create a note on the sender''s Pipedrive person.","surface":"reader"}]'::jsonb,
    '[{"key":"apiToken","label":"Pipedrive API token","type":"password","placeholder":"Pipedrive API token","help":"Create a personal API token in Pipedrive → Settings → Personal preferences → API. The token is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    80, 'https://www.pipedrive.com', 'https://support.pipedrive.com', 'https://www.pipedrive.com/en/privacy', 'https://developers.pipedrive.com', 'pipedrive.png', '1.0'
  ),
  (
    'zoho', 'Zoho CRM', 'Zoho Corporation', 'CRM',
    'Add an email note to the matching contact in Zoho CRM.',
    'Keep customer conversations in the Zoho CRM contact timeline.',
    'i-people', '#E42527', 'tenant', 'oauth2', false, true,
    '["ZohoCRM.modules.contacts.READ","ZohoCRM.modules.notes.CREATE"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Find the sender as a Zoho CRM contact", "Create a note on that contact''s record"]'::jsonb,
    '[{"id":"log-email","label":"Log email to Zoho CRM","description":"Create a note on the sender''s Zoho CRM contact record.","surface":"reader"}]'::jsonb,
    '[{"key":"region","label":"Zoho data centre","type":"select","default":"com","required":true,"secret":false,"options":[{"value":"com","label":"United States (.com)"},{"value":"eu","label":"Europe (.eu)"},{"value":"in","label":"India (.in)"},{"value":"com.au","label":"Australia (.com.au)"},{"value":"jp","label":"Japan (.jp)"}]},{"key":"clientId","label":"Zoho client ID","type":"text","placeholder":"1000.XXXXXXXXXXXXXXXX","required":true,"secret":false},{"key":"clientSecret","label":"Zoho client secret","type":"password","placeholder":"Zoho client secret","required":true,"secret":true},{"key":"refreshToken","label":"Zoho refresh token","type":"password","placeholder":"Refresh token from a Self Client","help":"Create a Self Client in the Zoho API console, generate a refresh token with the CRM scopes, and paste it here. The token is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    90, 'https://www.zoho.com/crm/', 'https://help.zoho.com/portal/en/kb/crm', 'https://www.zoho.com/privacy.html', 'https://www.zoho.com/crm/developer/docs/api/', 'zoho.svg', '1.0'
  ),
  (
    'clickup', 'ClickUp', 'ClickUp', 'Tasks',
    'Create a ClickUp task from the email you are reading.',
    'Turn important mail into an actionable ClickUp task.',
    'i-check-circle', '#7B68EE', 'user', 'api_key', true, false,
    '["tasks:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a task in the connected ClickUp list"]'::jsonb,
    '[{"id":"create-task","label":"Create ClickUp task","description":"Create a task from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"apiToken","label":"ClickUp personal API token","type":"password","placeholder":"pk_…","help":"Create a personal API token in ClickUp → Settings → Apps. The token is encrypted before it is stored.","required":true,"secret":true},{"key":"listId","label":"ClickUp list ID","type":"text","placeholder":"901234567890","help":"Open the ClickUp list and copy the numeric ID from its URL or list settings.","required":true,"secret":false}]'::jsonb,
    100, 'https://clickup.com', 'https://help.clickup.com', 'https://clickup.com/privacy', 'https://developer.clickup.com', 'clickup.svg', '1.0'
  ),
  (
    'zendesk', 'Zendesk', 'Zendesk, Inc.', 'Support',
    'Open a support ticket from the email you are reading.',
    'Turn customer mail into a tracked Zendesk ticket.',
    'i-people', '#03363D', 'tenant', 'api_key', false, true,
    '["tickets:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a ticket with the email as the initial comment"]'::jsonb,
    '[{"id":"create-ticket","label":"Create Zendesk ticket","description":"Open a ticket with the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"subdomain","label":"Zendesk subdomain","type":"text","placeholder":"acme","help":"Use acme when your help centre is acme.zendesk.com.","required":true,"secret":false},{"key":"email","label":"Agent email","type":"text","placeholder":"support@example.com","help":"The Zendesk agent email that owns the API token.","required":true,"secret":false},{"key":"apiToken","label":"Zendesk API token","type":"password","placeholder":"Zendesk API token","help":"Create an API token in Zendesk Admin Center → Apps and integrations → APIs → Zendesk API. The token is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    110, 'https://www.zendesk.com', 'https://support.zendesk.com', 'https://www.zendesk.com/legal/privacy-policy/', 'https://developer.zendesk.com', 'zendesk.svg', '1.0'
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('0049_addon_wave2_branding') ON CONFLICT DO NOTHING;

COMMIT;
