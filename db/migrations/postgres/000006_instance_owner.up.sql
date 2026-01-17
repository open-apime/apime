ALTER TABLE instances
  ADD COLUMN IF NOT EXISTS owner_user_id UUID REFERENCES users(id);

CREATE INDEX IF NOT EXISTS idx_instances_owner_user_id ON instances(owner_user_id);
