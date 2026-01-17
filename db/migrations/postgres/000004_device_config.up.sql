CREATE TABLE IF NOT EXISTS device_config (
    id UUID PRIMARY KEY,
    platform_type TEXT NOT NULL DEFAULT 'DESKTOP',
    device_name TEXT NOT NULL DEFAULT 'ApiMe Server',
    manufacturer TEXT NOT NULL DEFAULT 'DigiMind.Space',
    os_name TEXT NOT NULL DEFAULT 'ApiMe',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO device_config (id, platform_type, device_name, manufacturer, os_name)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'DESKTOP',
    'ApiMe Server',
    'DigiMind.Space',
    'ApiMe'
)
ON CONFLICT (id) DO NOTHING;
