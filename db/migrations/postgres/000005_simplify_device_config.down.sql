-- Rollback: restaurar campos removidos

ALTER TABLE device_config ADD COLUMN IF NOT EXISTS device_name TEXT NOT NULL DEFAULT 'ApiMe Server';
ALTER TABLE device_config ADD COLUMN IF NOT EXISTS manufacturer TEXT NOT NULL DEFAULT 'DigiMind.Space';
