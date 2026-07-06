-- Migration 003: Gift cards for plan unlocks.
--
-- Gift cards are stored as hashed codes (SHA-256) so raw codes are never kept
-- in plaintext in the DB. Redeeming a valid code upgrades the caller's plan.
CREATE TABLE gift_cards (
    id               TEXT PRIMARY KEY,
    code_hash        TEXT NOT NULL UNIQUE,
    value_usd        INTEGER NOT NULL CHECK (value_usd > 0),
    grant_plan       TEXT NOT NULL CHECK (grant_plan IN ('free', 'pro', 'team', 'business')),
    max_redemptions  INTEGER NOT NULL DEFAULT 1 CHECK (max_redemptions > 0),
    redeemed_count   INTEGER NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    expires_at       TIMESTAMPTZ,
    created_by       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_gift_cards_code_hash ON gift_cards(code_hash);
CREATE INDEX idx_gift_cards_created_by ON gift_cards(created_by);

CREATE TABLE gift_card_redemptions (
    id            TEXT PRIMARY KEY,
    gift_card_id  TEXT NOT NULL REFERENCES gift_cards(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redeemed_plan TEXT NOT NULL CHECK (redeemed_plan IN ('free', 'pro', 'team', 'business')),
    value_usd     INTEGER NOT NULL CHECK (value_usd > 0),
    redeemed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (gift_card_id, user_id)
);

CREATE INDEX idx_gift_redemptions_user ON gift_card_redemptions(user_id);
CREATE INDEX idx_gift_redemptions_card ON gift_card_redemptions(gift_card_id);
