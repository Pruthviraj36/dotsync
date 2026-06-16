-- Migration 001: Initial schema for DotSync
-- All timestamps are UTC. All IDs are UUID strings.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Users (synced from GitHub OAuth)
CREATE TABLE users (
    id                    TEXT PRIMARY KEY,
    github_id             BIGINT UNIQUE NOT NULL,
    username              TEXT NOT NULL,
    email                 TEXT NOT NULL DEFAULT '',
    avatar_url            TEXT NOT NULL DEFAULT '',
    plan                  TEXT NOT NULL DEFAULT 'free' CHECK (plan IN ('free', 'pro', 'team', 'business')),
    stripe_customer_id    TEXT UNIQUE,
    stripe_subscription_id TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_github_id ON users(github_id);
CREATE INDEX idx_users_stripe_customer ON users(stripe_customer_id);

-- Refresh tokens (stored as SHA-256 hashes — never raw)
CREATE TABLE refresh_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX idx_refresh_tokens_hash ON refresh_tokens(token_hash);

-- Projects
CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_projects_owner ON projects(owner_id);
CREATE INDEX idx_projects_slug ON projects(slug);

-- Environments (dev / staging / production per project)
CREATE TABLE environments (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL CHECK (name IN ('dev', 'staging', 'production')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);

CREATE INDEX idx_environments_project ON environments(project_id);

-- Team members
CREATE TABLE team_members (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    invited_by  TEXT REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, user_id)
);

CREATE INDEX idx_team_members_project ON team_members(project_id);
CREATE INDEX idx_team_members_user ON team_members(user_id);

-- Secrets (append-only versioned encrypted blobs)
-- The server NEVER sees plaintext. Only AES-256-GCM ciphertext + nonce are stored.
CREATE TABLE secrets (
    id              TEXT PRIMARY KEY,
    environment_id  TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    encrypted_data  BYTEA NOT NULL,   -- AES-256-GCM ciphertext + auth tag
    data_nonce      BYTEA NOT NULL,   -- 12-byte GCM nonce (unique per version)
    version         INTEGER NOT NULL,
    pushed_by       TEXT NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (environment_id, version)
);

CREATE INDEX idx_secrets_environment ON secrets(environment_id);
CREATE INDEX idx_secrets_version ON secrets(environment_id, version DESC);

-- Audit logs (immutable, never deleted)
CREATE TABLE audit_logs (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id),
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id  TEXT REFERENCES environments(id),
    action          TEXT NOT NULL,       -- push | pull | invite | revoke | login | logout
    metadata        JSONB NOT NULL DEFAULT '{}',
    ip_address      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_logs_project ON audit_logs(project_id);
CREATE INDEX idx_audit_logs_user ON audit_logs(user_id);
CREATE INDEX idx_audit_logs_created ON audit_logs(created_at DESC);
