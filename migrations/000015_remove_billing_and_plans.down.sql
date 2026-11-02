-- Restore the legacy billing model only when explicitly rolling back.
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan TEXT NOT NULL DEFAULT 'free'
    CHECK (plan IN ('free', 'pro', 'team', 'business'));

CREATE TABLE IF NOT EXISTS gift_cards (
    id              TEXT PRIMARY KEY,
    code_hash       TEXT NOT NULL UNIQUE,
    value_usd       INTEGER NOT NULL CHECK (value_usd > 0),
    grant_plan      TEXT NOT NULL CHECK (grant_plan IN ('free', 'pro', 'team', 'business')),
    max_redemptions INTEGER NOT NULL DEFAULT 1 CHECK (max_redemptions > 0),
    redeemed_count  INTEGER NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    expires_at      TIMESTAMPTZ,
    created_by      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS gift_card_redemptions (
    id            TEXT PRIMARY KEY,
    gift_card_id  TEXT NOT NULL REFERENCES gift_cards(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redeemed_plan TEXT NOT NULL CHECK (redeemed_plan IN ('free', 'pro', 'team', 'business')),
    value_usd     INTEGER NOT NULL CHECK (value_usd > 0),
    redeemed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (gift_card_id, user_id)
);
