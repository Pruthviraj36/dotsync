-- Migration 003: per-user ed25519 identities + signed pushes.
--
-- Every dotsync client now has a local ed25519 keypair (~/.dotsync/id_ed25519).
-- The public half is uploaded here so teammates can verify who actually
-- pushed a given version — AES-256-GCM already gives you confidentiality
-- and tamper-evidence, this adds authenticity (proof of *who*, not just
-- "unmodified since encryption").
ALTER TABLE users ADD COLUMN ed25519_pubkey TEXT;

-- The signature covers SHA-256(ciphertext) and is produced client-side with
-- the pusher's private key, which never leaves their machine.
ALTER TABLE secrets ADD COLUMN signature BYTEA;
