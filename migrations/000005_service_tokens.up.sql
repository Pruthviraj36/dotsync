-- Migration 005: Service tokens for CI/CD integrations
-- Service tokens are scoped to a project+environment, read-only (pull only),
-- and do not expire automatically (owners revoke them manually).
-- Only the SHA-256 hash is stored — the raw token is shown once to the user.

CREATE TABLE service_tokens (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    env         TEXT NOT NULL,            -- dev | staging | production | * (all envs)
    name        TEXT NOT NULL,            -- human label, e.g. "github-actions-prod"
    token_hash  TEXT NOT NULL UNIQUE,     -- SHA-256(raw_token), hex-encoded
    created_by  TEXT NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE INDEX idx_service_tokens_project ON service_tokens(project_id);
CREATE INDEX idx_service_tokens_hash    ON service_tokens(token_hash);
