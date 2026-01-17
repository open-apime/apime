-- Rollback do schema SQLite

DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS audit_trail;
DROP TABLE IF EXISTS message_queue;
DROP TABLE IF EXISTS event_logs;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS device_config;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS schema_migrations;
