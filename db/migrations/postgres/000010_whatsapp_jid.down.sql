-- Remove whatsapp_jid column from instances table
ALTER TABLE instances DROP COLUMN IF EXISTS whatsapp_jid;
