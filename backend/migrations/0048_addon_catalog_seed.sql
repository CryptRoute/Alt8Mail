-- Seed the first-party add-on catalog so System Control can review and enable
-- add-ons before the first webmail request populates it at runtime. The
-- webmail API upserts the same definitions, so this is the initial snapshot.
BEGIN;

INSERT INTO public.addon_system_catalog (
  id, name, vendor, category, description, icon, color, connection_scope,
  auth_type, self_service, requires_tenant_setup, scopes, permissions, actions,
  fields, sort_order
) VALUES
  (
    'deepl', 'DeepL', 'DeepL SE', 'Translation',
    'Translate an email into your preferred language without leaving the reader.',
    'i-translate', '#0f2b46', 'user', 'api_key', true, false,
    '["translate:text"]'::jsonb,
    '["Read the selected email''s subject and body", "Send that text to DeepL for translation"]'::jsonb,
    '[{"id":"translate","label":"Translate email","description":"Translate the selected message with DeepL.","surface":"reader"}]'::jsonb,
    '[{"key":"apiKey","label":"DeepL API key","type":"password","placeholder":"DeepL API key","help":"Free API keys end in :fx. The key is encrypted before it is stored.","required":true,"secret":true},{"key":"targetLanguage","label":"Translate into","type":"select","default":"EN-US","required":true,"secret":false,"options":[{"value":"EN-US","label":"English (US)"},{"value":"EN-GB","label":"English (UK)"},{"value":"ES","label":"Spanish"},{"value":"FR","label":"French"},{"value":"DE","label":"German"},{"value":"IT","label":"Italian"},{"value":"PT-BR","label":"Portuguese (Brazil)"},{"value":"NL","label":"Dutch"},{"value":"JA","label":"Japanese"},{"value":"KO","label":"Korean"},{"value":"ZH","label":"Chinese"},{"value":"AR","label":"Arabic"},{"value":"RU","label":"Russian"}]}]'::jsonb,
    10
  ),
  (
    'todoist', 'Todoist', 'Doist', 'Tasks',
    'Turn the email you are reading into a Todoist task with its sender and context attached.',
    'i-check-circle', '#e44332', 'user', 'api_key', true, false,
    '["tasks:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a task in your Todoist account"]'::jsonb,
    '[{"id":"create-task","label":"Create Todoist task","description":"Create a task from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"apiKey","label":"Todoist API token","type":"password","placeholder":"Paste your Todoist API token","help":"Create a token in Todoist → Settings → Integrations → Developer. The token is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    20
  ),
  (
    'slack', 'Slack', 'Slack Technologies', 'Communication',
    'Post a clean summary of an email into the Slack channel your workspace has connected.',
    'i-people', '#4a154b', 'tenant', 'webhook', false, true,
    '["chat:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Post that summary to the configured Slack channel"]'::jsonb,
    '[{"id":"send-to-channel","label":"Send to Slack","description":"Post the selected email summary to Slack.","surface":"reader"}]'::jsonb,
    '[{"key":"webhookUrl","label":"Slack incoming webhook URL","type":"password","placeholder":"https://hooks.slack.com/services/…","help":"Create an Incoming Webhook in your Slack app, choose its channel, and paste the URL here. The URL is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    30
  ),
  (
    'hubspot', 'HubSpot', 'HubSpot, Inc.', 'CRM',
    'Log the selected email to the matching HubSpot contact''s CRM timeline.',
    'i-people', '#ff7a59', 'tenant', 'api_key', false, true,
    '["crm.objects.contacts.read", "crm.objects.contacts.write", "crm.objects.notes.write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Look up the sender as a HubSpot contact", "Create a note on that contact''s timeline"]'::jsonb,
    '[{"id":"log-email","label":"Log email to HubSpot","description":"Create a HubSpot note on the sender''s contact record.","surface":"reader"}]'::jsonb,
    '[{"key":"accessToken","label":"HubSpot private app access token","type":"password","placeholder":"pat-…","help":"Create a private app with contact read/write and notes write scopes. The token is encrypted before it is stored.","required":true,"secret":true}]'::jsonb,
    40
  ),
  (
    'asana', 'Asana', 'Asana, Inc.', 'Tasks',
    'Create an Asana task from the selected email in the project your workspace connects.',
    'i-check-circle', '#f06a6a', 'user', 'api_key', true, false,
    '["tasks:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a task in your connected Asana project"]'::jsonb,
    '[{"id":"create-task","label":"Create Asana task","description":"Create a task from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"apiKey","label":"Asana personal access token","type":"password","placeholder":"Asana PAT","help":"Create a personal access token in Asana → Settings → Apps. The token is encrypted before it is stored.","required":true,"secret":true},{"key":"projectGid","label":"Project GID","type":"text","placeholder":"1201234567890123","help":"Open the Asana project and copy the numeric ID from its URL or project settings.","required":true,"secret":false}]'::jsonb,
    50
  ),
  (
    'pagerduty', 'PagerDuty', 'PagerDuty, Inc.', 'Operations',
    'Raise a PagerDuty incident from the email you are reading, with the sender and preview attached.',
    'i-alert', '#06ac38', 'tenant', 'api_key', false, true,
    '["events:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Send a trigger event to the configured PagerDuty routing key"]'::jsonb,
    '[{"id":"trigger-incident","label":"Trigger PagerDuty incident","description":"Send an incident trigger from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"routingKey","label":"PagerDuty integration routing key","type":"password","placeholder":"Routing key","help":"Create an Events API v2 integration in PagerDuty and paste its routing key. The key is encrypted before it is stored.","required":true,"secret":true},{"key":"severity","label":"Default severity","type":"select","default":"error","required":true,"secret":false,"options":[{"value":"critical","label":"Critical"},{"value":"error","label":"Error"},{"value":"warning","label":"Warning"},{"value":"info","label":"Info"}]}]'::jsonb,
    60
  ),
  (
    'zoom', 'Zoom', 'Zoom Communications', 'Meetings',
    'Create a Zoom meeting link from an email and copy it into the conversation.',
    'i-calendar-plus', '#2d8cff', 'tenant', 'oauth2', false, true,
    '["meeting:write"]'::jsonb,
    '["Read the selected email''s subject", "Create a Zoom meeting under the connected account"]'::jsonb,
    '[{"id":"create-meeting","label":"Create Zoom meeting","description":"Create a meeting link for the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"accountId","label":"Zoom account ID","type":"text","placeholder":"Zoom account ID","required":true,"secret":false},{"key":"clientId","label":"Zoom client ID","type":"text","placeholder":"Zoom app client ID","required":true,"secret":false},{"key":"clientSecret","label":"Zoom client secret","type":"password","placeholder":"Zoom app client secret","required":true,"secret":true},{"key":"duration","label":"Default meeting length","type":"select","default":"30","required":true,"secret":false,"options":[{"value":"30","label":"30 minutes"},{"value":"45","label":"45 minutes"},{"value":"60","label":"60 minutes"}]}]'::jsonb,
    70
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('0048_addon_catalog_seed') ON CONFLICT DO NOTHING;

COMMIT;
