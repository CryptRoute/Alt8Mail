-- Staging-only directory recipients. These accounts have no web password and
-- cannot sign in; they exist so Postfix/Dovecot accept delivery for the seeded
-- directory addresses used by Tasks and compose testing.
WITH t AS (
  SELECT tenant_id FROM users WHERE email = 'avery.chen@meridian.test'
)
INSERT INTO users (tenant_id, email, name, initials, color, role, status, preferences, security, quota_bytes)
SELECT t.tenant_id, v.email, v.name, v.initials, v.color, 'member', 'active', '{}'::jsonb, '{}'::jsonb, 10737418240
FROM t, (VALUES
  ('elena.vasquez@meridian.test', 'Elena Vasquez', 'EV', '#34557a'),
  ('marcus.webb@meridian.test', 'Marcus Webb', 'MW', '#3d6b62'),
  ('nora.patel@meridian.test', 'Nora Patel', 'NP', '#5b4a7e'),
  ('priya.shah@meridian.test', 'Priya Shah', 'PS', '#7a4a5c'),
  ('tomas.ribeiro@meridian.test', 'Tomás Ribeiro', 'TR', '#7a5a3a'),
  ('lena.fischer@meridian.test', 'Lena Fischer', 'LF', '#3f6570'),
  ('daniel.kim@meridian.test', 'Daniel Kim', 'DK', '#476b4f'),
  ('aisha.bello@meridian.test', 'Aisha Bello', 'AB', '#4f5673')
) AS v(email, name, initials, color)
ON CONFLICT (email) DO NOTHING;

INSERT INTO mailboxes (tenant_id, user_id, email, domain, local_part, maildir_path, active)
SELECT u.tenant_id, u.id, u.email, 'meridian.test', split_part(u.email, '@', 1),
       '/var/vmail/meridian.test/' || split_part(u.email, '@', 1) || '/Maildir', true
FROM users u
WHERE u.email LIKE '%@meridian.test' AND u.email <> 'avery.chen@meridian.test'
ON CONFLICT (email) DO NOTHING;
