// Package stream fans engine events out to live subscribers.
//
// The matching loop is single-threaded and latency-sensitive, so publishing must
// never block it. Every send here is non-blocking: a subscriber that cannot keep
// up has events dropped rather than being allowed to apply backpressure to
// matching. Dropped counts are tracked so a client can tell it fell behind.
package stream

import (
	"sync"
	"sync/atomic"
	"time"
)

// Event types carried on the stream.
const (
	EventTrade     = "trade"
	EventOrder     = "order"
	EventBook      = "book"
	EventSnapshot  = "snapshot"
	EventHeartbeat = "heartbeat"
)

// Buffer depth per subscriber. Deep enough to absorb a burst, shallow enough
// that a stalled client is detected quickly.
const subscriberBuffer = 256

// Event is one message on the stream.
type Event struct {
	Seq    uint64      `json:"seq"`
	Type   string      `json:"type"`
	Symbol string      `json:"symbol"`
	Time   int64       `json:"time"`
	Data   interface{} `json:"data,omitempty"`
}

type subscriber struct {
	id      uint64
	symbols map[string]struct{} // empty means every symbol
	ch      chan Event
	dropped atomic.Uint64
}

// wants reports whether this subscriber is interested in a symbol. Events with
// no symbol (heartbeats) always pass.
func (s *subscriber) wants(symbol string) bool {
	if len(s.symbols) == 0 || symbol == "" {
		return true
	}
	_, ok := s.symbols[symbol]
	return ok
}

// Broker is a fan-out registry of live subscribers.
type Broker struct {
	mu     sync.RWMutex
	subs   map[uint64]*subscriber
	nextID uint64
	seq    atomic.Uint64
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[uint64]*subscriber)}
}

// Subscription is a live handle. Close must be called to release it.
type Subscription struct {
	broker *Broker
	sub    *subscriber
}

// Events is the channel to read from.
func (s *Subscription) Events() <-chan Event { return s.sub.ch }

// Dropped is how many events this subscriber missed by falling behind.
func (s *Subscription) Dropped() uint64 { return s.sub.dropped.Load() }

// Close unregisters the subscriber.
func (s *Subscription) Close() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()

	if _, ok := s.broker.subs[s.sub.id]; !ok {
		return // already closed
	}
	delete(s.broker.subs, s.sub.id)
	close(s.sub.ch)
}

// Subscribe registers for events. An empty symbol list receives every symbol.
func (b *Broker) Subscribe(symbols []string) *Subscription {
	filter := make(map[string]struct{}, len(symbols))
	for _, s := range symbols {
		if s != "" {
			filter[s] = struct{}{}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	sub := &subscriber{
		id:      b.nextID,
		symbols: filter,
		ch:      make(chan Event, subscriberBuffer),
	}
	b.subs[sub.id] = sub

	return &Subscription{broker: b, sub: sub}
}

// SubscriberCount reports how many live subscribers are attached.
func (b *Broker) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Publish delivers an event to every interested subscriber.
//
// This is called from the matching goroutine, so it must return promptly. Sends
// use a non-blocking select: a full subscriber buffer means that client is too
// slow, and its event is dropped and counted rather than stalling matching.
func (b *Broker) Publish(eventType, symbol string, data interface{}) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.subs) == 0 {
		return // nothing to serialise for
	}

	event := Event{
		Seq:    b.seq.Add(1),
		Type:   eventType,
		Symbol: symbol,
		Time:   time.Now().UnixNano(),
		Data:   data,
	}

	for _, sub := range b.subs {
		if !sub.wants(symbol) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			sub.dropped.Add(1)
		}
	}
}
