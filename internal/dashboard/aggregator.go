package dashboard

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/v-shah07/event-ticketing/internal/events"
)

// TierStat is per-tier live inventory + sales.
type TierStat struct {
	TierID    string `json:"tier_id"`
	Name      string `json:"name"`
	Sold      int    `json:"sold"`
	Remaining int    `json:"remaining"`
	Capacity  int    `json:"capacity"`
}

// Update is the live dashboard snapshot pushed to WebSocket clients.
type Update struct {
	EventID             string     `json:"event_id"`
	TotalRevenueCents   int64      `json:"total_revenue_cents"`
	TicketsSold         int64      `json:"tickets_sold"`
	SalesVelocityPerMin float64    `json:"sales_velocity_per_min"`
	Tiers               []TierStat `json:"tiers"`
	At                  time.Time  `json:"at"`
}

// Aggregator folds purchase events into Redis running totals and broadcasts a
// fresh snapshot to the dashboard hub. It is the Kafka consumer's handler.
type Aggregator struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	hub  *Hub
}

func NewAggregator(pool *pgxpool.Pool, rdb *redis.Client, hub *Hub) *Aggregator {
	return &Aggregator{pool: pool, rdb: rdb, hub: hub}
}

func revKey(eventID string) string   { return "dash:event:" + eventID + ":revenue" }
func soldKey(eventID string) string  { return "dash:event:" + eventID + ":sold" }
func firstKey(eventID string) string { return "dash:event:" + eventID + ":first_ts" }

// OnPurchase updates running totals in Redis and broadcasts a snapshot. It is
// safe to call from the Kafka consumer goroutine.
func (a *Aggregator) OnPurchase(p events.Purchase) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Running totals in Redis (the analytics aggregation store).
	revenue, _ := a.rdb.IncrBy(ctx, revKey(p.EventID), p.AmountCents).Result()
	sold, _ := a.rdb.IncrBy(ctx, soldKey(p.EventID), int64(p.Quantity)).Result()
	// First-seen timestamp for velocity (set once).
	a.rdb.SetNX(ctx, firstKey(p.EventID), p.OccurredAt.UnixMilli(), 0)
	firstMs, _ := a.rdb.Get(ctx, firstKey(p.EventID)).Int64()

	update := Update{
		EventID:             p.EventID,
		TotalRevenueCents:   revenue,
		TicketsSold:         sold,
		SalesVelocityPerMin: velocity(sold, firstMs),
		Tiers:               a.tierStats(ctx, p.EventID),
		At:                  time.Now(),
	}
	if payload, err := json.Marshal(update); err == nil {
		a.hub.Broadcast(p.EventID, payload)
	}
}

// tierStats reads committed per-tier inventory (source of truth) from Postgres.
func (a *Aggregator) tierStats(ctx context.Context, eventID string) []TierStat {
	rows, err := a.pool.Query(ctx,
		`SELECT id, name, sold, capacity FROM ticket_tiers WHERE event_id=$1 ORDER BY price_cents`, eventID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TierStat
	for rows.Next() {
		var ts TierStat
		if err := rows.Scan(&ts.TierID, &ts.Name, &ts.Sold, &ts.Capacity); err != nil {
			return out
		}
		ts.Remaining = ts.Capacity - ts.Sold
		out = append(out, ts)
	}
	return out
}

func velocity(sold, firstMs int64) float64 {
	if firstMs == 0 {
		return float64(sold)
	}
	mins := time.Since(time.UnixMilli(firstMs)).Minutes()
	if mins < 1 {
		mins = 1
	}
	return float64(sold) / mins
}
