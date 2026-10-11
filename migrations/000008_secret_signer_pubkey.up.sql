-- Migration 008: Snapshot the signing key used for each secret version
-- Historical signatures must remain verifiable after a user rotates keys.

ALTER TABLE secrets
ADD COLUMN signer_pubkey TEXT;

COMMENT ON COLUMN secrets.signer_pubkey IS
'Hex-encoded Ed25519 public key that signed this exact secret version. NULL for legacy unsigned versions.';
