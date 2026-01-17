ALTER TABLE instances
  ADD COLUMN IF NOT EXISTS webhook_url TEXT,
  ADD COLUMN IF NOT EXISTS webhook_secret TEXT,
  ADD COLUMN IF NOT EXISTS instance_token_hash TEXT UNIQUE,
  ADD COLUMN IF NOT EXISTS instance_token_updated_at TIMESTAMPTZ;

DROP INDEX IF EXISTS idx_instances_webhook_id;

ALTER TABLE instances
  DROP COLUMN IF EXISTS webhook_id;

DROP TABLE IF EXISTS webhooks;
