package stream

import (
	"testing"
	"time"
)

// receive waits briefly for one event.
func receive(t *testing.T, sub *Subscription) (Event, bool) {
	t.Helper()

	select {
	case e, open := <-sub.Events():
		return e, open
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for an event")
		return Event{}, false
	}
}

func TestSubscriberReceivesPublishedEvent(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)
	defer sub.Close()

	b.Publish(EventTrade, "AAA", map[string]int{"qty": 5})

	event, _ := receive(t, sub)
	if event.Type != EventTrade {
		t.Errorf("type: want %s, got %s", EventTrade, event.Type)
	}
	if event.Symbol != "AAA" {
		t.Errorf("symbol: want AAA, got %s", event.Symbol)
	}
	if event.Seq == 0 {
		t.Errorf("events should carry a monotonic sequence number")
	}
	if event.Time == 0 {
		t.Errorf("events should be timestamped")
	}
}

func TestSequenceNumbersIncrease(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)
	defer sub.Close()

	b.Publish(EventTrade, "AAA", nil)
	b.Publish(EventTrade, "AAA", nil)

	first, _ := receive(t, sub)
	second, _ := receive(t, sub)

	if second.Seq <= first.Seq {
		t.Errorf("sequence must increase: %d then %d", first.Seq, second.Seq)
	}
}

func TestSymbolFilterExcludesOtherInstruments(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe([]string{"AAA"})
	defer sub.Close()

	b.Publish(EventTrade, "BBB", nil) // filtered out
	b.Publish(EventTrade, "AAA", nil)

	event, _ := receive(t, sub)
	if event.Symbol != "AAA" {
		t.Errorf("subscriber should only see AAA, got %s", event.Symbol)
	}

	select {
	case extra := <-sub.Events():
		t.Errorf("unexpected extra event for %s", extra.Symbol)
	default:
	}
}

// Events with no symbol (heartbeats and similar) reach every subscriber
// regardless of filter.
func TestUnsymbolledEventsReachFilteredSubscribers(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe([]string{"AAA"})
	defer sub.Close()

	b.Publish(EventHeartbeat, "", nil)

	event, _ := receive(t, sub)
	if event.Type != EventHeartbeat {
		t.Errorf("want a heartbeat, got %s", event.Type)
	}
}

func TestEmptyFilterReceivesAllSymbols(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)
	defer sub.Close()

	b.Publish(EventTrade, "AAA", nil)
	b.Publish(EventTrade, "BBB", nil)

	first, _ := receive(t, sub)
	second, _ := receive(t, sub)

	if first.Symbol == second.Symbol {
		t.Errorf("expected both symbols, got %s twice", first.Symbol)
	}
}

func TestMultipleSubscribersEachGetTheEvent(t *testing.T) {
	b := NewBroker()
	a := b.Subscribe(nil)
	defer a.Close()
	c := b.Subscribe(nil)
	defer c.Close()

	if b.SubscriberCount() != 2 {
		t.Errorf("want 2 subscribers, got %d", b.SubscriberCount())
	}

	b.Publish(EventTrade, "AAA", nil)

	if e, _ := receive(t, a); e.Type != EventTrade {
		t.Errorf("first subscriber missed the event")
	}
	if e, _ := receive(t, c); e.Type != EventTrade {
		t.Errorf("second subscriber missed the event")
	}
}

// The critical property. The matching loop calls Publish, so a client that
// stops reading must never be able to slow matching down.
func TestPublishNeverBlocksOnSlowSubscriber(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)
	defer sub.Close()

	// Deliberately never read from sub.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*4; i++ {
			b.Publish(EventTrade, "AAA", i)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Publish blocked on a subscriber that stopped reading")
	}

	if sub.Dropped() == 0 {
		t.Errorf("a slow subscriber should have events dropped and counted")
	}
}

func TestCloseUnregistersSubscriber(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)

	if b.SubscriberCount() != 1 {
		t.Fatalf("want 1 subscriber, got %d", b.SubscriberCount())
	}

	sub.Close()

	if b.SubscriberCount() != 0 {
		t.Errorf("want 0 subscribers after close, got %d", b.SubscriberCount())
	}

	// The channel must be closed so a reader's range loop terminates.
	if _, open := <-sub.Events(); open {
		t.Errorf("channel should be closed after Close")
	}
}

// Close is called from a defer on the request path, and a handler can plausibly
// unwind twice; a double close must not panic.
func TestCloseIsIdempotent(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(nil)

	sub.Close()
	sub.Close() // must not panic on a second call

	if b.SubscriberCount() != 0 {
		t.Errorf("want 0 subscribers, got %d", b.SubscriberCount())
	}
}

func TestPublishWithNoSubscribersIsSafe(t *testing.T) {
	b := NewBroker()
	// Must not panic or block when nobody is listening.
	b.Publish(EventTrade, "AAA", nil)

	if b.SubscriberCount() != 0 {
		t.Errorf("want 0 subscribers, got %d", b.SubscriberCount())
	}
}
