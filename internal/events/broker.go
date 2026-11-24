package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"klipforge/internal/model"
)

type activityItem struct {
	source  string
	level   string
	message string
}

// Broker manages SSE (Server-Sent Events) subscribers for real-time dashboard updates
// and forwards activity logs asynchronously into a persisted activity feed sink without blocking workers.
type Broker struct {
	mu           sync.RWMutex
	subscribers  map[chan []byte]struct{}
	activitySink func(source, level, message string)
	activityCh   chan activityItem
}

func NewBroker() *Broker {
	b := &Broker{
		subscribers: make(map[chan []byte]struct{}),
		activityCh:  make(chan activityItem, 256),
	}
	go b.activityLoop()
	return b
}

func (b *Broker) activityLoop() {
	for item := range b.activityCh {
		b.mu.RLock()
		sink := b.activitySink
		b.mu.RUnlock()
		if sink != nil {
			sink(item.source, item.level, item.message)
		}
	}
}

// SetActivitySink attaches a persistence callback invoked asynchronously whenever Log() emits an event.
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
	select {
	case b.activityCh <- activityItem{source: source, level: level, message: message}:
	default:
		// Never block FFmpeg workers if activity persistence lags
	}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Note: Do NOT override Access-Control-Allow-Origin here — corsMiddleware governs CORS with credentials cleanly!
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

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

	rc := http.NewResponseController(w)
	writeWithDeadline := func(format string, args ...interface{}) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial handshake ping
	if !writeWithDeadline("event: connected\ndata: {\"ok\":true}\n\n") {
		return
	}

	// Keep-alive heartbeat every 20s so Cloudflare Edge proxies never drop idle SSE connections
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if !writeWithDeadline(": ping\n\n") {
				return
			}
		case msg := <-ch:
			if !writeWithDeadline("data: %s\n\n", msg) {
				return
			}
		}
	}
}
