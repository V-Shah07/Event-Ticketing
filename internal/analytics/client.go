package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/v-shah07/event-ticketing/internal/events"
	analyticspb "github.com/v-shah07/event-ticketing/proto/analyticspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client is the core API's handle to the analytics service. It implements
// events.Publisher by pushing each committed purchase over the RecordStream RPC.
type Client struct {
	conn   *grpc.ClientConn
	client analyticspb.AnalyticsServiceClient
}

var _ events.Publisher = (*Client)(nil)

// Dial connects to the analytics gRPC service.
func Dial(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial analytics: %w", err)
	}
	return &Client{conn: conn, client: analyticspb.NewAnalyticsServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// PublishPurchase streams a single purchase event to the analytics service.
// Using a (short) client stream keeps the call shape identical to a batched
// push and lets us honestly describe the transport as streaming gRPC.
func (c *Client) PublishPurchase(ctx context.Context, p events.Purchase) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stream, err := c.client.RecordStream(ctx)
	if err != nil {
		return fmt.Errorf("open record stream: %w", err)
	}
	occurred := p.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	if err := stream.Send(&analyticspb.PurchaseEvent{
		PurchaseId:  p.PurchaseID,
		EventId:     p.EventID,
		TierId:      p.TierID,
		BuyerId:     p.BuyerID,
		AmountCents: p.AmountCents,
		Quantity:    int32(p.Quantity),
		OccurredAt:  timestamppb.New(occurred),
	}); err != nil {
		return fmt.Errorf("send purchase event: %w", err)
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("close record stream: %w", err)
	}
	return nil
}

// Stats fetches the current aggregation for an event (unary RPC).
func (c *Client) Stats(ctx context.Context, eventID string) (*analyticspb.EventStats, error) {
	return c.client.GetStats(ctx, &analyticspb.StatsRequest{EventId: eventID})
}
