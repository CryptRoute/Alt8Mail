-- Phase 3 template gallery and payment-link policy inputs.
BEGIN;

CREATE TABLE IF NOT EXISTS public.template_gallery (
  id text PRIMARY KEY,
  name text NOT NULL,
  category text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  subject text NOT NULL DEFAULT '',
  body_text text NOT NULL DEFAULT '',
  body_html text NOT NULL DEFAULT '',
  variables text[] NOT NULL DEFAULT '{}',
  sort_order integer NOT NULL DEFAULT 100,
  version integer NOT NULL DEFAULT 1,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO public.template_gallery (id, name, category, description, subject, body_text, body_html, variables, sort_order)
VALUES
  ('gallery-client-intro', 'Client introduction', 'Sales',
   'A short introduction with space for the client and company name.',
   'Hello {{client_name}} — good to meet you',
   'Hi {{client_name}},

Thanks for taking the time to speak with us. I have attached the overview we discussed for {{company_name}}.

If anything needs a closer look, reply here and I will help.

Best,
{{sender_name}}',
   '<p>Hi {{client_name}},</p><p>Thanks for taking the time to speak with us. I have attached the overview we discussed for {{company_name}}.</p><p>If anything needs a closer look, reply here and I will help.</p><p>Best,<br>{{sender_name}}</p>',
   ARRAY['client_name','company_name','sender_name'], 10),
  ('gallery-invoice-reminder', 'Invoice reminder', 'Billing',
   'A polite reminder for an outstanding invoice.',
   'Reminder: invoice {{invoice_number}} is outstanding',
   'Hi {{client_name}},

A friendly reminder that invoice {{invoice_number}} for {{amount}} was due on {{due_date}}. You can review it here: {{invoice_url}}.

If payment is already on its way, please ignore this note.

Thank you,
{{sender_name}}',
   '<p>Hi {{client_name}},</p><p>A friendly reminder that invoice {{invoice_number}} for {{amount}} was due on {{due_date}}. You can review it here: <a href="{{invoice_url}}">{{invoice_url}}</a>.</p><p>If payment is already on its way, please ignore this note.</p><p>Thank you,<br>{{sender_name}}</p>',
   ARRAY['client_name','invoice_number','amount','due_date','invoice_url','sender_name'], 20),
  ('gallery-contract-followup', 'Contract follow-up', 'Legal',
   'Follow up on a contract that is waiting for signature.',
   'Following up on {{contract_name}}',
   'Hi {{client_name}},

I wanted to check whether you had a chance to review {{contract_name}}. The signature link is here: {{signature_url}}.

Happy to walk through any clause if that is useful.

Best,
{{sender_name}}',
   '<p>Hi {{client_name}},</p><p>I wanted to check whether you had a chance to review {{contract_name}}. The signature link is here: <a href="{{signature_url}}">{{signature_url}}</a>.</p><p>Happy to walk through any clause if that is useful.</p><p>Best,<br>{{sender_name}}</p>',
   ARRAY['client_name','contract_name','signature_url','sender_name'], 30),
  ('gallery-meeting-recap', 'Meeting recap', 'Operations',
   'Summarize a meeting and the agreed next steps.',
   'Recap and next steps from {{meeting_name}}',
   'Hi {{client_name}},

Thanks for the discussion about {{meeting_name}}. Here is the short recap:
{{recap}}

Next steps:
{{next_steps}}

Please reply if anything is missing.

Best,
{{sender_name}}',
   '<p>Hi {{client_name}},</p><p>Thanks for the discussion about {{meeting_name}}. Here is the short recap:</p><p>{{recap}}</p><p>Next steps:</p><p>{{next_steps}}</p><p>Please reply if anything is missing.</p><p>Best,<br>{{sender_name}}</p>',
   ARRAY['client_name','meeting_name','recap','next_steps','sender_name'], 40)
ON CONFLICT (id) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON public.template_gallery TO a8m_app;

INSERT INTO schema_migrations (version) VALUES ('0073_template_gallery')
ON CONFLICT DO NOTHING;

COMMIT;
