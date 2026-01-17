-- Email: admin@apime.local
-- Senha (Hash bcrypt): admin123

INSERT INTO users (id, email, password_hash, role, created_at)
SELECT 
    '00000000-0000-0000-0000-000000000001',
    'admin@apime.local',
    '$2b$10$.QLh2pBoW1Xvuh.ri91VY.oafvAXW0adCxCkcNZJZZ.9qY9Gu3OBa',
    'admin',
    now()
WHERE NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin');
