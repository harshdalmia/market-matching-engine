package engine

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
)

// -----------------------------------------------------------------------
// Helpers
//
// Tests drive processOrder directly rather than going through Submit and the
// matching goroutine. Matching is single-threaded by design, so calling it
// synchronously exercises exactly the same code path without needing to poll
// for the channel to drain.
// -----------------------------------------------------------------------

func newTestEngine() (*Engine, *orderbook.OrderBook) {
	ob := orderbook.New()
	return New(ob), ob
}

var seq int64

func order(id, trader, side string, price float64, qty int) *models.Order {
	seq++
	return &models.Order{
		ID:        id,
		TraderID:  trader,
		Side:      side,
		Price:     price,
		Quantity:  qty,
		Remaining: qty,
		Timestamp: seq, // monotonic, so time priority is deterministic
		Status:    models.StatusNew,
	}
}

// restingAt returns the aggregated quantity resting at a price on one side.
func restingAt(t *testing.T, ob *orderbook.OrderBook, side string, price float64) int {
	t.Helper()

	snap := ob.Snapshot()
	orders := snap.Bids
	if side == models.SideSell {
		orders = snap.Asks
	}

	total := 0
	for _, o := range orders {
		if o.Price == price {
			total += o.Remaining
		}
	}
	return total
}

// -----------------------------------------------------------------------
// Regression tests for the three matching bugs
// -----------------------------------------------------------------------

// A sell that only partially consumes the best bid must leave the remainder of
// that bid resting. The original code popped the bid, filled it, and then only
// ever re-added the *sell*, so the bid's remaining quantity vanished.
func TestPartiallyFilledRestingBidSurvives(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("bid-1", "carol", models.SideBuy, 99.00, 10))
	if got := restingAt(t, ob, models.SideBuy, 99.00); got != 10 {
		t.Fatalf("setup: want 10 resting on the bid, got %d", got)
	}

	e.processOrder(order("ask-1", "dave", models.SideSell, 99.00, 3))

	if got := restingAt(t, ob, models.SideBuy, 99.00); got != 7 {
		t.Errorf("resting bid remainder: want 7, got %d (liquidity destroyed)", got)
	}

	trades := e.GetTrades(0)
	if len(trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(trades))
	}
	if trades[0].Quantity != 3 {
		t.Errorf("fill quantity: want 3, got %d", trades[0].Quantity)
	}
}

// The mirror case, which always worked: a buy partially consuming the best ask.
func TestPartiallyFilledRestingAskSurvives(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("ask-1", "alice", models.SideSell, 100.00, 10))
	e.processOrder(order("bid-1", "bob", models.SideBuy, 100.00, 4))

	if got := restingAt(t, ob, models.SideSell, 100.00); got != 6 {
		t.Errorf("resting ask remainder: want 6, got %d", got)
	}
}

// A partially filled incoming order must appear in the book exactly once.
// executeTrade used to add it, and then processOrder added the same pointer
// again, producing duplicate IDs and double-counted depth.
func TestIncomingOrderIsNotDoubleAdded(t *testing.T) {
	e, ob := newTestEngine()

	// One resting bid for 4, then a sell for 10 — the sell rests with 6 left.
	e.processOrder(order("bid-1", "carol", models.SideBuy, 99.00, 4))
	e.processOrder(order("ask-1", "dave", models.SideSell, 99.00, 10))

	snap := ob.Snapshot()

	seen := map[string]int{}
	for _, o := range snap.Asks {
		seen[o.ID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("order %s appears %d times in the book", id, count)
		}
	}

	if got := restingAt(t, ob, models.SideSell, 99.00); got != 6 {
		t.Errorf("resting sell remainder: want 6, got %d", got)
	}
}

