-- Migration 008 rollback: remove per-version signing key snapshots

ALTER TABLE secrets
DROP COLUMN signer_pubkey;
