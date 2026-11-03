// Package realtime broadcasts job progress/log events to connected panel
// clients over WebSocket, so the UI can show live progress without polling.
package realtime

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Event struct {
	Type      string      `json:"type"` // "job_update" | "job_log"
	JobID     uint        `json:"job_id"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

type Hub struct {
	upgrader websocket.Upgrader

	mu      sync.RWMutex
	clients map[*client]struct{}
}

type client struct {
	conn *websocket.Conn
	send chan Event
}

func NewHub(allowedOrigin string) *Hub {
	return &Hub{
		clients: map[*client]struct{}{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return allowedOrigin == "*" || r.Header.Get("Origin") == "" || r.Header.Get("Origin") == allowedOrigin
			},
		},
	}
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade failed: %v", err)
		return
	}
	c := &client{conn: conn, send: make(chan Event, 32)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	go h.writePump(c)
	h.readPump(c)
}

func (h *Hub) readPump(c *client) {
	defer h.remove(c)
	c.conn.SetReadLimit(4096)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) writePump(c *client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case ev, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteJSON(ev); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
		_ = c.conn.Close()
	}
}

func (h *Hub) Broadcast(ev Event) {
	ev.Timestamp = time.Now()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- ev:
		default:
			// slow client, drop event rather than blocking the whole hub
		}
	}
}

func (h *Hub) JobUpdate(jobID uint, payload interface{}) {
	h.Broadcast(Event{Type: "job_update", JobID: jobID, Payload: payload})
}

func (h *Hub) JobLog(jobID uint, payload interface{}) {
	h.Broadcast(Event{Type: "job_log", JobID: jobID, Payload: payload})
}
