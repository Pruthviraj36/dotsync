-- Migration 013: retain audit history when a project is deleted.

ALTER TABLE audit_logs
    ALTER COLUMN project_id DROP NOT NULL;

ALTER TABLE audit_logs
    DROP CONSTRAINT IF EXISTS audit_logs_project_id_fkey,
    ADD CONSTRAINT audit_logs_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE SET NULL;
