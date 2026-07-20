package ticket_test

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
	"github.com/v-shah07/event-ticketing/internal/ticket"
)

// TestExactlyOnceEntry is the Phase 5 PROVE IT.
//
// A single ticket's QR is fired at K door scanners concurrently. Exactly one
// scan must be "admitted"; every other scan must be rejected as
// "already_scanned". The Redis SETNX lock is the atomic gate.
func TestExactlyOnceEntry(t *testing.T) {
	const scanners = 50

	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	ctx := context.Background()
	qrDir := t.TempDir()

	signer := ticket.NewSigner("test-ticket-secret")
	svc := payment.NewService(pool, rdb, payment.NewMockProvider())
	svc.SetTicketIssuer(ticket.NewIssuer(pool, signer, qrDir))

	h := server.New(server.Deps{
		Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret"),
		Payments: svc, TicketSigner: signer, QRDir: qrDir,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Mint one ticket via the real purchase path.
	orgTok := reg(t, srv.URL, testsupport.UniqueEmail("org-qr"), "organizer")
	eventID := mkEvent(t, srv.URL, orgTok)
	tierID := mkTier(t, srv.URL, orgTok, eventID, 10)
	buyerTok := reg(t, srv.URL, testsupport.UniqueEmail("buyer-qr"), "buyer")
	intentID := doCheckout(t, srv.URL, buyerTok, tierID)
	fireWebhook(t, srv.URL, intentID)

	// Read the issued QR token straight from the ticket row (a scanner would
	// decode this from the PNG).
	var token string
	if err := pool.QueryRow(ctx,
		`SELECT qr_token FROM tickets WHERE purchase_id=(SELECT id FROM purchases WHERE stripe_payment_intent_id=$1)`,
		intentID,
	).Scan(&token); err != nil {
		t.Fatalf("read qr token: %v", err)
	}
	if token == "" {
		t.Fatal("ticket has no QR token issued")
	}

	// Fire the same token at K scanners simultaneously.
	var admitted, already, other int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < scanners; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"token": token, "scanner": scannerName(n)})
			<-start
			resp, err := http.Post(srv.URL+"/tickets/validate", "application/json", bytes.NewReader(body))
			if err != nil {
				atomic.AddInt64(&other, 1)
				return
			}
			defer resp.Body.Close()
			var out struct {
				Outcome  string `json:"outcome"`
				Admitted bool   `json:"admitted"`
			}
			json.NewDecoder(resp.Body).Decode(&out)
			switch {
			case out.Admitted && out.Outcome == "admitted":
				atomic.AddInt64(&admitted, 1)
			case out.Outcome == "already_scanned":
				atomic.AddInt64(&already, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	var dbStatus string
	pool.QueryRow(ctx,
		`SELECT status FROM tickets WHERE qr_token=$1`, token).Scan(&dbStatus)

	if admitted != 1 {
		t.Fatalf("FAIL: expected exactly 1 admitted, got %d (already=%d other=%d)", admitted, already, other)
	}
	if already != scanners-1 {
		t.Fatalf("FAIL: expected %d already_scanned, got %d (other=%d)", scanners-1, already, other)
	}
	if dbStatus != "scanned" {
		t.Fatalf("FAIL: expected ticket status scanned, got %q", dbStatus)
	}

	t.Logf("PASS Phase5: 1 QR at %d concurrent scanners -> admitted=%d already_scanned=%d other=%d, ticket marked %q",
		scanners, admitted, already, other, dbStatus)
}

func scannerName(n int) string {
	return "gate-" + string(rune('A'+n%26))
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
	body, _ := json.Marshal(map[string]string{"title": "QR Test", "category": "music"})
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
	body, _ := json.Marshal(map[string]any{"name": "GA", "price_cents": 1500, "capacity": capacity})
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
