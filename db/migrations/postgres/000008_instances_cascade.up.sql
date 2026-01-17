ALTER TABLE instances
DROP CONSTRAINT IF EXISTS instances_owner_user_id_fkey;

ALTER TABLE instances
ADD CONSTRAINT instances_owner_user_id_fkey
FOREIGN KEY (owner_user_id)
REFERENCES users(id)
ON DELETE CASCADE;
