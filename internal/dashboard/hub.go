// Package dashboard fans out live sales updates to organizer dashboards over
// WebSocket. An aggregator consumes purchase events (off Kafka), folds them into
// running totals, and broadcasts a snapshot to every client watching that event.
package dashboard

import (
	"sync"
)

// client is a single WebSocket subscriber for one event.
type client struct {
	eventID string
	send    chan []byte
}

// Hub tracks WebSocket clients grouped by event ID and fans out broadcasts.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*client]struct{} // eventID -> set of clients
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]map[*client]struct{})}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.clients[c.eventID]
	if set == nil {
		set = make(map[*client]struct{})
		h.clients[c.eventID] = set
	}
	set[c] = struct{}{}
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.clients[c.eventID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.eventID)
		}
	}
	close(c.send)
}

// Broadcast delivers payload to every client watching eventID. Slow clients
// whose buffer is full drop the message rather than block the aggregator.
func (h *Hub) Broadcast(eventID string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[eventID] {
		select {
		case c.send <- payload:
		default: // buffer full: drop rather than stall the pipeline
		}
	}
}

// Subscribers returns how many clients currently watch an event (for tests).
func (h *Hub) Subscribers(eventID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[eventID])
}
