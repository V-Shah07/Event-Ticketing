package dashboard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/dashboard"
	"github.com/v-shah07/event-ticketing/internal/payment"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/streaming"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

// TestLiveDashboardLatency is the Phase 6 PROVE IT.
//
// It drives 50 purchases through the full Kafka pipeline (payment -> Kafka ->
// consumer/aggregator -> WebSocket) and asserts a WebSocket client receives live
// totals matching every purchase, logging the observed end-to-end latency.
func TestLiveDashboardLatency(t *testing.T) {
	const purchases = 50
	const price = 3000

	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	brokers := testsupport.KafkaBrokers(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ensure the topic exists so nothing races auto-creation on a fresh broker.
	if err := streaming.EnsureTopic(brokers, 1); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}

	// Pipeline: Kafka consumer -> aggregator -> hub -> WebSocket.
	hub := dashboard.NewHub()
	aggregator := dashboard.NewAggregator(pool, rdb, hub)
	group := fmt.Sprintf("test-dash-%d", time.Now().UnixNano())
	consumer := streaming.NewConsumerFromStart(group, brokers...)
	defer consumer.Close()
	go consumer.Run(ctx, aggregator.OnPurchase)

	producer := streaming.NewProducer(brokers...)
	defer producer.Close()

	svc := payment.NewService(pool, rdb, payment.NewMockProvider())
	svc.SetPublisher(producer)

	h := server.New(server.Deps{
		Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret"),
		Payments: svc, DashboardHub: hub,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	orgTok := reg(t, srv.URL, testsupport.UniqueEmail("org-dash"), "organizer")
	eventID := mkEvent(t, srv.URL, orgTok)
	tierID := mkTier(t, srv.URL, orgTok, eventID, purchases)

	// Connect the WebSocket dashboard client.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/events/" + eventID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Wait until the hub has registered this subscriber.
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(eventID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("ws client never registered with hub")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Background reader: decode updates with their receive time.
	type stamped struct {
		u  dashboard.Update
		at time.Time
	}
	updates := make(chan stamped, 256)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var u dashboard.Update
			if json.Unmarshal(data, &u) == nil {
				updates <- stamped{u: u, at: time.Now()}
			}
		}
	}()

	// Drive purchases sequentially; measure per-purchase end-to-end latency.
	var maxLatency, sumLatency time.Duration
	var lastUpdate dashboard.Update
	for i := 1; i <= purchases; i++ {
		buyerTok := reg(t, srv.URL, testsupport.UniqueEmail("buyer-dash"), "buyer")
		intentID := doCheckout(t, srv.URL, buyerTok, tierID)
		fireWebhook(t, srv.URL, intentID)
		t0 := time.Now()

		// Wait for a WS update reflecting at least i tickets sold.
		waitDeadline := time.After(10 * time.Second)
		for {
			select {
			case s := <-updates:
				lastUpdate = s.u
				if s.u.TicketsSold >= int64(i) {
					lat := s.at.Sub(t0)
					if lat > maxLatency {
						maxLatency = lat
					}
					sumLatency += lat
					goto next
				}
			case <-waitDeadline:
				t.Fatalf("timed out waiting for WS update reflecting purchase %d (last sold=%d)", i, lastUpdate.TicketsSold)
			}
		}
	next:
	}

	// Final assertions on the last snapshot the client holds.
	if lastUpdate.TicketsSold != purchases {
		t.Fatalf("FAIL: final WS tickets_sold=%d, want %d", lastUpdate.TicketsSold, purchases)
	}
	wantRevenue := int64(price * purchases)
	if lastUpdate.TotalRevenueCents != wantRevenue {
		t.Fatalf("FAIL: final WS revenue=%d, want %d", lastUpdate.TotalRevenueCents, wantRevenue)
	}

	avgMs := float64(sumLatency.Microseconds()) / float64(purchases) / 1000.0
	t.Logf("PASS Phase6: %d purchases -> WebSocket received matching live totals (tickets_sold=%d revenue_cents=%d). End-to-end latency payment->WS: avg=%.1fms max=%.1fms",
		purchases, lastUpdate.TicketsSold, lastUpdate.TotalRevenueCents, avgMs, float64(maxLatency.Microseconds())/1000.0)
}

// --- helpers ---

func reg(t *testing.T, baseURL, email, role string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": "hunter2", "role": role})
	resp, err := http.Post(baseURL+"/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

func mkEvent(t *testing.T, baseURL, tok string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"title": "Dash Test", "category": "music"})
	req, _ := http.NewRequest("POST", baseURL+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

func mkTier(t *testing.T, baseURL, tok, eventID string, capacity int) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "GA", "price_cents": 3000, "capacity": capacity})
	req, _ := http.NewRequest("POST", baseURL+"/events/"+eventID+"/tiers", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

func doCheckout(t *testing.T, baseURL, tok, tierID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"tier_id": tierID, "quantity": 1})
	req, _ := http.NewRequest("POST", baseURL+"/checkout", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		IntentID string `json:"payment_intent_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.IntentID
}

func fireWebhook(t *testing.T, baseURL, intentID string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"type": "payment_intent.succeeded",
		"data": map[string]any{"object": map[string]any{"id": intentID}},
	})
	resp, err := http.Post(baseURL+"/webhooks/stripe", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status %d", resp.StatusCode)
	}
}
