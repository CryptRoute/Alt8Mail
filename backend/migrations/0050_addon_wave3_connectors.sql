-- Add-on phase 4 / Wave 3: Salesforce, Freshdesk, Intercom, TickTick,
-- Calendly, Jira and Linear.
BEGIN;

INSERT INTO public.addon_system_catalog (
  id, name, vendor, category, description, tagline, icon, color, logo_asset,
  website_url, support_url, privacy_url, docs_url, connector_version,
  connection_scope, auth_type, self_service, requires_tenant_setup,
  scopes, permissions, actions, fields, sort_order
) VALUES
  (
    'salesforce', 'Salesforce', 'Salesforce, Inc.', 'CRM',
    'Log the selected email as a completed task on the matching Salesforce contact.',
    'Keep every customer conversation in Salesforce.',
    'i-people', '#00a1e0', 'salesforce.svg',
    'https://www.salesforce.com', 'https://help.salesforce.com',
    'https://www.salesforce.com/company/privacy/', 'https://developer.salesforce.com/docs', '1.0',
    'tenant', 'oauth2', false, true,
    '["api","refresh_token"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Find the sender as a Salesforce contact", "Create a completed task on that contact"]'::jsonb,
    '[{"id":"log-email","label":"Log email to Salesforce","description":"Create a task on the sender''s Salesforce contact.","surface":"reader"}]'::jsonb,
    '[{"key":"loginUrl","label":"Salesforce login","type":"select","default":"https://login.salesforce.com","required":true,"secret":false,"options":[{"value":"https://login.salesforce.com","label":"Production"},{"value":"https://test.salesforce.com","label":"Sandbox"}]},{"key":"clientId","label":"Connected app consumer key","type":"text","required":true,"secret":false,"placeholder":"Consumer key"},{"key":"clientSecret","label":"Connected app consumer secret","type":"password","required":true,"secret":true,"placeholder":"Consumer secret"},{"key":"refreshToken","label":"Salesforce refresh token","type":"password","required":true,"secret":true,"placeholder":"Refresh token","help":"Authorize a connected app with the api and refresh_token scopes, then paste its refresh token. The token is encrypted before it is stored."}]'::jsonb,
    120
  ),
  (
    'freshdesk', 'Freshdesk', 'Freshworks', 'Support',
    'Open a Freshdesk ticket from the email you are reading.',
    'Turn customer mail into a Freshdesk ticket.',
    'i-people', '#25c16f', 'freshdesk.svg',
    'https://www.freshworks.com/freshdesk/', 'https://support.freshdesk.com',
    'https://www.freshworks.com/privacy/', 'https://developers.freshdesk.com', '1.0',
    'tenant', 'api_key', false, true,
    '["tickets:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a ticket with the email as the initial description"]'::jsonb,
    '[{"id":"create-ticket","label":"Create Freshdesk ticket","description":"Open a ticket with the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"domain","label":"Freshdesk domain","type":"text","required":true,"secret":false,"placeholder":"acme","help":"Use acme when your help desk is acme.freshdesk.com."},{"key":"apiKey","label":"Freshdesk API key","type":"password","required":true,"secret":true,"placeholder":"Freshdesk API key","help":"Find your API key in Freshdesk Profile settings. The key is encrypted before it is stored."}]'::jsonb,
    130
  ),
  (
    'intercom', 'Intercom', 'Intercom, Inc.', 'Support',
    'Add an internal note to the matching Intercom contact.',
    'Keep customer context in Intercom without copying and pasting.',
    'i-people', '#1f8ded', 'intercom.svg',
    'https://www.intercom.com', 'https://help.intercom.com',
    'https://www.intercom.com/legal/privacy', 'https://developers.intercom.com', '1.0',
    'tenant', 'api_key', false, true,
    '["contacts:read","notes:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Find the sender as an Intercom contact", "Add an internal note to that contact"]'::jsonb,
    '[{"id":"add-note","label":"Add Intercom note","description":"Add a note to the sender''s Intercom contact.","surface":"reader"}]'::jsonb,
    '[{"key":"region","label":"Intercom region","type":"select","default":"us","required":true,"secret":false,"options":[{"value":"us","label":"United States"},{"value":"eu","label":"Europe"},{"value":"au","label":"Australia"}]},{"key":"accessToken","label":"Intercom access token","type":"password","required":true,"secret":true,"placeholder":"Intercom access token","help":"Create a private app access token in Intercom → Settings → Integrations. The token is encrypted before it is stored."}]'::jsonb,
    140
  ),
  (
    'ticktick', 'TickTick', 'TickTick', 'Tasks',
    'Create a TickTick task from the email you are reading.',
    'Capture an email as a TickTick task.',
    'i-check-circle', '#4772fa', 'ticktick.svg',
    'https://ticktick.com', 'https://help.ticktick.com',
    'https://ticktick.com/privacy', 'https://developer.ticktick.com', '1.0',
    'user', 'oauth2', true, false,
    '["tasks:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create a task in the connected TickTick project"]'::jsonb,
    '[{"id":"create-task","label":"Create TickTick task","description":"Create a task from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"accessToken","label":"TickTick access token","type":"password","required":true,"secret":true,"placeholder":"TickTick OAuth access token","help":"Create an app in the TickTick developer console and paste its OAuth access token. The token is encrypted before it is stored."},{"key":"projectId","label":"TickTick project ID","type":"text","required":true,"secret":false,"placeholder":"Project ID","help":"The project ID where new tasks should be created."}]'::jsonb,
    150
  ),
  (
    'calendly', 'Calendly', 'Calendly LLC', 'Scheduling',
    'Create a single-use Calendly booking link from an email.',
    'Offer a booking link without leaving the conversation.',
    'i-calendar-plus', '#006bff', 'calendly.svg',
    'https://calendly.com', 'https://help.calendly.com',
    'https://calendly.com/privacy', 'https://developer.calendly.com', '1.0',
    'user', 'api_key', true, false,
    '["scheduling_links:write"]'::jsonb,
    '["Read the selected email''s subject and sender", "Create a single-use scheduling link for your Calendly event"]'::jsonb,
    '[{"id":"create-link","label":"Create Calendly link","description":"Create a single-use booking link for this email.","surface":"reader"}]'::jsonb,
    '[{"key":"accessToken","label":"Calendly personal access token","type":"password","required":true,"secret":true,"placeholder":"Calendly access token","help":"Create a personal access token in Calendly integrations. The token is encrypted before it is stored."},{"key":"eventTypeUri","label":"Calendly event type URI","type":"text","required":true,"secret":false,"placeholder":"https://api.calendly.com/event_types/…"},{"key":"ownerUri","label":"Calendly owner URI","type":"text","required":true,"secret":false,"placeholder":"https://api.calendly.com/users/…"}]'::jsonb,
    160
  ),
  (
    'jira', 'Jira', 'Atlassian', 'Engineering',
    'Create a Jira issue from the email you are reading.',
    'Turn customer mail into a tracked Jira issue.',
    'i-check-circle', '#0052cc', 'jira.svg',
    'https://www.atlassian.com/software/jira', 'https://support.atlassian.com/jira-software-cloud/',
    'https://www.atlassian.com/legal/privacy-policy', 'https://developer.atlassian.com/cloud/jira/platform/rest/v3/', '1.0',
    'tenant', 'api_key', false, true,
    '["issues:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create an issue in the configured Jira project"]'::jsonb,
    '[{"id":"create-issue","label":"Create Jira issue","description":"Create an issue from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"site","label":"Jira Cloud site","type":"text","required":true,"secret":false,"placeholder":"acme.atlassian.net","help":"Use the hostname of your Jira Cloud site, without https://."},{"key":"email","label":"Atlassian account email","type":"text","required":true,"secret":false,"placeholder":"you@example.com"},{"key":"apiToken","label":"Atlassian API token","type":"password","required":true,"secret":true,"placeholder":"Atlassian API token","help":"Create an API token in your Atlassian account security settings. The token is encrypted before it is stored."},{"key":"projectKey","label":"Jira project key","type":"text","required":true,"secret":false,"placeholder":"ENG"},{"key":"issueType","label":"Issue type","type":"text","default":"Task","required":true,"secret":false,"placeholder":"Task"}]'::jsonb,
    170
  ),
  (
    'linear', 'Linear', 'Linear', 'Engineering',
    'Create a Linear issue from the email you are reading.',
    'Move an email into the team''s Linear backlog.',
    'i-check-circle', '#5e6ad2', 'linear.svg',
    'https://linear.app', 'https://linear.app/contact/support',
    'https://linear.app/privacy', 'https://developers.linear.app', '1.0',
    'tenant', 'api_key', false, true,
    '["issues:write"]'::jsonb,
    '["Read the selected email''s subject, sender and preview", "Create an issue in the configured Linear team"]'::jsonb,
    '[{"id":"create-issue","label":"Create Linear issue","description":"Create an issue from the selected email.","surface":"reader"}]'::jsonb,
    '[{"key":"apiKey","label":"Linear API key","type":"password","required":true,"secret":true,"placeholder":"lin_api_…","help":"Create a personal API key in Linear → Settings → API. The key is encrypted before it is stored."},{"key":"teamId","label":"Linear team ID","type":"text","required":true,"secret":false,"placeholder":"Team UUID","help":"Find the team ID in Linear team settings or the API."}]'::jsonb,
    180
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('0050_addon_wave3_connectors') ON CONFLICT DO NOTHING;

COMMIT;
