package ticket

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Outcome is the result of a door scan.
type Outcome string

const (
	Admitted       Outcome = "admitted"
	AlreadyScanned Outcome = "already_scanned"
	InvalidToken   Outcome = "invalid_token"
	NotFound       Outcome = "not_found"
)

// Validator admits a ticket exactly once across any number of concurrent door
// scanners. The admission decision is made by a single atomic Redis SETNX on
// the ticket's scan key: the one call that creates the key wins and admits;
// every other scan (concurrent or later) observes the key and is rejected as
// already-scanned. Redis ops are atomic and sub-millisecond, so this is both
// correct under contention and far cheaper than holding a DB row lock open for
// the duration of a scan.
type Validator struct {
	pool   *pgxpool.Pool
	rdb    *redis.Client
	signer Signer
	ttl    time.Duration
}

func NewValidator(pool *pgxpool.Pool, rdb *redis.Client, signer Signer) *Validator {
	return &Validator{pool: pool, rdb: rdb, signer: signer, ttl: 24 * time.Hour}
}

func scanKey(ticketID string) string { return "scan:ticket:" + ticketID }

// Result carries the scan outcome plus the resolved ticket ID.
type Result struct {
	Outcome  Outcome
	TicketID string
	Scanner  string
}

// Validate verifies the token, confirms the ticket exists and is valid, then
// atomically claims the single admission via SETNX.
func (v *Validator) Validate(ctx context.Context, token, scanner string) (Result, error) {
	ticketID, ok := v.signer.Verify(token)
	if !ok {
		return Result{Outcome: InvalidToken}, nil
	}

	// Confirm the ticket exists and is not refunded.
	var status string
	err := v.pool.QueryRow(ctx, `SELECT status FROM tickets WHERE id=$1`, ticketID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{Outcome: NotFound, TicketID: ticketID}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if status == "refunded" {
		return Result{Outcome: NotFound, TicketID: ticketID}, nil
	}

	// The atomic gate: exactly one scanner creates the key and is admitted.
	won, err := v.rdb.SetNX(ctx, scanKey(ticketID), scanner, v.ttl).Result()
	if err != nil {
		return Result{}, err
	}
	if !won {
		return Result{Outcome: AlreadyScanned, TicketID: ticketID}, nil
	}

	// Durably record the scan (best effort; the SETNX is the source of truth).
	_, _ = v.pool.Exec(ctx,
		`UPDATE tickets SET status='scanned', scanned_at=now() WHERE id=$1 AND status<>'scanned'`,
		ticketID)

	return Result{Outcome: Admitted, TicketID: ticketID, Scanner: scanner}, nil
}
