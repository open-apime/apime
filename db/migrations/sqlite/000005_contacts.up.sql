CREATE TABLE IF NOT EXISTS contacts (
    phone TEXT PRIMARY KEY,
    jid TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
