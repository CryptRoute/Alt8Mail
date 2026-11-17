-- Staging-only directory recipients. These accounts have no web password and
-- cannot sign in; they exist so Postfix/Dovecot accept delivery for the seeded
-- directory addresses used by Tasks and compose testing.
WITH t AS (
  SELECT tenant_id FROM users WHERE email = 'avery.chen@a8m.test'
)
INSERT INTO users (tenant_id, email, name, initials, color, role, status, preferences, security, quota_bytes)
SELECT t.tenant_id, v.email, v.name, v.initials, v.color, 'member', 'active', '{}'::jsonb, '{}'::jsonb, 10737418240
FROM t, (VALUES
  ('elena.vasquez@a8m.test', 'Elena Vasquez', 'EV', '#34557a'),
  ('marcus.webb@a8m.test', 'Marcus Webb', 'MW', '#3d6b62'),
  ('nora.patel@a8m.test', 'Nora Patel', 'NP', '#5b4a7e'),
  ('priya.shah@a8m.test', 'Priya Shah', 'PS', '#7a4a5c'),
  ('tomas.ribeiro@a8m.test', 'Tomás Ribeiro', 'TR', '#7a5a3a'),
  ('lena.fischer@a8m.test', 'Lena Fischer', 'LF', '#3f6570'),
  ('daniel.kim@a8m.test', 'Daniel Kim', 'DK', '#476b4f'),
  ('aisha.bello@a8m.test', 'Aisha Bello', 'AB', '#4f5673')
) AS v(email, name, initials, color)
ON CONFLICT (email) DO NOTHING;

INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
SELECT u.tenant_id, u.id, u.email, 'a8m.test', split_part(u.email, '@', 1),
       '/var/vmail/a8m.test/' || split_part(u.email, '@', 1) || '/Maildir', true
FROM users u
WHERE u.email LIKE '%@a8m.test' AND u.email <> 'avery.chen@a8m.test'
ON CONFLICT (email) DO NOTHING;
