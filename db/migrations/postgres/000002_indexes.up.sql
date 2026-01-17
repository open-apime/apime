CREATE INDEX IF NOT EXISTS idx_instances_status ON instances(status);
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_schema = 'public'
      AND table_name = 'instances'
      AND column_name = 'webhook_id'
  ) THEN
    CREATE INDEX IF NOT EXISTS idx_instances_webhook_id ON instances(webhook_id);
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_event_logs_instance_id ON event_logs(instance_id);
CREATE INDEX IF NOT EXISTS idx_event_logs_created_at ON event_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_message_queue_instance_id ON message_queue(instance_id);
CREATE INDEX IF NOT EXISTS idx_message_queue_status ON message_queue(status);
CREATE INDEX IF NOT EXISTS idx_audit_trail_actor ON audit_trail(actor);
CREATE INDEX IF NOT EXISTS idx_audit_trail_created_at ON audit_trail(created_at);

