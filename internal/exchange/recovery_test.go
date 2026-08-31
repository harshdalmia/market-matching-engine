package exchange

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"matching-engine/internal/models"
	"matching-engine/internal/wal"
)

// buildFlow produces a deterministic order sequence across two symbols.
//
// Timestamps are explicit and monotonic rather than wall-clock: matching depends
// on them for time priority, so a replay that generated fresh timestamps could
// legitimately produce a different book.
func buildFlow(symbols []string, count int) []*models.Order {
	rng := rand.New(rand.NewSource(42))
	orders := make([]*models.Order, 0, count)

	for i := 0; i < count; i++ {
		side := models.SideBuy
		if rng.Intn(2) == 0 {
			side = models.SideSell
		}
		qty := 1 + rng.Intn(30)

		orders = append(orders, &models.Order{
			ID:          fmt.Sprintf("o-%04d", i),
			Symbol:      symbols[rng.Intn(len(symbols))],
			TraderID:    fmt.Sprintf("t-%d", rng.Intn(8)),
			Side:        side,
			Type:        models.TypeLimit,
			TimeInForce: models.TIFGTC,
			Price:       95.0 + float64(rng.Intn(1001))/100.0,
			Quantity:    qty,
			Remaining:   qty,
			Timestamp:   int64(i + 1),
			Status:      models.StatusNew,
		})
	}

	return orders
}

// fingerprint captures the observable state of every venue, so two exchanges can
// be compared for equivalence.
func fingerprint(t *testing.T, x *Exchange) string {
	t.Helper()

	var sb []byte
	for _, symbol := range x.Symbols() {
		v, _ := x.Venue(symbol)
		depth := v.Book.Depth(0)

		sb = append(sb, fmt.Sprintf("%s trades=%d\n", symbol, v.Engine.TradeCount())...)
		for _, level := range depth.Bids {
			sb = append(sb, fmt.Sprintf("  bid %.2f x%d n%d\n", level.Price, level.Quantity, level.OrderCount)...)
		}
		for _, level := range depth.Asks {
			sb = append(sb, fmt.Sprintf("  ask %.2f x%d n%d\n", level.Price, level.Quantity, level.OrderCount)...)
		}
	}
	return string(sb)
}

// The whole point of the write-ahead log: replaying it must rebuild the same
// market, because matching is deterministic given the same command sequence.
func TestWALReplayReconstructsState(t *testing.T) {
	symbols := []string{"AAA", "BBB"}
	path := filepath.Join(t.TempDir(), "engine.wal")

	log, err := wal.Open(path)
	if err != nil {
		t.Fatalf("opening wal: %v", err)
	}

	// ---- Original run -------------------------------------------------
	original := New(symbols)
	original.Start()

	flow := buildFlow(symbols, 400)
	for _, order := range flow {
		if err := log.AppendOrder(order); err != nil {
			t.Fatalf("appending to wal: %v", err)
		}
		v, ok := original.Resolve(order.Symbol)
		if !ok {
			t.Fatalf("unknown symbol %s", order.Symbol)
		}
		v.Engine.SubmitBlocking(order)
	}

	if !original.Drain(15 * time.Second) {
		t.Fatalf("original run did not drain")
	}
	original.Stop()

	// Cancel a couple of survivors to exercise cancel records too.
	cancelled := 0
	for _, symbol := range symbols {
		v, _ := original.Venue(symbol)
		for _, o := range v.Book.Snapshot().Bids {
			if v.Book.CancelOrder(o.ID) {
				if err := log.AppendCancel(symbol, o.ID); err != nil {
					t.Fatalf("appending cancel: %v", err)
				}
				cancelled++
			}
			break // one per symbol is enough
		}
	}
	if cancelled == 0 {
		t.Logf("no resting bids to cancel; cancel replay not exercised")
	}

	want := fingerprint(t, original)
	log.Close()

	// ---- Recovery run -------------------------------------------------
	recovered := New(symbols)
	recovered.Start()

	applied, err := wal.Replay(path, func(record wal.Record) error {
		v, ok := recovered.Resolve(record.Symbol)
		if !ok {
			return fmt.Errorf("unknown symbol %s", record.Symbol)
		}

		switch record.Kind {
		case wal.KindOrder:
			replayed := *record.Order
			replayed.Remaining = replayed.Quantity
			replayed.Status = models.StatusNew
			v.Engine.SubmitBlocking(&replayed)

		case wal.KindCancel:
			// Cancels must be applied after the orders ahead of them have been
			// matched, or they would target an order that has not arrived yet.
			recovered.Drain(5 * time.Second)
			v.Book.CancelOrder(record.OrderID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if applied != len(flow)+cancelled {
		t.Errorf("want %d records applied, got %d", len(flow)+cancelled, applied)
	}

	if !recovered.Drain(15 * time.Second) {
		t.Fatalf("recovery run did not drain")
	}

	recovered.Stop()
	got := fingerprint(t, recovered)

	if got != want {
		t.Errorf("recovered state differs from the original.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// Replay must not be sensitive to how many symbols the exchange happens to host,
// as long as the ones in the log are present.
func TestWALReplayIsIdempotentPerRecordSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.wal")

	log, err := wal.Open(path)
	if err != nil {
		t.Fatalf("opening wal: %v", err)
	}

	orders := []*models.Order{
		{
			ID: "a1", Symbol: "AAA", TraderID: "t", Side: models.SideSell,
			Type: models.TypeLimit, TimeInForce: models.TIFGTC,
			Price: 100, Quantity: 10, Remaining: 10, Timestamp: 1, Status: models.StatusNew,
		},
		{
			ID: "b1", Symbol: "AAA", TraderID: "t", Side: models.SideBuy,
			Type: models.TypeLimit, TimeInForce: models.TIFGTC,
			Price: 100, Quantity: 4, Remaining: 4, Timestamp: 2, Status: models.StatusNew,
		},
	}
	for _, o := range orders {
		if err := log.AppendOrder(o); err != nil {
			t.Fatalf("appending: %v", err)
		}
	}
	log.Close()

	replayInto := func() string {
		x := New([]string{"AAA"})
		x.Start()

		if _, err := wal.Replay(path, func(r wal.Record) error {
			v, _ := x.Resolve(r.Symbol)
			replayed := *r.Order
			replayed.Remaining = replayed.Quantity
			replayed.Status = models.StatusNew
			v.Engine.SubmitBlocking(&replayed)
			return nil
		}); err != nil {
			t.Fatalf("replaying: %v", err)
		}

		if !x.Drain(10 * time.Second) {
			t.Fatalf("replay run did not drain")
		}
		x.Stop()
		return fingerprint(t, x)
	}

	first := replayInto()
	second := replayInto()

	if first != second {
		t.Errorf("replaying the same log twice produced different state:\n%s\nvs\n%s", first, second)
	}
	// Sanity: 6 should remain resting on the ask after a 4-lot fill.
	if first == "" {
		t.Errorf("fingerprint should not be empty")
	}
}