// Trades print at the resting (maker) order's price regardless of which side
// was the aggressor. The original code always used the sell's price, which is
// the taker's limit on sell-aggressor flow.
func TestTradePricesAtRestingOrderPrice(t *testing.T) {
	tests := []struct {
		name      string
		resting   *models.Order
		incoming  *models.Order
		wantPrice float64
	}{
		{
			name:      "buy aggressor lifts a resting ask",
			resting:   order("ask-1", "maker", models.SideSell, 100.00, 5),
			incoming:  order("bid-1", "taker", models.SideBuy, 101.00, 5),
			wantPrice: 100.00,
		},
		{
			name:      "sell aggressor hits a resting bid",
			resting:   order("bid-1", "maker", models.SideBuy, 100.00, 5),
			incoming:  order("ask-1", "taker", models.SideSell, 99.00, 5),
			wantPrice: 100.00,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newTestEngine()

			e.processOrder(tc.resting)
			e.processOrder(tc.incoming)

			trades := e.GetTrades(0)
			if len(trades) != 1 {
				t.Fatalf("want 1 trade, got %d", len(trades))
			}
			if trades[0].Price != tc.wantPrice {
				t.Errorf("trade price: want %.2f, got %.2f", tc.wantPrice, trades[0].Price)
			}
		})
	}
}

// A resting order that is partially filled keeps its position in the queue at
// its price. Filling in place preserves this; popping and re-adding would too,
// but only if the timestamp were preserved — this pins the behaviour either way.
func TestPartialFillPreservesTimePriority(t *testing.T) {
	e, _ := newTestEngine()

	// Two bids at the same price. early-1 must be filled first and, after a
	// partial fill, must still be ahead of late-1.
	e.processOrder(order("early-1", "first", models.SideBuy, 100.00, 10))
	e.processOrder(order("late-1", "second", models.SideBuy, 100.00, 10))

	e.processOrder(order("ask-1", "taker", models.SideSell, 100.00, 4))
	e.processOrder(order("ask-2", "taker", models.SideSell, 100.00, 4))

	trades := e.GetTrades(0)
	if len(trades) != 2 {
		t.Fatalf("want 2 trades, got %d", len(trades))
	}
	for i, tr := range trades {
		if tr.BuyOrderID != "early-1" {
			t.Errorf("trade %d: want buy side early-1 (time priority), got %s", i, tr.BuyOrderID)
		}
	}
}

// -----------------------------------------------------------------------
// Matching behaviour
// -----------------------------------------------------------------------

func TestNonCrossingOrdersRest(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("bid-1", "a", models.SideBuy, 99.00, 5))
	e.processOrder(order("ask-1", "b", models.SideSell, 101.00, 5))

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("a bid below the ask must not trade")
	}
	if got := restingAt(t, ob, models.SideBuy, 99.00); got != 5 {
		t.Errorf("bid: want 5 resting, got %d", got)
	}
	if got := restingAt(t, ob, models.SideSell, 101.00); got != 5 {
		t.Errorf("ask: want 5 resting, got %d", got)
	}
}

func TestAggressorSweepsMultipleLevels(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("ask-1", "m1", models.SideSell, 100.00, 5))
	e.processOrder(order("ask-2", "m2", models.SideSell, 101.00, 5))
	e.processOrder(order("ask-3", "m3", models.SideSell, 102.00, 5))

	// Buy 12 at 101 — takes all of 100.00, all of 101.00, stops before 102.00.
	e.processOrder(order("bid-1", "taker", models.SideBuy, 101.00, 12))

	trades := e.GetTrades(0)
	if len(trades) != 2 {
		t.Fatalf("want 2 trades, got %d", len(trades))
	}

	// Cheapest liquidity is consumed first.
	if trades[0].Price != 100.00 || trades[1].Price != 101.00 {
		t.Errorf("price priority violated: got %.2f then %.2f", trades[0].Price, trades[1].Price)
	}

	// 12 - 5 - 5 = 2 left, resting at the buy's own limit.
	if got := restingAt(t, ob, models.SideBuy, 101.00); got != 2 {
		t.Errorf("aggressor remainder: want 2 resting, got %d", got)
	}
	if got := restingAt(t, ob, models.SideSell, 102.00); got != 5 {
		t.Errorf("untouched level: want 5 resting, got %d", got)
	}
}

