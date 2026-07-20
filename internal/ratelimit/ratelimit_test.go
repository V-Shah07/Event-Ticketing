package ratelimit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/v-shah07/event-ticketing/internal/ratelimit"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

// TestSlidingWindowRejectsOverLimit is the Phase 8 PROVE IT (limiter core).
//
// The first `limit` requests in a window are admitted; the (limit+1)th is
// rejected; after the window elapses, requests are admitted again.
func TestSlidingWindowRejectsOverLimit(t *testing.T) {
	rdb := testsupport.Redis(t)
	ctx := context.Background()

	const limit = 10
	window := 500 * time.Millisecond
	lim := ratelimit.New(rdb, limit, window)
	key := fmt.Sprintf("test-%d", time.Now().UnixNano())

	admitted := 0
	for i := 0; i < limit; i++ {
		ok, err := lim.Allow(ctx, key)
		if err != nil {
			t.Fatalf("allow: %v", err)
		}
		if ok {
			admitted++
		}
	}
	if admitted != limit {
		t.Fatalf("FAIL: expected first %d requests admitted, got %d", limit, admitted)
	}

	// The (limit+1)th request must be rejected.
	ok, err := lim.Allow(ctx, key)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if ok {
		t.Fatalf("FAIL: request %d should be rejected", limit+1)
	}

	// After the window passes, a request is admitted again.
	time.Sleep(window + 100*time.Millisecond)
	ok, err = lim.Allow(ctx, key)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !ok {
		t.Fatal("FAIL: request after window elapsed should be admitted")
	}

	t.Logf("PASS Phase8: sliding window admitted %d/%d, rejected the %dth, admitted again after window",
		admitted, limit, limit+1)
}

// TestMiddlewareReturns429 verifies the HTTP layer rejects the (limit+1)th.
func TestMiddlewareReturns429(t *testing.T) {
	rdb := testsupport.Redis(t)

	const limit = 5
	lim := ratelimit.New(rdb, limit, time.Minute)
	h := lim.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	var ok, limited int
	for i := 0; i < limit+3; i++ {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok != limit {
		t.Fatalf("FAIL: expected %d OK, got %d", limit, ok)
	}
	if limited != 3 {
		t.Fatalf("FAIL: expected 3 rate-limited, got %d", limited)
	}
	t.Logf("PASS Phase8: HTTP middleware allowed %d then returned 429 for the next %d", ok, limited)
}
