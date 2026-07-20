package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

type gqlEvent struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Category       string   `json:"category"`
	MinPriceCents  *int     `json:"minPriceCents"`
	DistanceMeters *float64 `json:"distanceMeters"`
	TrendingScore  float64  `json:"trendingScore"`
}

// TestGraphQLDiscoveryAndPostGIS is the Phase 7 PROVE IT.
//
//  1. A single GraphQL query filtering on ALL FOUR dimensions (category + date +
//     price + location) returns exactly the matching event.
//  2. A geo radius query returns events within the radius ordered by distance.
//  3. p99 latency of the geo query over Z events is logged.
func TestGraphQLDiscoveryAndPostGIS(t *testing.T) {
	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	ctx := context.Background()

	h := server.New(server.Deps{Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret")})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Unique tag isolates this run's events from any others in the shared DB.
	tag := fmt.Sprintf("p7-%d", time.Now().UnixNano())
	cat := "concert-" + tag

	// Organizer for FK.
	var orgID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1,'x','organizer') RETURNING id`,
		testsupport.UniqueEmail("org-p7"),
	).Scan(&orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}

	// Center = Georgia Tech-ish.
	const lat0, lng0 = 33.7756, -84.3963
	startsAt := time.Now().Add(48 * time.Hour)

	// The one event that satisfies every dimension.
	target := insertEvent(t, pool, orgID, "TARGET "+tag, cat, lat0+0.001, lng0+0.001, startsAt, 5000)

	// Decoys, each violating exactly one dimension.
	insertEvent(t, pool, orgID, "wrong-category "+tag, "sports-"+tag, lat0, lng0, startsAt, 5000)
	insertEvent(t, pool, orgID, "out-of-date "+tag, cat, lat0, lng0, time.Now().Add(400*24*time.Hour), 5000)
	insertEvent(t, pool, orgID, "too-expensive "+tag, cat, lat0, lng0, startsAt, 50000)
	insertEvent(t, pool, orgID, "too-far "+tag, cat, lat0+2.0, lng0+2.0, startsAt, 5000) // ~200km+ away

	// --- Part 1: single 4-dimensional filter query ---
	q := `query ($f: EventFilter) { events(filter: $f) { id title category minPriceCents distanceMeters } }`
	vars := map[string]any{"f": map[string]any{
		"category":      cat,
		"startsAfter":   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		"startsBefore":  time.Now().Add(72 * time.Hour).Format(time.RFC3339),
		"minPriceCents": 1000,
		"maxPriceCents": 10000,
		"near":          map[string]any{"lat": lat0, "lng": lng0, "radiusMeters": 5000},
	}}
	events := runQuery(t, srv.URL, q, vars)
	if len(events) != 1 {
		t.Fatalf("FAIL: 4-dim filter returned %d events, want 1: %+v", len(events), events)
	}
	if events[0].ID != target {
		t.Fatalf("FAIL: 4-dim filter returned wrong event %s, want target %s", events[0].ID, target)
	}
	t.Logf("Part1 OK: 4-dim filter (category+date+price+location) returned exactly the target event")

	// --- Part 2: geo radius ordered by distance, over Z events ---
	const z = 300
	geoCat := "geo-" + tag
	for i := 0; i < z; i++ {
		// Spread events radially outward within ~11km.
		d := float64(i+1) * 0.0003 // ~33m per step in latitude
		insertEvent(t, pool, orgID, fmt.Sprintf("geo-%d %s", i, tag), geoCat,
			lat0+d, lng0, startsAt, 2000)
	}

	geoQ := `query ($f: EventFilter) { events(filter: $f) { id distanceMeters } }`
	geoVars := map[string]any{"f": map[string]any{
		"category": geoCat,
		"near":     map[string]any{"lat": lat0, "lng": lng0, "radiusMeters": 20000},
	}}
	geoEvents := runQuery(t, srv.URL, geoQ, geoVars)
	if len(geoEvents) != z {
		t.Fatalf("FAIL: geo query returned %d events within radius, want %d", len(geoEvents), z)
	}
	// Assert strictly non-decreasing distance ordering.
	prev := -1.0
	for i, e := range geoEvents {
		if e.DistanceMeters == nil {
			t.Fatalf("FAIL: event %d missing distance", i)
		}
		if *e.DistanceMeters+1e-6 < prev {
			t.Fatalf("FAIL: distance ordering violated at %d: %.2f < %.2f", i, *e.DistanceMeters, prev)
		}
		prev = *e.DistanceMeters
	}
	t.Logf("Part2 OK: geo radius query returned %d events ordered by distance (nearest=%.1fm farthest=%.1fm)",
		len(geoEvents), *geoEvents[0].DistanceMeters, *geoEvents[len(geoEvents)-1].DistanceMeters)

	// --- Part 3: p99 latency of the geo query over Z events ---
	const iters = 200
	lat := make([]float64, 0, iters)
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		_ = runQuery(t, srv.URL, geoQ, geoVars)
		lat = append(lat, float64(time.Since(t0).Microseconds())/1000.0)
	}
	sort.Float64s(lat)
	p50 := lat[int(math.Floor(0.50*float64(iters)))]
	p99 := lat[int(math.Floor(0.99*float64(iters)))]
	t.Logf("PASS Phase7: GraphQL 4-dim filter correct; PostGIS geo query over Z=%d events ordered by distance. Latency p50=%.1fms p99=%.1fms over %d runs",
		z, p50, p99, iters)
}

// insertEvent creates a published event with one tier at the given price and
// returns the event ID. The geom column is populated by the DB trigger.
func insertEvent(t *testing.T, pool *pgxpool.Pool, orgID, title, category string, lat, lng float64, startsAt time.Time, priceCents int64) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO events (organizer_id, title, category, venue, lat, lng, state, starts_at)
		VALUES ($1,$2,$3,'venue',$4,$5,'published',$6) RETURNING id`,
		orgID, title, category, lat, lng, startsAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_tiers (event_id, name, price_cents, capacity) VALUES ($1,'GA',$2,100)`,
		id, priceCents,
	); err != nil {
		t.Fatalf("insert tier: %v", err)
	}
	return id
}

// runQuery posts a GraphQL query and returns the events array from the response.
func runQuery(t *testing.T, baseURL, query string, vars map[string]any) []gqlEvent {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	resp, err := http.Post(baseURL+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("graphql post: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Events []gqlEvent `json:"events"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode graphql: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	return out.Data.Events
}
