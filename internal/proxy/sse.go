package proxy

import (
	"fmt"
	"net/http"
	"sync"
)

var SSEHub = NewSSEHub()

type sseHub struct {
	mu      sync.RWMutex
	clients map[chan string]bool
}

func NewSSEHub() *sseHub {
	return &sseHub{
		clients: make(map[chan string]bool),
	}
}

func (h *sseHub) AddClient(ch chan string) {
	h.mu.Lock()
	h.clients[ch] = true
	h.mu.Unlock()
}

func (h *sseHub) RemoveClient(ch chan string) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
}

func (h *sseHub) Broadcast(event, data string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
			// 跳过慢客户端
		}
	}
}

func SSEHandler(hub *sseHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		ch := make(chan string, 64)
		hub.AddClient(ch)

		defer hub.RemoveClient(ch)

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprint(w, msg)
				flusher.Flush()
			}
		}
	}
}
