package dashboard

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// Dashboards are same-origin in prod; allow all in this build for the demo.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler upgrades dashboard connections and registers them with the hub.
type Handler struct {
	Hub *Hub
}

func NewHandler(hub *Hub) *Handler { return &Handler{Hub: hub} }

// Subscribe handles GET /ws/events/{eventID}: it upgrades to WebSocket and
// streams live Update snapshots for that event until the client disconnects.
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventID")
	if eventID == "" {
		http.Error(w, "event id required", http.StatusBadRequest)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}

	c := &client{eventID: eventID, send: make(chan []byte, 64)}
	h.Hub.register(c)
	defer h.Hub.unregister(c)

	// Reader: drain/ignore client messages and detect disconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Writer: push broadcasts + periodic pings.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case payload, ok := <-c.send:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
