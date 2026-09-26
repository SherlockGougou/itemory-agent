// Package events provides a tiny fan-out hub for SSE subscribers.
package events

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one server-sent event payload.
type Event struct {
	Kind string    `json:"kind"`
	Data any       `json:"data,omitempty"`
	Time time.Time `json:"time"`
}

// Hub broadcasts events to every subscriber.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
	last map[string]Event
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[chan Event]struct{}{}, last: map[string]Event{}}
}

// Subscribe returns a buffered channel; callers must Unsubscribe when done.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[ch] = struct{}{}
	// 按发生顺序回放：map 遍历顺序随机，新打开的控制台可能先收到 scan/done 再收到 scan/started，
	// 于是一直显示「扫描中」。
	replay := make([]Event, 0, len(h.last))
	for _, ev := range h.last {
		replay = append(replay, ev)
	}
	sort.Slice(replay, func(i, j int) bool { return replay[i].Time.Before(replay[j].Time) })
	for _, ev := range replay {
		select {
		case ch <- ev:
		default:
		}
	}
	return ch
}

// Unsubscribe removes and closes a subscriber channel.
func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// Broadcast delivers an event to all subscribers (never blocks).
func (h *Hub) Broadcast(kind string, data any) {
	ev := Event{Kind: kind, Data: data, Time: time.Now()}
	h.mu.Lock()
	defer h.mu.Unlock()
	// 扫描的各阶段事件只保留最新的一条：回放一条过期的 scan/started 会让控制台误以为仍在扫描。
	if strings.HasPrefix(kind, "scan/") {
		for existing := range h.last {
			if strings.HasPrefix(existing, "scan/") {
				delete(h.last, existing)
			}
		}
	}
	h.last[kind] = ev
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
