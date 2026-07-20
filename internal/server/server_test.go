package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/v-shah07/event-ticketing/internal/auth"
	"github.com/v-shah07/event-ticketing/internal/server"
	"github.com/v-shah07/event-ticketing/internal/testsupport"
)

// TestEventCRUDWithJWT is the Phase 1 PROVE IT: register an organizer, create
// an event with a valid JWT, and list it back over REST.
func TestEventCRUDWithJWT(t *testing.T) {
	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)

	h := server.New(server.Deps{Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret")})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Register organizer.
	token, _ := registerUser(t, srv.URL, testsupport.UniqueEmail("organizer1"), "hunter2", "organizer")

	// Create an event.
	body := `{"title":"Spring Fest","description":"live music","category":"music","venue":"Klaus"}`
	req, _ := http.NewRequest("POST", srv.URL+"/events", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("created event has no id")
	}
	if created.State != "draft" {
		t.Fatalf("new event state = %q, want draft", created.State)
	}

	// Publish so it appears in the public list.
	pubReq, _ := http.NewRequest("POST", srv.URL+"/events/"+created.ID+"/publish", nil)
	pubReq.Header.Set("Authorization", "Bearer "+token)
	pubResp, err := http.DefaultClient.Do(pubReq)
	if err != nil || pubResp.StatusCode != http.StatusOK {
		t.Fatalf("publish failed: err=%v status=%v", err, pubResp.StatusCode)
	}
	pubResp.Body.Close()

	// List events (public).
	listResp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var events []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	json.NewDecoder(listResp.Body).Decode(&events)
	listResp.Body.Close()

	found := false
	for _, e := range events {
		if e.ID == created.ID && e.Title == "Spring Fest" {
			found = true
		}
	}
	if !found {
		t.Fatalf("created event not found in list; got %d events", len(events))
	}

	t.Logf("PASS Phase1: organizer registered, event %s created + published + listed via REST with JWT", created.ID)
}

// TestUnauthenticatedCannotCreate verifies JWT enforcement on writes.
func TestUnauthenticatedCannotCreate(t *testing.T) {
	pool := testsupport.Pool(t)
	rdb := testsupport.Redis(t)

	h := server.New(server.Deps{Pool: pool, Redis: rdb, JWT: auth.NewManager("test-secret")})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/events", "application/json", bytes.NewBufferString(`{"title":"x"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create status = %d, want 401", resp.StatusCode)
	}

	// A buyer token must be forbidden from creating events.
	buyerTok, _ := registerUser(t, srv.URL, testsupport.UniqueEmail("buyer1"), "hunter2", "buyer")
	req, _ := http.NewRequest("POST", srv.URL+"/events", bytes.NewBufferString(`{"title":"x"}`))
	req.Header.Set("Authorization", "Bearer "+buyerTok)
	bResp, _ := http.DefaultClient.Do(req)
	if bResp.StatusCode != http.StatusForbidden {
		t.Fatalf("buyer create status = %d, want 403", bResp.StatusCode)
	}
	bResp.Body.Close()
	t.Log("PASS Phase1: writes reject anonymous (401) and buyer (403)")
}

func registerUser(t *testing.T, baseURL, email, pw, role string) (token, userID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": pw, "role": role})
	resp, err := http.Post(baseURL+"/auth/register", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", resp.StatusCode)
	}
	var out struct {
		Token  string `json:"token"`
		UserID string `json:"user_id"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token, out.UserID
}
