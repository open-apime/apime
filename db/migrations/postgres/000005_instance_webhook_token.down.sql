CREATE TABLE IF NOT EXISTS webhooks (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    secret TEXT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE instances
  ADD COLUMN IF NOT EXISTS webhook_id UUID REFERENCES webhooks(id);

CREATE INDEX IF NOT EXISTS idx_instances_webhook_id ON instances(webhook_id);

ALTER TABLE instances
  DROP COLUMN IF EXISTS webhook_url,
  DROP COLUMN IF EXISTS webhook_secret,
  DROP COLUMN IF EXISTS instance_token_hash,
  DROP COLUMN IF EXISTS instance_token_updated_at;