func TestCancelledOrdersAreSkipped(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("ask-1", "m1", models.SideSell, 100.00, 5))
	e.processOrder(order("ask-2", "m2", models.SideSell, 101.00, 5))

	if !ob.CancelOrder("ask-1") {
		t.Fatalf("cancel of a resting order should succeed")
	}

	e.processOrder(order("bid-1", "taker", models.SideBuy, 101.00, 5))

	trades := e.GetTrades(0)
	if len(trades) != 1 {
		t.Fatalf("want 1 trade, got %d", len(trades))
	}
	if trades[0].Price != 101.00 {
		t.Errorf("cancelled level must be skipped: traded at %.2f", trades[0].Price)
	}
}

// A partially filled resting order must still be cancellable. Filling in place
// keeps it in the ID index; the old pop-then-re-add briefly removed it, so a
// cancel arriving in that window returned 404 for a visibly resting order.
func TestPartiallyFilledOrderRemainsCancellable(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(order("bid-1", "carol", models.SideBuy, 99.00, 10))
	e.processOrder(order("ask-1", "dave", models.SideSell, 99.00, 3))

	if !ob.CancelOrder("bid-1") {
		t.Fatalf("partially filled resting order should still be cancellable")
	}
	if got := restingAt(t, ob, models.SideBuy, 99.00); got != 0 {
		t.Errorf("cancelled order should leave the snapshot, %d still resting", got)
	}
}

func TestRejectsInvalidOrders(t *testing.T) {
	tests := []struct {
		name  string
		order *models.Order
	}{
		{"zero price", order("x1", "a", models.SideBuy, 0, 5)},
		{"negative price", order("x2", "a", models.SideBuy, -1, 5)},
		{"zero quantity", order("x3", "a", models.SideBuy, 100, 0)},
		{"negative quantity", order("x4", "a", models.SideBuy, 100, -5)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, ob := newTestEngine()
			e.processOrder(tc.order)

			if tc.order.Status != models.StatusRejected {
				t.Errorf("status: want REJECTED, got %s", tc.order.Status)
			}
			if ob.Len() != 0 {
				t.Errorf("rejected order must not rest in the book")
			}
		})
	}
}

func TestTradeHistoryIsBounded(t *testing.T) {
	e, _ := newTestEngine()

	// Each crossing pair produces one trade.
	for i := 0; i < maxTradeHistory+50; i++ {
		e.processOrder(order(fmt.Sprintf("b%d", i), "a", models.SideBuy, 100.00, 1))
		e.processOrder(order(fmt.Sprintf("s%d", i), "b", models.SideSell, 100.00, 1))
	}

	if got := e.TradeCount(); got != maxTradeHistory {
		t.Errorf("retained trades: want the cap %d, got %d", maxTradeHistory, got)
	}
}

func TestGetTradesLimitReturnsMostRecent(t *testing.T) {
	e, _ := newTestEngine()

	for i := 0; i < 10; i++ {
		e.processOrder(order(fmt.Sprintf("b%d", i), "a", models.SideBuy, float64(100+i), 1))
		e.processOrder(order(fmt.Sprintf("s%d", i), "b", models.SideSell, float64(100+i), 1))
	}

	all := e.GetTrades(0)
	if len(all) != 10 {
		t.Fatalf("want 10 trades, got %d", len(all))
	}

	tail := e.GetTrades(3)
	if len(tail) != 3 {
		t.Fatalf("want 3 trades, got %d", len(tail))
	}
	// Oldest-first within the window, anchored on the newest print.
	for i := range tail {
		if tail[i].ID != all[len(all)-3+i].ID {
			t.Errorf("limit should return the most recent trades in order")
		}
	}
}

