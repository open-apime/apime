DROP INDEX IF EXISTS idx_instances_owner_user_id;

ALTER TABLE instances
  DROP COLUMN IF EXISTS owner_user_id;
