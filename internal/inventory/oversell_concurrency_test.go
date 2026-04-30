package inventory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/payment"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

// TestNoOversell100Buyers1Ticket is the Phase 3 PROVE IT.
//
// 100 distinct buyers each check out and fire their own payment webhook
// concurrently against a tier with capacity 1. Exactly one must mint a ticket;
// the other 99 must fail cleanly as sold-out. Inventory must never go negative
// and must end at exactly 0 remaining.
func TestNoOversell100Buyers1Ticket(t *testing.T) {
	const buyers = 100

	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	ctx := context.Background()

	svc := payment.NewService(pool, rdb, payment.NewMockProvider())
	h := server.New(server.Deps{
		Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret"), Payments: svc,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	orgTok := reg(t, srv.URL, testsupport.UniqueEmail("org-oversell"), "organizer")
	eventID := mkEvent(t, srv.URL, orgTok)
	tierID := mkTier(t, srv.URL, orgTok, eventID, 1) // capacity == 1

	// Each buyer checks out first (checkout does not reserve inventory; the
	// webhook is where the SELECT FOR UPDATE race is decided).
	intents := make([]string, buyers)
	for i := 0; i < buyers; i++ {
		buyerTok := reg(t, srv.URL, testsupport.UniqueEmail("buyer-oversell"), "buyer")
		intents[i] = doCheckout(t, srv.URL, buyerTok, tierID)
	}

	// Sample inventory concurrently while the sales race runs, to catch any
	// transient negative value.
	var minObserved int64 = 1 << 30
	stopSampling := make(chan struct{})
	var sampWG sync.WaitGroup
	sampWG.Add(1)
	go func() {
		defer sampWG.Done()
		for {
			select {
			case <-stopSampling:
				return
			default:
				var sold, capacity int
				if err := pool.QueryRow(ctx,
					`SELECT sold, capacity FROM ticket_tiers WHERE id=$1`, tierID,
				).Scan(&sold, &capacity); err == nil {
					rem := int64(capacity - sold)
					for {
						cur := atomic.LoadInt64(&minObserved)
						if rem >= cur || atomic.CompareAndSwapInt64(&minObserved, cur, rem) {
							break
						}
					}
				}
			}
		}
	}()

	var success, soldOut, other int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(intentID string) {
			defer wg.Done()
			<-start
			payload, _ := json.Marshal(map[string]any{
				"type": "payment_intent.succeeded",
				"data": map[string]any{"object": map[string]any{"id": intentID}},
			})
			resp, err := http.Post(srv.URL+"/webhooks/stripe", "application/json", bytes.NewReader(payload))
			if err != nil {
				atomic.AddInt64(&other, 1)
				return
			}
			defer resp.Body.Close()
			var out struct {
				Status  string `json:"status"`
				Created bool   `json:"created"`
			}
			json.NewDecoder(resp.Body).Decode(&out)
			switch {
			case resp.StatusCode == http.StatusOK && out.Created:
				atomic.AddInt64(&success, 1)
			case resp.StatusCode == http.StatusOK && out.Status == "sold_out":
				atomic.AddInt64(&soldOut, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
		}(intents[i])
	}
	close(start)
	wg.Wait()
	close(stopSampling)
	sampWG.Wait()

	var sold, capacity, ticketCount int
	pool.QueryRow(ctx, `SELECT sold, capacity FROM ticket_tiers WHERE id=$1`, tierID).Scan(&sold, &capacity)
	pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tier_id=$1`, tierID).Scan(&ticketCount)
	remaining := capacity - sold

	if success != 1 {
		t.Fatalf("FAIL: expected exactly 1 successful purchase, got %d", success)
	}
	if soldOut != buyers-1 {
		t.Fatalf("FAIL: expected %d sold-out failures, got %d (other=%d)", buyers-1, soldOut, other)
	}
	if ticketCount != 1 {
		t.Fatalf("FAIL: expected exactly 1 ticket row, got %d", ticketCount)
	}
	if remaining != 0 {
		t.Fatalf("FAIL: expected 0 remaining, got %d", remaining)
	}
	if atomic.LoadInt64(&minObserved) < 0 {
		t.Fatalf("FAIL: observed negative inventory (%d)", minObserved)
	}

	t.Logf("PASS Phase3: %d concurrent buyers, capacity=1 -> success=%d sold_out=%d tickets=%d remaining=%d min_inventory_observed=%d (never negative)",
		buyers, success, soldOut, ticketCount, remaining, minObserved)
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
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

func mkEvent(t *testing.T, baseURL, tok string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"title": "Oversell Test", "category": "music"})
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
	body, _ := json.Marshal(map[string]any{"name": "GA", "price_cents": 1000, "capacity": capacity})
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