func TestSubmitShedsLoadWhenSaturated(t *testing.T) {
	// A stopped engine never drains, so the buffer fills and stays full.
	e, _ := newTestEngine()

	accepted := 0
	for i := 0; i < cap(e.orderChan)+10; i++ {
		if e.Submit(order(fmt.Sprintf("o%d", i), "a", models.SideBuy, 100, 1)) {
			accepted++
		}
	}

	if accepted != cap(e.orderChan) {
		t.Errorf("want exactly %d accepted before shedding, got %d", cap(e.orderChan), accepted)
	}
	if e.Submit(order("overflow", "a", models.SideBuy, 100, 1)) {
		t.Errorf("Submit must report false once the buffer is full")
	}
}

// -----------------------------------------------------------------------
// Property tests
//
// These assert invariants that must hold for *any* order flow, which is what
// catches the classes of bug that hand-picked cases miss.
// -----------------------------------------------------------------------

// randomFlow drives a deterministic pseudo-random order flow through an engine.
func randomFlow(t *testing.T, seed int64, count int) (*Engine, *orderbook.OrderBook, []*models.Order) {
	t.Helper()

	e, ob := newTestEngine()
	rng := rand.New(rand.NewSource(seed))
	submitted := make([]*models.Order, 0, count)

	for i := 0; i < count; i++ {
		side := models.SideBuy
		if rng.Intn(2) == 0 {
			side = models.SideSell
		}
		price := 95.0 + float64(rng.Intn(1001))/100.0 // 95.00–105.00
		qty := 1 + rng.Intn(50)

		o := order(fmt.Sprintf("o-%d", i), fmt.Sprintf("t-%d", rng.Intn(10)), side, price, qty)
		submitted = append(submitted, o)
		e.processOrder(o)
	}

	return e, ob, submitted
}

// After any flow, the best bid must be strictly below the best ask. A crossed
// book means the matching loop stopped early and left money on the table.
func TestPropertyBookNeverCrossed(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		e, ob, _ := randomFlow(t, seed, 400)
		_ = e

		depth := ob.Depth(0)
		if depth.BestBid == nil || depth.BestAsk == nil {
			continue
		}
		if *depth.BestBid >= *depth.BestAsk {
			t.Fatalf("seed %d: crossed book, best bid %.2f >= best ask %.2f",
				seed, *depth.BestBid, *depth.BestAsk)
		}
	}
}

// No order may ever be filled for more than it was submitted for.
func TestPropertyFillsNeverExceedQuantity(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		e, _, submitted := randomFlow(t, seed, 400)

		filled := map[string]int{}
		for _, tr := range e.GetTrades(0) {
			filled[tr.BuyOrderID] += tr.Quantity
			filled[tr.SellOrderID] += tr.Quantity
		}

		for _, o := range submitted {
			if filled[o.ID] > o.Quantity {
				t.Fatalf("seed %d: order %s filled %d but was only for %d",
					seed, o.ID, filled[o.ID], o.Quantity)
			}
			// Remaining must always account for exactly what was filled.
			if o.Status != models.StatusRejected && o.Quantity-filled[o.ID] != o.Remaining {
				t.Fatalf("seed %d: order %s quantity %d, filled %d, remaining %d — does not reconcile",
					seed, o.ID, o.Quantity, filled[o.ID], o.Remaining)
			}
		}
	}
}

// Quantity is conserved: every unit submitted is either filled, still resting,
// or was never eligible to rest. Nothing may simply disappear — which is
// exactly what the resting-bid bug did.
func TestPropertyQuantityIsConserved(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		e, ob, submitted := randomFlow(t, seed, 400)

		submittedTotal := 0
		for _, o := range submitted {
			submittedTotal += o.Quantity
		}

		// Each trade consumes one unit from a buy and one from a sell.
		filledTotal := 0
		for _, tr := range e.GetTrades(0) {
			filledTotal += tr.Quantity * 2
		}

		restingTotal := 0
		snap := ob.Snapshot()
		for _, o := range append(append([]*models.Order{}, snap.Bids...), snap.Asks...) {
			restingTotal += o.Remaining
		}

		if filledTotal+restingTotal != submittedTotal {
			t.Fatalf("seed %d: %d submitted but %d filled + %d resting = %d",
				seed, submittedTotal, filledTotal, restingTotal, filledTotal+restingTotal)
		}
	}
}

