-- Remove campos não utilizados da tabela device_config
-- DeviceName e Manufacturer têm impacto mínimo pois são restaurados após 10 segundos

ALTER TABLE device_config DROP COLUMN IF EXISTS device_name;
ALTER TABLE device_config DROP COLUMN IF EXISTS manufacturer;
