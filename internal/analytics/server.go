// Package analytics contains the gRPC analytics service (server) and the client
// the core API uses to push purchase events over a stream.
package analytics

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	analyticspb "github.com/v-shah07/event-ticketing/proto/analyticspb"
)

// eventAgg is the running aggregation for a single event.
type eventAgg struct {
	revenueCents int64
	ticketsSold  int64
	firstAt      time.Time
	lastAt       time.Time
}

// Server implements analyticspb.AnalyticsServiceServer. It aggregates purchase
// events received over the RecordStream RPC in memory, and (best effort)
// mirrors running totals into Redis so the Phase 6 dashboard can read them.
type Server struct {
	analyticspb.UnimplementedAnalyticsServiceServer

	mu          sync.RWMutex
	byEvent     map[string]*eventAgg
	totalEvents uint64

	rdb *redis.Client // optional
}

func NewServer(rdb *redis.Client) *Server {
	return &Server{byEvent: make(map[string]*eventAgg), rdb: rdb}
}

// RecordStream is a client-streaming RPC: the core API opens a stream and pushes
// purchase events; the analytics service folds each into its aggregation and
// replies once with the running total when the stream closes.
func (s *Server) RecordStream(stream analyticspb.AnalyticsService_RecordStreamServer) error {
	var received uint64
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&analyticspb.RecordAck{
				Received:    true,
				TotalEvents: atomic.LoadUint64(&s.totalEvents),
			})
		}
		if err != nil {
			return err
		}
		s.apply(ev)
		received++
	}
}

func (s *Server) apply(ev *analyticspb.PurchaseEvent) {
	occurred := ev.OccurredAt.AsTime()
	if occurred.IsZero() {
		occurred = time.Now()
	}

	s.mu.Lock()
	agg := s.byEvent[ev.EventId]
	if agg == nil {
		agg = &eventAgg{firstAt: occurred, lastAt: occurred}
		s.byEvent[ev.EventId] = agg
	}
	agg.revenueCents += ev.AmountCents
	agg.ticketsSold += int64(ev.Quantity)
	if occurred.Before(agg.firstAt) {
		agg.firstAt = occurred
	}
	if occurred.After(agg.lastAt) {
		agg.lastAt = occurred
	}
	rev, sold := agg.revenueCents, agg.ticketsSold
	s.mu.Unlock()

	atomic.AddUint64(&s.totalEvents, 1)

	if s.rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pipe := s.rdb.Pipeline()
		k := "analytics:event:" + ev.EventId
		pipe.HSet(ctx, k, "revenue_cents", rev, "tickets_sold", sold)
		_, _ = pipe.Exec(ctx)
	}
}

// GetStats is a unary RPC returning the current aggregation for one event.
func (s *Server) GetStats(_ context.Context, req *analyticspb.StatsRequest) (*analyticspb.EventStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agg := s.byEvent[req.EventId]
	if agg == nil {
		return &analyticspb.EventStats{EventId: req.EventId}, nil
	}
	return &analyticspb.EventStats{
		EventId:             req.EventId,
		TotalRevenueCents:   agg.revenueCents,
		TicketsSold:         agg.ticketsSold,
		SalesVelocityPerMin: velocity(agg.ticketsSold, agg.firstAt, agg.lastAt),
	}, nil
}

// TotalEvents exposes the running count for tests.
func (s *Server) TotalEvents() uint64 { return atomic.LoadUint64(&s.totalEvents) }

func velocity(sold int64, first, last time.Time) float64 {
	mins := last.Sub(first).Minutes()
	if mins < 1 {
		mins = 1 // floor the window at a minute so bursts don't explode the rate
	}
	return float64(sold) / mins
}
