package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// realtimeHub is a tiny in-process fan-out used by the calendar stream. It
// keeps the deployment dependency-free: each connected webmail session gets a
// buffered channel and mutations publish a topic name. Consumers fall back to
// polling if the stream cannot be established.
type realtimeHub struct {
	mu     sync.Mutex
	subs   map[chan string]string
	closed bool
}

func newRealtimeHub() *realtimeHub {
	return &realtimeHub{subs: map[chan string]string{}}
}

func (hub *realtimeHub) subscribe(userID string) chan string {
	channel := make(chan string, 8)
	hub.mu.Lock()
	hub.subs[channel] = userID
	hub.mu.Unlock()
	return channel
}

func (hub *realtimeHub) unsubscribe(channel chan string) {
	hub.mu.Lock()
	delete(hub.subs, channel)
	hub.mu.Unlock()
	close(channel)
}

func (hub *realtimeHub) publish(userID, topic string) {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for channel, subscriber := range hub.subs {
		if subscriber != "" && subscriber != userID {
			continue
		}
		select {
		case channel <- topic:
		default:
		}
	}
}

func (s *Server) publishRealtime(topic string) {
	s.publishRealtimeTo("", topic)
}

func (s *Server) publishRealtimeTo(userID, topic string) {
	if s.realtime != nil {
		s.realtime.publish(userID, topic)
	}
}

func (s *Server) calendarStream(w http.ResponseWriter, r *http.Request) {
	s.eventStream(w, r)
}

func (s *Server) mailStream(w http.ResponseWriter, r *http.Request) {
	s.eventStream(w, r)
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "STREAM", "Streaming is not available")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	channel := s.realtime.subscribe(sess.User.ID)
	defer s.realtime.unsubscribe(channel)
	fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case topic, open := <-channel:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", topic, time.Now().UTC().Format(time.RFC3339))
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
