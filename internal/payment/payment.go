// Package payment handles checkout (Stripe payment-intent creation) and the
// idempotent webhook that turns a succeeded payment into tickets + inventory.
//
// Idempotency is defended at two layers:
//  1. A Redis key (the Stripe payment-intent ID) claimed via SETNX — the fast
//     path that lets duplicate deliveries return success without touching the DB.
//  2. A `SELECT ... FOR UPDATE` on the purchase row plus a status check — the
//     authoritative guarantee: even if Redis is bypassed or fails, concurrent
//     webhook processors serialize and exactly one creates the tickets.
package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/v-shah07/event-ticketing/internal/events"
	"github.com/v-shah07/event-ticketing/internal/inventory"
)

var (
	// ErrPurchaseNotFound means the webhook arrived before checkout persisted
	// the purchase; Stripe will retry.
	ErrPurchaseNotFound = errors.New("purchase not found for intent")
	// ErrSoldOut means the tier cannot satisfy the purchased quantity.
	ErrSoldOut = errors.New("tier sold out")
)

type Service struct {
	pool      *pgxpool.Pool
	rdb       *redis.Client
	provider  Provider
	idempo    *idempotencyStore
	inv       *inventory.Cache
	publisher events.Publisher // optional analytics sink (gRPC + Kafka)
}

func NewService(pool *pgxpool.Pool, rdb *redis.Client, provider Provider) *Service {
	return &Service{
		pool:     pool,
		rdb:      rdb,
		provider: provider,
		idempo:   newIdempotencyStore(rdb),
		inv:      inventory.NewCache(pool, rdb),
	}
}

// SetPublisher attaches an analytics publisher, invoked best-effort after each
// committed purchase. Nil is fine (analytics simply isn't fed).
func (s *Service) SetPublisher(p events.Publisher) { s.publisher = p }

type CheckoutInput struct {
	TierID   string `json:"tier_id"`
	Quantity int    `json:"quantity"`
}

type CheckoutResult struct {
	PurchaseID   string `json:"purchase_id"`
	IntentID     string `json:"payment_intent_id"`
	ClientSecret string `json:"client_secret"`
	AmountCents  int64  `json:"amount_cents"`
	Provider     string `json:"provider"`
}

// Checkout creates a pending purchase and a Stripe payment intent. The actual
// ticket is minted later, when the payment_intent.succeeded webhook arrives.
func (s *Service) Checkout(ctx context.Context, buyerID string, in CheckoutInput) (*CheckoutResult, error) {
	if in.Quantity <= 0 {
		in.Quantity = 1
	}

	var eventID string
	var priceCents int64
	err := s.pool.QueryRow(ctx,
		`SELECT event_id, price_cents FROM ticket_tiers WHERE id=$1`, in.TierID,
	).Scan(&eventID, &priceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tier not found")
	}
	if err != nil {
		return nil, err
	}

	amount := priceCents * int64(in.Quantity)
	intentID, clientSecret, err := s.provider.CreateIntent(ctx, amount, map[string]string{
		"tier_id":  in.TierID,
		"event_id": eventID,
		"buyer_id": buyerID,
	})
	if err != nil {
		return nil, err
	}

	var purchaseID string
	err = s.pool.QueryRow(ctx, `
		INSERT INTO purchases (buyer_id, event_id, tier_id, stripe_payment_intent_id, amount_cents, quantity, status)
		VALUES ($1,$2,$3,$4,$5,$6,'pending')
		RETURNING id`,
		buyerID, eventID, in.TierID, intentID, amount, in.Quantity,
	).Scan(&purchaseID)
	if err != nil {
		return nil, err
	}

	return &CheckoutResult{
		PurchaseID:   purchaseID,
		IntentID:     intentID,
		ClientSecret: clientSecret,
		AmountCents:  amount,
		Provider:     s.provider.Name(),
	}, nil
}

// ProcessResult describes what a webhook delivery actually did.
type ProcessResult struct {
	Created       bool // this delivery minted the tickets
	AlreadyDone   bool // a previous delivery already handled it
	TicketsMinted int
}

