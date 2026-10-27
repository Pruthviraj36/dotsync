-- Migration 010: Indexes for the paginated read paths.
--
-- Audit logs are read newest-first per project with LIMIT/OFFSET. A plain
-- (project_id) index still forces Postgres to sort every matching row; the
-- composite index lets it walk rows in order and stop at the LIMIT, keeping
-- dashboard/audit latency flat as history grows.
CREATE INDEX IF NOT EXISTS idx_audit_logs_project_created
    ON audit_logs (project_id, created_at DESC);

-- idx_secrets_environment is a strict prefix of idx_secrets_version
-- (environment_id, version DESC), so it costs a write on every push without
-- enabling any query the composite index cannot serve.
DROP INDEX IF EXISTS idx_secrets_environment;
