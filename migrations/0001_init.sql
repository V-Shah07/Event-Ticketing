-- Phase 1: core schema
-- Money is always stored in integer cents.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

DO $$ BEGIN
    CREATE TYPE user_role AS ENUM ('organizer', 'buyer', 'admin');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE event_state AS ENUM ('draft', 'published', 'ended');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE purchase_status AS ENUM ('pending', 'completed', 'failed');
EXCEPTION WHEN duplicate_object THEN null; END $$;

DO $$ BEGIN
    CREATE TYPE ticket_status AS ENUM ('valid', 'scanned', 'refunded');
EXCEPTION WHEN duplicate_object THEN null; END $$;

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          user_role NOT NULL DEFAULT 'buyer',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organizer_id UUID NOT NULL REFERENCES users(id),
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT 'general',
    venue        TEXT NOT NULL DEFAULT '',
    lat          DOUBLE PRECISION,
    lng          DOUBLE PRECISION,
    state        event_state NOT NULL DEFAULT 'draft',
    starts_at    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_events_organizer ON events(organizer_id);
CREATE INDEX IF NOT EXISTS idx_events_state ON events(state);

CREATE TABLE IF NOT EXISTS ticket_tiers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id    UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
    capacity    INTEGER NOT NULL CHECK (capacity >= 0),
    sold        INTEGER NOT NULL DEFAULT 0 CHECK (sold >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT no_oversell CHECK (sold <= capacity)
);

CREATE INDEX IF NOT EXISTS idx_tiers_event ON ticket_tiers(event_id);

CREATE TABLE IF NOT EXISTS purchases (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    buyer_id                 UUID NOT NULL REFERENCES users(id),
    event_id                 UUID NOT NULL REFERENCES events(id),
    tier_id                  UUID NOT NULL REFERENCES ticket_tiers(id),
    stripe_payment_intent_id TEXT NOT NULL UNIQUE,
    amount_cents             BIGINT NOT NULL CHECK (amount_cents >= 0),
    status                   purchase_status NOT NULL DEFAULT 'pending',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_purchases_buyer ON purchases(buyer_id);

CREATE TABLE IF NOT EXISTS tickets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id     UUID NOT NULL REFERENCES ticket_tiers(id),
    event_id    UUID NOT NULL REFERENCES events(id),
    buyer_id    UUID NOT NULL REFERENCES users(id),
    purchase_id UUID NOT NULL REFERENCES purchases(id),
    qr_token    TEXT UNIQUE,
    status      ticket_status NOT NULL DEFAULT 'valid',
    scanned_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tickets_purchase ON tickets(purchase_id);
CREATE INDEX IF NOT EXISTS idx_tickets_qr ON tickets(qr_token);