// Every trade must be priced within the crossing range of the two orders that
// produced it, and must equal the resting order's price.
func TestPropertyTradePricesAreSane(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		e, _, submitted := randomFlow(t, seed, 400)

		byID := map[string]*models.Order{}
		for _, o := range submitted {
			byID[o.ID] = o
		}

		for _, tr := range e.GetTrades(0) {
			buy, sell := byID[tr.BuyOrderID], byID[tr.SellOrderID]
			if buy == nil || sell == nil {
				t.Fatalf("seed %d: trade references an unknown order", seed)
			}
			// A trade only happens when the buy limit is at or above the sell
			// limit, and the print must land inside that band.
			if tr.Price < sell.Price || tr.Price > buy.Price {
				t.Fatalf("seed %d: trade at %.2f outside [%.2f, %.2f]",
					seed, tr.Price, sell.Price, buy.Price)
			}
			if tr.Quantity <= 0 {
				t.Fatalf("seed %d: non-positive trade quantity %d", seed, tr.Quantity)
			}
		}
	}
}

// -----------------------------------------------------------------------
// Concurrency
// -----------------------------------------------------------------------

// Drives the real Start/Submit path from many goroutines while others read
// snapshots, then checks that quantity still reconciles.
//
// This asserts logical consistency under concurrent access; it does not detect
// data races on its own. Run `go test -race ./...` for that — CI does, on Linux,
// because the race detector needs a working 64-bit cgo toolchain.
func TestConcurrentSubmitAndRead(t *testing.T) {
	ob := orderbook.New()
	e := New(ob)
	e.Start()

	const writers = 8
	const perWriter = 250

	submittedQty := make([]int, writers)

	var writeWG sync.WaitGroup
	for w := 0; w < writers; w++ {
		writeWG.Add(1)
		go func(w int) {
			defer writeWG.Done()
			rng := rand.New(rand.NewSource(int64(w) + 1))

			for i := 0; i < perWriter; i++ {
				side := models.SideBuy
				if rng.Intn(2) == 0 {
					side = models.SideSell
				}
				qty := 1 + rng.Intn(20)

				o := &models.Order{
					ID:        fmt.Sprintf("c-%d-%d", w, i),
					TraderID:  fmt.Sprintf("t-%d", w),
					Side:      side,
					Price:     95.0 + float64(rng.Intn(1001))/100.0,
					Quantity:  qty,
					Remaining: qty,
					Timestamp: time.Now().UnixNano(),
					Status:    models.StatusNew,
				}

				// Retry rather than drop, so the accounting below stays exact.
				for !e.Submit(o) {
					time.Sleep(time.Millisecond)
				}
				submittedQty[w] += qty
			}
		}(w)
	}

	// Readers run throughout while writers submit.
	stop := make(chan struct{})
	var readWG sync.WaitGroup
	for r := 0; r < 4; r++ {
		readWG.Add(1)
		go func() {
			defer readWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
					e.GetTrades(50)
					e.Metrics()
				}
			}
		}()
	}

	writeWG.Wait()

	// Let the matching loop drain.
	deadline := time.Now().Add(15 * time.Second)
	for e.QueueDepth() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)

	close(stop)
	readWG.Wait()

	if e.QueueDepth() != 0 {
		t.Fatalf("engine did not drain: %d orders still queued", e.QueueDepth())
	}

	// The engine mutates resting orders in place during fills, so stop it before
	// reading book snapshots to avoid racing with in-flight writes.
	e.Stop()

	total := 0
	for _, q := range submittedQty {
		total += q
	}

	filled := 0
	for _, tr := range e.GetTrades(0) {
		filled += tr.Quantity * 2
	}

	resting := 0
	snap := ob.Snapshot()
	for _, o := range snap.Bids {
		resting += o.Remaining
	}
	for _, o := range snap.Asks {
		resting += o.Remaining
	}

	if filled+resting != total {
		t.Errorf("quantity not conserved under concurrency: %d submitted, %d filled + %d resting = %d",
			total, filled, resting, filled+resting)
	}
}
