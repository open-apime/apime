-- Add whatsapp_jid column to instances table
ALTER TABLE instances ADD COLUMN IF NOT EXISTS whatsapp_jid VARCHAR(255);
CREATE INDEX IF NOT EXISTS idx_instances_whatsapp_jid ON instances(whatsapp_jid);
