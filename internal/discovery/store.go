package discovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// queryEvents resolves the multi-dimensional discovery query. All supplied
// filter dimensions (category, date range, price range, geo radius) are ANDed
// into a single SQL statement. When a geo filter is present, results are ordered
// by distance; otherwise by trending score then recency.
func queryEvents(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, f *EventFilter) ([]*Event, error) {
	var (
		conds []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	conds = append(conds, "e.state = 'published'")

	// Distance expression (only meaningful when a geo filter is present).
	distanceSelect := "NULL::double precision AS distance_meters"
	orderBy := "trending DESC, e.created_at DESC"

	if f != nil && f.Near != nil {
		pt := fmt.Sprintf("ST_SetSRID(ST_MakePoint(%s, %s), 4326)::geography",
			arg(f.Near.Lng), arg(f.Near.Lat))
		distanceSelect = "ST_Distance(e.geom, " + pt + ") AS distance_meters"
		conds = append(conds, "e.geom IS NOT NULL")
		conds = append(conds, fmt.Sprintf("ST_DWithin(e.geom, %s, %s)", pt, arg(f.Near.RadiusMeters)))
		orderBy = "distance_meters ASC"
	}

	if f != nil {
		if f.Category != nil {
			conds = append(conds, "e.category = "+arg(*f.Category))
		}
		if f.StartsAfter != nil {
			conds = append(conds, "e.starts_at >= "+arg(*f.StartsAfter))
		}
		if f.StartsBefore != nil {
			conds = append(conds, "e.starts_at <= "+arg(*f.StartsBefore))
		}
		if f.MinPriceCents != nil {
			conds = append(conds, "mp.min_price_cents >= "+arg(int64(*f.MinPriceCents)))
		}
		if f.MaxPriceCents != nil {
			conds = append(conds, "mp.min_price_cents <= "+arg(int64(*f.MaxPriceCents)))
		}
	}

	query := `
		SELECT e.id, e.title, e.description, e.category, e.venue, e.lat, e.lng,
		       e.state, e.starts_at, mp.min_price_cents, ` + distanceSelect + `,
		       COALESCE(t.trending, 0) AS trending
		FROM events e
		LEFT JOIN LATERAL (
		    SELECT MIN(price_cents) AS min_price_cents FROM ticket_tiers WHERE event_id = e.id
		) mp ON true
		LEFT JOIN LATERAL (
		    SELECT SUM(1.0 / (1.0 + EXTRACT(EPOCH FROM (now() - created_at)) / 86400.0)) AS trending
		    FROM purchases WHERE event_id = e.id AND status = 'completed'
		) t ON true
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ` + orderBy + `
		LIMIT 500`

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Event
	for rows.Next() {
		var e Event
		var minPrice *int
		var dist *float64
		var trending float64
		if err := rows.Scan(&e.ID, &e.Title, &e.Description, &e.Category, &e.Venue,
			&e.Lat, &e.Lng, &e.State, &e.StartsAt, &minPrice, &dist, &trending); err != nil {
			return nil, err
		}
		e.MinPriceCents = minPrice
		e.DistanceMeters = dist
		e.TrendingScore = trending
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Cache the (small) trending scores in Redis for fast repeat reads.
	cacheTrending(ctx, rdb, out)

	// Batch-load tiers for all matched events in a single query (no N+1).
	if err := attachTiers(ctx, pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachTiers loads every matched event's tiers in one round trip and assigns
// them, avoiding an N+1 query per event.
func attachTiers(ctx context.Context, pool *pgxpool.Pool, events []*Event) error {
	if len(events) == 0 {
		return nil
	}
	byID := make(map[string]*Event, len(events))
	ids := make([]string, 0, len(events))
	for _, e := range events {
		byID[e.ID] = e
		ids = append(ids, e.ID)
	}

	rows, err := pool.Query(ctx,
		`SELECT id, event_id, name, price_cents, capacity, sold
		 FROM ticket_tiers WHERE event_id = ANY($1) ORDER BY price_cents`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var t Tier
		var eventID string
		var price int64
		if err := rows.Scan(&t.ID, &eventID, &t.Name, &price, &t.Capacity, &t.Sold); err != nil {
			return err
		}
		t.PriceCents = int(price)
		t.Remaining = t.Capacity - t.Sold
		if e := byID[eventID]; e != nil {
			e.Tiers = append(e.Tiers, &t)
		}
	}
	return rows.Err()
}

func cacheTrending(ctx context.Context, rdb *redis.Client, events []*Event) {
	if rdb == nil {
		return
	}
	pipe := rdb.Pipeline()
	for _, e := range events {
		pipe.Set(ctx, "trending:event:"+e.ID, e.TrendingScore, 60*time.Second)
	}
	_, _ = pipe.Exec(ctx)
}
