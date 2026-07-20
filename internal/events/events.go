// Package events defines the purchase-event type and publisher interface shared
// by the payment package (producer) and the analytics client (consumer), so
// neither has to import the other.
package events

import (
	"context"
	"time"
)

// Purchase is a committed purchase, published to analytics after the DB commit.
type Purchase struct {
	PurchaseID  string
	EventID     string
	TierID      string
	BuyerID     string
	AmountCents int64
	Quantity    int
	OccurredAt  time.Time
}

// Publisher receives committed purchases. Implementations must be safe for
// concurrent use and should never block the purchase critical path for long.
type Publisher interface {
	PublishPurchase(ctx context.Context, p Purchase) error
}

// MultiPublisher fans a purchase out to several publishers (e.g. gRPC analytics
// and Kafka). Individual failures are collected but never abort the others.
type MultiPublisher []Publisher

func (m MultiPublisher) PublishPurchase(ctx context.Context, p Purchase) error {
	var firstErr error
	for _, pub := range m {
		if err := pub.PublishPurchase(ctx, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
