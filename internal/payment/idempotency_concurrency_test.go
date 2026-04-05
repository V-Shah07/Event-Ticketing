package payment_test

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

// TestWebhookIdempotency100xConcurrent is the Phase 2 PROVE IT.
//
// It fires the *same* Stripe payment_intent.succeeded webhook 100 times
// concurrently and asserts exactly one ticket is created and the tier's sold
// count advances by exactly one — no duplicates, no lost updates.
func TestWebhookIdempotency100xConcurrent(t *testing.T) {
	const concurrency = 100

	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	ctx := context.Background()

	svc := payment.NewService(pool, rdb, payment.NewMockProvider())
	h := server.New(server.Deps{
		Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret"),
		Payments: svc, // no webhook key => signature verification skipped (test mode)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Seed an organizer, an event, and a tier with plenty of capacity so the
	// only thing under test is idempotency (not the sold-out path).
	orgTok := register(t, srv.URL, testsupport.UniqueEmail("org-idem"), "organizer")
	buyerTok := register(t, srv.URL, testsupport.UniqueEmail("buyer-idem"), "buyer")
	eventID := createEvent(t, srv.URL, orgTok, "Idempotency Fest")
	tierID := createTier(t, srv.URL, orgTok, eventID, "GA", 2500, 100)

	// Buyer checks out once -> one pending purchase + one payment intent.
	intentID := checkout(t, srv.URL, buyerTok, tierID)

	// Build the webhook payload once; every goroutine posts this identical body.
	payload := webhookPayload(intentID)

	var okCount, createdCount, errCount int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all at once for maximum contention
			resp, err := http.Post(srv.URL+"/webhooks/stripe", "application/json", bytes.NewReader(payload))
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				atomic.AddInt64(&okCount, 1)
				var out struct {
					Created bool `json:"created"`
				}
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Created {
					atomic.AddInt64(&createdCount, 1)
				}
			} else {
				atomic.AddInt64(&errCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	// Assertions: exactly one ticket, sold==1, purchase completed.
	var ticketCount, sold int
	var purchaseStatus string
	// Scope to this test's tier so the assertion is robust when other test
	// packages run against the same database concurrently.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tier_id=$1`, tierID).Scan(&ticketCount); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT sold FROM ticket_tiers WHERE id=$1`, tierID).Scan(&sold); err != nil {
		t.Fatalf("read sold: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT status FROM purchases WHERE stripe_payment_intent_id=$1`, intentID,
	).Scan(&purchaseStatus); err != nil {
		t.Fatalf("read purchase: %v", err)
	}

	if ticketCount != 1 {
		t.Fatalf("FAIL: expected exactly 1 ticket, got %d", ticketCount)
	}
	if sold != 1 {
		t.Fatalf("FAIL: expected tier sold=1, got %d", sold)
	}
	if createdCount != 1 {
		t.Fatalf("FAIL: expected exactly 1 webhook to report created=true, got %d", createdCount)
	}
	if purchaseStatus != "completed" {
		t.Fatalf("FAIL: expected purchase completed, got %q", purchaseStatus)
	}

	t.Logf("PASS Phase2: %d concurrent identical webhooks -> tickets=%d sold=%d created_true=%d ok_responses=%d errors=%d",
		concurrency, ticketCount, sold, createdCount, okCount, errCount)
}

// --- helpers ---

func webhookPayload(intentID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "payment_intent.succeeded",
		"data": map[string]any{
			"object": map[string]any{"id": intentID},
		},
	})
	return b
}

func register(t *testing.T, baseURL, email, role string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": "hunter2", "role": role})
	resp, err := http.Post(baseURL+"/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s status %d", email, resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

func createEvent(t *testing.T, baseURL, tok, title string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"title": title, "category": "music"})
	req, _ := http.NewRequest("POST", baseURL+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

func createTier(t *testing.T, baseURL, tok, eventID, name string, price int64, cap int) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "price_cents": price, "capacity": cap})
	req, _ := http.NewRequest("POST", baseURL+"/events/"+eventID+"/tiers", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create tier: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.ID
}

func checkout(t *testing.T, baseURL, tok, tierID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"tier_id": tierID, "quantity": 1})
	req, _ := http.NewRequest("POST", baseURL+"/checkout", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkout status %d", resp.StatusCode)
	}
	var out struct {
		IntentID string `json:"payment_intent_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.IntentID == "" {
		t.Fatal("checkout returned empty intent id")
	}
	return out.IntentID
}
