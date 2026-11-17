package control

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type eventHub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	closed  bool
}

func newEventHub() *eventHub {
	return &eventHub{clients: map[chan []byte]struct{}{}}
}

func (h *eventHub) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	if !h.closed {
		h.clients[ch] = struct{}{}
	}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *eventHub) Broadcast(event string, payload any) {
	body, err := json.Marshal(map[string]any{
		"event": event, "timestamp": time.Now().UTC().Format(time.RFC3339), "data": payload,
	})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- body:
		default:
		}
	}
}

func (h *eventHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
	h.mu.Unlock()
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE", "Streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, ok := <-ch:
			if !ok {
				return
			}
			_, _ = w.Write([]byte("event: update\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		case <-keepalive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}
