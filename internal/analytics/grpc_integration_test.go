package analytics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v-shah07/event-ticketing/internal/analytics"
	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/payment"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
	analyticspb "github.com/v-shah07/event-ticketing/proto/analyticspb"
	"google.golang.org/grpc"
)

// TestPurchaseFlowsToAnalyticsOverGRPC is the Phase 4 PROVE IT.
//
// It stands up the analytics gRPC service on a real socket, connects the core
// API to it via a gRPC client, drives real purchases through the core's REST +
// webhook path, and asserts the analytics service received them over gRPC
// (verified both via the server's counter and a unary GetStats round trip).
func TestPurchaseFlowsToAnalyticsOverGRPC(t *testing.T) {
	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)
	ctx := context.Background()

	// --- Start the analytics gRPC service on a real port ---
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	analyticsSrv := analytics.NewServer(rdb)
	grpcSrv := grpc.NewServer()
	analyticspb.RegisterAnalyticsServiceServer(grpcSrv, analyticsSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.Stop()

	// --- Connect the core API to it via a gRPC client ---
	client, err := analytics.Dial(lis.Addr().String())
	if err != nil {
		t.Fatalf("dial analytics: %v", err)
	}
	defer client.Close()

	svc := payment.NewService(pool, rdb, payment.NewMockProvider())
	svc.SetPublisher(client)
	h := server.New(server.Deps{
		Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret"), Payments: svc,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// --- Drive real purchases through the core ---
	orgTok := reg(t, srv.URL, testsupport.UniqueEmail("org-grpc"), "organizer")
	eventID := mkEvent(t, srv.URL, orgTok)
	const price = 2500
	tierID := mkTier(t, srv.URL, orgTok, eventID, price, 50)

	const purchases = 5
	for i := 0; i < purchases; i++ {
		buyerTok := reg(t, srv.URL, testsupport.UniqueEmail("buyer-grpc"), "buyer")
		intentID := doCheckout(t, srv.URL, buyerTok, tierID)
		fireWebhook(t, srv.URL, intentID)
	}

	// --- Assert analytics received them over gRPC ---
	if got := analyticsSrv.TotalEvents(); got < purchases {
		t.Fatalf("FAIL: analytics server saw %d events over gRPC, want >= %d", got, purchases)
	}

	statCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stats, err := client.Stats(statCtx, eventID)
	if err != nil {
		t.Fatalf("GetStats over gRPC: %v", err)
	}
	if stats.TicketsSold != purchases {
		t.Fatalf("FAIL: analytics tickets_sold=%d, want %d", stats.TicketsSold, purchases)
	}
	wantRevenue := int64(price * purchases)
	if stats.TotalRevenueCents != wantRevenue {
		t.Fatalf("FAIL: analytics revenue=%d, want %d", stats.TotalRevenueCents, wantRevenue)
	}

	t.Logf("PASS Phase4: %d purchases streamed core->analytics over gRPC; GetStats returned tickets_sold=%d revenue_cents=%d velocity=%.2f/min",
		purchases, stats.TicketsSold, stats.TotalRevenueCents, stats.SalesVelocityPerMin)
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
	body, _ := json.Marshal(map[string]string{"title": "gRPC Test", "category": "music"})
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

func mkTier(t *testing.T, baseURL, tok, eventID string, price, capacity int) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "GA", "price_cents": price, "capacity": capacity})
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
