-- Phase 2: purchases gain a quantity; add helpful indexes for webhook lookups.

ALTER TABLE purchases
    ADD COLUMN IF NOT EXISTS quantity INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0);

-- The webhook handler looks purchases up by the Stripe payment-intent id.
CREATE INDEX IF NOT EXISTS idx_purchases_intent ON purchases(stripe_payment_intent_id);
