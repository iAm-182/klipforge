package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"klipforge/internal/model"
)

// Broker manages SSE (Server-Sent Events) subscribers for real-time dashboard updates
// and optionally forwards activity logs into a persisted activity feed sink.
type Broker struct {
	mu           sync.RWMutex
	subscribers  map[chan []byte]struct{}
	activitySink func(source, level, message string)
}

func NewBroker() *Broker {
	return &Broker{
		subscribers: make(map[chan []byte]struct{}),
	}
}

// SetActivitySink attaches a persistence callback invoked whenever Log() emits an event.
func (b *Broker) SetActivitySink(fn func(source, level, message string)) {
	b.mu.Lock()
	b.activitySink = fn
	b.mu.Unlock()
}

func (b *Broker) Publish(eventType string, payload interface{}) {
	evt := model.SSEEvent{
		Type:    eventType,
		Payload: payload,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- data:
		default:
			// Drop if slow client buffer is full so workers never block
		}
	}
}

func (b *Broker) Log(source, level, message string) {
	b.Publish("log", map[string]string{
		"source":  source,
		"level":   level,
		"message": message,
	})
	b.mu.RLock()
	sink := b.activitySink
	b.mu.RUnlock()
	if sink != nil {
		sink(source, level, message)
	}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan []byte, 64)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
		close(ch)
	}()

	// Initial handshake ping
	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
