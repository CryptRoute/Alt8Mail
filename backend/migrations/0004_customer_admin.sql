-- Customer/tenant administration: admin identity, RBAC, groups, org units,
-- tenant policy, domain verification and immutable admin audit.
BEGIN;

ALTER TABLE tenants
  ADD COLUMN IF NOT EXISTS plan text NOT NULL DEFAULT 'business',
  ADD COLUMN IF NOT EXISTS region text NOT NULL DEFAULT 'us-east-1',
  ADD COLUMN IF NOT EXISTS billing_email citext,
  ADD COLUMN IF NOT EXISTS suspended_at timestamptz;

ALTER TABLE tenants
  DROP CONSTRAINT IF EXISTS tenants_plan_check;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_plan_check CHECK (plan IN ('starter', 'business', 'enterprise', 'education'));
ALTER TABLE tenants
  DROP CONSTRAINT IF EXISTS tenants_region_check;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_region_check CHECK (region IN ('us-east-1', 'us-west-2', 'eu-west-1', 'eu-central-1', 'ap-southeast-1'));

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS is_system_admin boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS department text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS employee_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS manager_email citext,
  ADD COLUMN IF NOT EXISTS force_password_change boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS password_expires_at timestamptz,
  ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

CREATE TABLE IF NOT EXISTS org_units (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  parent_id uuid REFERENCES org_units(id) ON DELETE RESTRICT,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  path text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS org_units_tenant_idx ON org_units (tenant_id);
CREATE INDEX IF NOT EXISTS org_units_parent_idx ON org_units (parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS org_units_sibling_name_idx
  ON org_units (tenant_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS org_unit_id uuid REFERENCES org_units(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS users_org_unit_idx ON users (org_unit_id);

ALTER TABLE mail_domains
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS verification_token text,
  ADD COLUMN IF NOT EXISTS verified_at timestamptz,
  ADD COLUMN IF NOT EXISTS mx_verified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS spf_verified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS dkim_verified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS dmarc_verified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS dmarc_policy text NOT NULL DEFAULT 'none',
  ADD COLUMN IF NOT EXISTS catch_all text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS last_checked_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';

UPDATE mail_domains
SET status = 'verified', verified_at = COALESCE(verified_at, created_at),
    mx_verified = true, spf_verified = true, dkim_verified = true, dmarc_verified = true
WHERE status = 'pending' AND domain = 'meridian.test';

CREATE TABLE IF NOT EXISTS admin_roles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  permissions text[] NOT NULL DEFAULT '{}',
  is_system boolean NOT NULL DEFAULT false,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS admin_roles_tenant_idx ON admin_roles (tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS admin_roles_name_idx ON admin_roles (tenant_id, lower(name));

CREATE TABLE IF NOT EXISTS admin_role_assignments (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id uuid NOT NULL REFERENCES admin_roles(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  assigned_by uuid REFERENCES users(id) ON DELETE SET NULL,
  expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, role_id)
);
CREATE INDEX IF NOT EXISTS admin_role_assignments_tenant_idx ON admin_role_assignments (tenant_id);

CREATE TABLE IF NOT EXISTS groups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL,
  email citext NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  group_type text NOT NULL DEFAULT 'distribution'
    CHECK (group_type IN ('distribution', 'security', 'shared')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS groups_tenant_idx ON groups (tenant_id);

CREATE TABLE IF NOT EXISTS group_members (
  group_id uuid NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  member_role text NOT NULL DEFAULT 'member' CHECK (member_role IN ('member', 'owner')),
  added_by uuid REFERENCES users(id) ON DELETE SET NULL,
  added_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS group_members_user_idx ON group_members (user_id);

CREATE TABLE IF NOT EXISTS admin_sessions (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  active_tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  csrf_token text NOT NULL,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS admin_sessions_user_idx ON admin_sessions (user_id);
CREATE INDEX IF NOT EXISTS admin_sessions_expiry_idx ON admin_sessions (expires_at);

CREATE TABLE IF NOT EXISTS admin_auth_challenges (
  id text PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  attempts integer NOT NULL DEFAULT 0,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS admin_auth_challenges_expiry_idx ON admin_auth_challenges (expires_at);

CREATE TABLE IF NOT EXISTS admin_audit_log (
  id bigserial PRIMARY KEY,
  tenant_id uuid REFERENCES tenants(id) ON DELETE SET NULL,
  actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
  actor_email citext NOT NULL DEFAULT '',
  action text NOT NULL,
  target text NOT NULL DEFAULT '',
  detail text NOT NULL DEFAULT '',
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS admin_audit_tenant_time_idx ON admin_audit_log (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS admin_audit_actor_time_idx ON admin_audit_log (actor_id, created_at DESC);

INSERT INTO admin_roles (tenant_id, name, description, permissions, is_system)
SELECT t.id, 'User administrator', 'Create and manage users, groups and directory details.',
       ARRAY['users.read','users.create','users.update','users.delete','users.reset_password','users.security','users.aliases','groups.read','groups.manage','orgunits.read','audit.read'], true
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM admin_roles r WHERE r.tenant_id = t.id AND lower(r.name) = 'user administrator');

INSERT INTO admin_roles (tenant_id, name, description, permissions, is_system)
SELECT t.id, 'Security administrator', 'Manage authentication policy, user security and sessions.',
       ARRAY['users.read','users.security','users.reset_password','sessions.manage','policies.read','policies.manage','roles.read','audit.read'], true
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM admin_roles r WHERE r.tenant_id = t.id AND lower(r.name) = 'security administrator');

INSERT INTO admin_roles (tenant_id, name, description, permissions, is_system)
SELECT t.id, 'Helpdesk', 'Reset passwords and unlock accounts without policy changes.',
       ARRAY['users.read','users.reset_password','users.security','sessions.manage','audit.read'], true
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM admin_roles r WHERE r.tenant_id = t.id AND lower(r.name) = 'helpdesk');

INSERT INTO admin_roles (tenant_id, name, description, permissions, is_system)
SELECT t.id, 'Auditor', 'Read-only access to users, policy, domains and audit.',
       ARRAY['tenant.read','users.read','groups.read','orgunits.read','roles.read','policies.read','domains.read','audit.read'], true
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM admin_roles r WHERE r.tenant_id = t.id AND lower(r.name) = 'auditor');

GRANT SELECT, INSERT, UPDATE, DELETE ON org_units, admin_roles, admin_role_assignments, groups, group_members,
  admin_sessions, admin_auth_challenges, admin_audit_log TO meridian_app;
GRANT USAGE, SELECT ON SEQUENCE admin_audit_log_id_seq TO meridian_app;
GRANT SELECT ON tenants, users, mail_domains, user_aliases, mailboxes, groups, group_members TO meridian_mail;

INSERT INTO schema_migrations (version) VALUES ('0004_customer_admin')
ON CONFLICT (version) DO NOTHING;

COMMIT;
