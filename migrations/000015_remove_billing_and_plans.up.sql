-- Remove the obsolete application billing and plan model.
DROP TABLE IF EXISTS gift_card_redemptions;
DROP TABLE IF EXISTS gift_cards;
ALTER TABLE users DROP COLUMN IF EXISTS plan;
