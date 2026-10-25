package node

import (
	"sync"
	"time"
)

// Event is one observable node occurrence (local op, remote op, rejection,
// sync result, fault change, lifecycle). Events are measured facts, never
// synthetic.
type Event struct {
	Type string         `json:"type"`
	Node string         `json:"node"`
	TS   int64          `json:"ts_ms"`
	Data map[string]any `json:"data,omitempty"`
}

// EventBus fans events out to bounded subscribers (SSE). A slow subscriber
// loses its OLDEST buffered events rather than blocking writers; the event
// count stays truthful because every event carries a sequence number.
type EventBus struct {
	nodeID string
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	seq    uint64
}

type subscriber struct {
	ch   chan Event
	done chan struct{}
}

const subscriberBuffer = 128

func NewEventBus(nodeID string) *EventBus {
	return &EventBus{nodeID: nodeID, subs: map[*subscriber]struct{}{}}
}

// Subscribe registers a new bounded subscriber. Stop must be called to
// release it.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	sub := &subscriber{ch: make(chan Event, subscriberBuffer), done: make(chan struct{})}
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()
	return sub.ch, func() {
		close(sub.done)
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
	}
}

// Emit publishes one event; never blocks. Subscribers with a full buffer
// drop their oldest event to make room.
func (b *EventBus) Emit(eventType string, data map[string]any) {
	b.mu.Lock()
	b.seq++
	ev := Event{Type: eventType, Node: b.nodeID, TS: time.Now().UnixMilli(), Data: data}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	ev.Data["seq"] = b.seq
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		select {
		case <-s.done:
			continue
		default:
		}
		select {
		case s.ch <- ev:
		default:
			// drop oldest, then deliver the new one
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

// SubscriberCount reports active subscribers (bounded externally by the API
// server).
func (b *EventBus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
