-- Migration 002: Server-side project password storage.
--
-- Previously the E2EE password lived only in ~/.dotsync/config.json on each
-- developer's machine (or DOTSYNC_PASSWORD in CI). That meant every new
-- machine required manually re-typing the password (`dotsync init
-- --rotate-password`), and losing the local file meant losing access.
--
-- This table lets the server hold the password so authenticated team
-- members can fetch it — but it is NEVER stored in plaintext. Each row is
-- AES-256-GCM ciphertext, encrypted with a key derived (via HMAC-SHA256)
-- from SERVER_MASTER_KEY + project_id. Only someone with SERVER_MASTER_KEY
-- (the server process itself) can ever decrypt it.
--
-- NOTE: this intentionally changes the trust model from pure end-to-end
-- encryption (only clients ever hold the key) to server-mediated
-- encryption (the server can decrypt on behalf of authorized members).
-- The .env payloads themselves (the `secrets` table) are unaffected and
-- remain E2EE — the server still never sees decrypted secrets.
CREATE TABLE project_passwords (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    encrypted_password  BYTEA NOT NULL,   -- AES-256-GCM ciphertext + auth tag
    password_nonce      BYTEA NOT NULL,   -- 12-byte GCM nonce (unique per rotation)
    updated_by          TEXT NOT NULL REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_project_passwords_project ON project_passwords(project_id);
