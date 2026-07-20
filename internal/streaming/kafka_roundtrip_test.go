package streaming_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/v-shah07/event-ticketing/internal/events"
	"github.com/v-shah07/event-ticketing/internal/streaming"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

func TestKafkaRoundTrip(t *testing.T) {
	brokers := testsupport.KafkaBrokers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := streaming.EnsureTopic(brokers, 1); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}

	group := fmt.Sprintf("test-rt-%d", time.Now().UnixNano())
	consumer := streaming.NewConsumerFromStart(group, brokers...)
	defer consumer.Close()

	got := make(chan events.Purchase, 4)
	go consumer.Run(ctx, func(p events.Purchase) { got <- p })

	producer := streaming.NewProducer(brokers...)
	defer producer.Close()

	want := events.Purchase{PurchaseID: "rt-" + fmt.Sprint(time.Now().UnixNano()), EventID: "rt-event", Quantity: 1, AmountCents: 100, OccurredAt: time.Now()}
	if err := producer.PublishPurchase(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.After(20 * time.Second)
	for {
		select {
		case p := <-got:
			if p.PurchaseID == want.PurchaseID {
				t.Logf("round-trip OK: %s", p.PurchaseID)
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for round-trip message")
		}
	}
}
