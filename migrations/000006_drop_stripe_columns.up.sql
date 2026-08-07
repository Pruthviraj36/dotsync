-- Migration 006: Remove Stripe/payment columns
-- DotSync is fully free. No payment provider is used.
-- These columns are dead weight and potentially confusing in logs/backups.

ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;
ALTER TABLE users DROP COLUMN IF EXISTS stripe_subscription_id;
DROP INDEX IF EXISTS idx_users_stripe_customer;
