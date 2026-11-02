-- Migration 014: per-member project-key envelopes.

ALTER TABLE users
ADD COLUMN x25519_pubkey TEXT;

CREATE TABLE project_key_envelopes (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient_pubkey TEXT NOT NULL,
    ephemeral_pubkey TEXT NOT NULL,
    nonce           BYTEA NOT NULL,
    encrypted_key   BYTEA NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, user_id)
);

CREATE INDEX idx_project_key_envelopes_project ON project_key_envelopes(project_id);
