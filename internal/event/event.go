// Package event holds event + ticket-tier persistence and REST handlers.
package event

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	ID          string     `json:"id"`
	OrganizerID string     `json:"organizer_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
	Venue       string     `json:"venue"`
	Lat         *float64   `json:"lat,omitempty"`
	Lng         *float64   `json:"lng,omitempty"`
	State       string     `json:"state"`
	StartsAt    *time.Time `json:"starts_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Tiers       []Tier     `json:"tiers,omitempty"`
}

type Tier struct {
	ID         string    `json:"id"`
	EventID    string    `json:"event_id"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	Capacity   int       `json:"capacity"`
	Sold       int       `json:"sold"`
	Remaining  int       `json:"remaining"`
	CreatedAt  time.Time `json:"created_at"`
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type CreateEventInput struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
	Venue       string     `json:"venue"`
	Lat         *float64   `json:"lat"`
	Lng         *float64   `json:"lng"`
	StartsAt    *time.Time `json:"starts_at"`
}

func (s *Store) Create(ctx context.Context, organizerID string, in CreateEventInput) (*Event, error) {
	e := &Event{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO events (organizer_id, title, description, category, venue, lat, lng, starts_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, organizer_id, title, description, category, venue, lat, lng, state, starts_at, created_at`,
		organizerID, in.Title, in.Description, nz(in.Category, "general"), in.Venue, in.Lat, in.Lng, in.StartsAt,
	).Scan(&e.ID, &e.OrganizerID, &e.Title, &e.Description, &e.Category, &e.Venue, &e.Lat, &e.Lng, &e.State, &e.StartsAt, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Event, error) {
	e := &Event{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, organizer_id, title, description, category, venue, lat, lng, state, starts_at, created_at
		FROM events WHERE id=$1`, id,
	).Scan(&e.ID, &e.OrganizerID, &e.Title, &e.Description, &e.Category, &e.Venue, &e.Lat, &e.Lng, &e.State, &e.StartsAt, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	tiers, err := s.ListTiers(ctx, id)
	if err != nil {
		return nil, err
	}
	e.Tiers = tiers
	return e, nil
}

func (s *Store) List(ctx context.Context, onlyPublished bool) ([]Event, error) {
	q := `SELECT id, organizer_id, title, description, category, venue, lat, lng, state, starts_at, created_at FROM events`
	if onlyPublished {
		q += ` WHERE state='published'`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrganizerID, &e.Title, &e.Description, &e.Category, &e.Venue, &e.Lat, &e.Lng, &e.State, &e.StartsAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Publish(ctx context.Context, eventID, organizerID string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE events SET state='published' WHERE id=$1 AND organizer_id=$2`, eventID, organizerID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

type CreateTierInput struct {
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Capacity   int    `json:"capacity"`
}

func (s *Store) CreateTier(ctx context.Context, eventID string, in CreateTierInput) (*Tier, error) {
	t := &Tier{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ticket_tiers (event_id, name, price_cents, capacity)
		VALUES ($1,$2,$3,$4)
		RETURNING id, event_id, name, price_cents, capacity, sold, created_at`,
		eventID, in.Name, in.PriceCents, in.Capacity,
	).Scan(&t.ID, &t.EventID, &t.Name, &t.PriceCents, &t.Capacity, &t.Sold, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Remaining = t.Capacity - t.Sold
	return t, nil
}

func (s *Store) ListTiers(ctx context.Context, eventID string) ([]Tier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, event_id, name, price_cents, capacity, sold, created_at
		FROM ticket_tiers WHERE event_id=$1 ORDER BY price_cents ASC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tier
	for rows.Next() {
		var t Tier
		if err := rows.Scan(&t.ID, &t.EventID, &t.Name, &t.PriceCents, &t.Capacity, &t.Sold, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Remaining = t.Capacity - t.Sold
		out = append(out, t)
	}
	return out, rows.Err()
}

// OwnsEvent reports whether organizerID created the event.
func (s *Store) OwnsEvent(ctx context.Context, eventID, organizerID string) (bool, error) {
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT organizer_id FROM events WHERE id=$1`, eventID).Scan(&owner)
	if err != nil {
		return false, err
	}
	return owner == organizerID, nil
}

func nz(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