// ProcessPaymentSucceeded is the idempotent core of the Stripe webhook. Calling
// it any number of times (including concurrently) for the same intent mints the
// tickets exactly once.
func (s *Service) ProcessPaymentSucceeded(ctx context.Context, intentID string) (ProcessResult, error) {
	// Layer 1: Redis fast-path idempotency key.
	acquired, err := s.idempo.acquire(ctx, intentID)
	if err == nil && !acquired {
		return ProcessResult{AlreadyDone: true}, nil
	}
	// If Redis errored we don't give up — the DB layer below is authoritative.

	res, purchase, procErr := s.processInTx(ctx, intentID)
	if procErr != nil {
		// Let a future retry reprocess this intent.
		s.idempo.release(ctx, intentID)
		return ProcessResult{}, procErr
	}
	_ = s.idempo.markDone(ctx, intentID)

	// Best-effort analytics publish AFTER commit — never on the critical path's
	// correctness. A failure here does not fail the webhook.
	if res.Created && s.publisher != nil {
		if err := s.publisher.PublishPurchase(ctx, purchase); err != nil {
			// Swallow: analytics is allowed to lag/miss without hurting checkout.
			_ = err
		}
	}
	return res, nil
}

func (s *Service) processInTx(ctx context.Context, intentID string) (ProcessResult, events.Purchase, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}
	defer tx.Rollback(ctx)

	// Layer 2: lock the purchase row. Concurrent processors serialize here.
	var purchaseID, tierID, eventID, buyerID, status string
	var quantity int
	var amountCents int64
	err = tx.QueryRow(ctx, `
		SELECT id, tier_id, event_id, buyer_id, status, quantity, amount_cents
		FROM purchases WHERE stripe_payment_intent_id=$1 FOR UPDATE`, intentID,
	).Scan(&purchaseID, &tierID, &eventID, &buyerID, &status, &quantity, &amountCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessResult{}, events.Purchase{}, ErrPurchaseNotFound
	}
	if err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}

	// Authoritative idempotency: already completed => do nothing.
	if status == "completed" {
		return ProcessResult{AlreadyDone: true}, events.Purchase{}, nil
	}

	// Lock the tier row and enforce capacity (Phase 3 leans on this too).
	var capacity, sold int
	err = tx.QueryRow(ctx,
		`SELECT capacity, sold FROM ticket_tiers WHERE id=$1 FOR UPDATE`, tierID,
	).Scan(&capacity, &sold)
	if err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}
	if sold+quantity > capacity {
		if _, err = tx.Exec(ctx, `UPDATE purchases SET status='failed' WHERE id=$1`, purchaseID); err != nil {
			return ProcessResult{}, events.Purchase{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return ProcessResult{}, events.Purchase{}, err
		}
		return ProcessResult{}, events.Purchase{}, ErrSoldOut
	}

	for i := 0; i < quantity; i++ {
		if _, err = tx.Exec(ctx, `
			INSERT INTO tickets (tier_id, event_id, buyer_id, purchase_id, status)
			VALUES ($1,$2,$3,$4,'valid')`,
			tierID, eventID, buyerID, purchaseID); err != nil {
			return ProcessResult{}, events.Purchase{}, err
		}
	}

	if _, err = tx.Exec(ctx,
		`UPDATE ticket_tiers SET sold = sold + $1 WHERE id=$2`, quantity, tierID); err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}
	if _, err = tx.Exec(ctx,
		`UPDATE purchases SET status='completed' WHERE id=$1`, purchaseID); err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return ProcessResult{}, events.Purchase{}, err
	}

	// Inventory changed: drop the cached count so reads recompute (Phase 3).
	s.inv.Invalidate(ctx, tierID)

	purchase := events.Purchase{
		PurchaseID:  purchaseID,
		EventID:     eventID,
		TierID:      tierID,
		BuyerID:     buyerID,
		AmountCents: amountCents,
		Quantity:    quantity,
		OccurredAt:  time.Now(),
	}
	return ProcessResult{Created: true, TicketsMinted: quantity}, purchase, nil
}
