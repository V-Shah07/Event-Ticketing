// Package streaming is the Kafka side of the analytics pipeline: a producer that
// publishes committed purchases and a consumer that folds them into running
// totals. Kafka decouples the purchase critical path from analytics — checkout
// completes as soon as the DB commits; the pipeline can lag without hurting UX.
package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/v-shah07/event-ticketing/internal/events"
)

// Topic is the single purchase-events topic (one topic, by design).
const Topic = "purchase-events"

// EnsureTopic creates the purchase-events topic if it does not already exist, so
// producers/consumers don't race auto-creation on a fresh broker. Idempotent.
func EnsureTopic(brokers []string, partitions int) error {
	if len(brokers) == 0 {
		return errors.New("no brokers")
	}
	conn, err := kafka.DialContext(context.Background(), "tcp", brokers[0])
	if err != nil {
		return err
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return err
	}
	ctrlConn, err := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer ctrlConn.Close()

	err = ctrlConn.CreateTopics(kafka.TopicConfig{
		Topic:             Topic,
		NumPartitions:     partitions,
		ReplicationFactor: 1,
	})
	// CreateTopics is fine if the topic already exists.
	if err != nil && !errors.Is(err, kafka.TopicAlreadyExists) {
		return err
	}
	return nil
}

// message is the JSON envelope written to Kafka.
type message struct {
	PurchaseID  string    `json:"purchase_id"`
	EventID     string    `json:"event_id"`
	TierID      string    `json:"tier_id"`
	BuyerID     string    `json:"buyer_id"`
	AmountCents int64     `json:"amount_cents"`
	Quantity    int       `json:"quantity"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Producer publishes purchase events to Kafka. It implements events.Publisher.
type Producer struct {
	writer *kafka.Writer
}

var _ events.Publisher = (*Producer)(nil)

func NewProducer(brokers ...string) *Producer {
	return &Producer{writer: &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Topic:                  Topic,
		Balancer:               &kafka.Hash{}, // partition by key (event id)
		AllowAutoTopicCreation: true,
		BatchTimeout:           10 * time.Millisecond,
	}}
}

func (p *Producer) Close() error { return p.writer.Close() }

func (p *Producer) PublishPurchase(ctx context.Context, pu events.Purchase) error {
	body, err := json.Marshal(message{
		PurchaseID:  pu.PurchaseID,
		EventID:     pu.EventID,
		TierID:      pu.TierID,
		BuyerID:     pu.BuyerID,
		AmountCents: pu.AmountCents,
		Quantity:    pu.Quantity,
		OccurredAt:  pu.OccurredAt,
	})
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(pu.EventID),
		Value: body,
	})
}

// Consumer reads purchase events and invokes a handler for each.
type Consumer struct {
	reader *kafka.Reader
}

// NewConsumer reads only messages produced after it joins (live dashboard).
func NewConsumer(groupID string, brokers ...string) *Consumer {
	return newConsumer(groupID, kafka.LastOffset, brokers...)
}

// NewConsumerFromStart reads the topic from the earliest offset. Used by tests
// (with a unique group ID) to guarantee no message is missed by a join race.
func NewConsumerFromStart(groupID string, brokers ...string) *Consumer {
	return newConsumer(groupID, kafka.FirstOffset, brokers...)
}

func newConsumer(groupID string, startOffset int64, brokers ...string) *Consumer {
	return &Consumer{reader: kafka.NewReader(kafka.ReaderConfig{
		Brokers:     brokers,
		Topic:       Topic,
		GroupID:     groupID,
		StartOffset: startOffset,
		MinBytes:    1,
		MaxBytes:    10e6,
		MaxWait:     50 * time.Millisecond,
	})}
}

func (c *Consumer) Close() error { return c.reader.Close() }

// Run blocks reading messages until ctx is cancelled, calling handle for each.
func (c *Consumer) Run(ctx context.Context, handle func(events.Purchase)) error {
	for {
		m, err := c.reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var msg message
		if err := json.Unmarshal(m.Value, &msg); err != nil {
			continue // skip malformed
		}
		handle(events.Purchase{
			PurchaseID:  msg.PurchaseID,
			EventID:     msg.EventID,
			TierID:      msg.TierID,
			BuyerID:     msg.BuyerID,
			AmountCents: msg.AmountCents,
			Quantity:    msg.Quantity,
			OccurredAt:  msg.OccurredAt,
		})
	}
}
