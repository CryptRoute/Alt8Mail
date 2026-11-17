-- Calendar rooms and resources are personal to the account that created them.
-- Early releases seeded Conference Room A, Board Room and a portable projector
-- for every tenant. Those platform rows have no created_by owner and are no
-- longer shown; user-created rows keep their owner.
BEGIN;

UPDATE calendar_resources
SET active = false, updated_at = now()
WHERE created_by IS NULL AND active = true;

CREATE INDEX IF NOT EXISTS calendar_resources_owner_idx
  ON calendar_resources (tenant_id, created_by)
  WHERE active = true;

COMMIT;
