package store

import "sync"

// Hub fans events out to live SSE subscribers.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]string // chan -> task filter ("" = all)
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]string{}} }

func (h *Hub) Subscribe(taskID string) chan Event {
	ch := make(chan Event, 256)
	h.mu.Lock()
	h.subs[ch] = taskID
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, filter := range h.subs {
		if filter != "" && filter != ev.TaskID {
			continue
		}
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the agent
		}
	}
}
