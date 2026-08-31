package engine

import (
	"testing"

	"matching-engine/internal/models"
)

// stopOrder builds a stop order. price 0 makes it a stop-market.
func stopOrder(id, side string, stopPrice, price float64, qty int) *models.Order {
	o := typed(id, side, models.TypeStop, models.TIFGTC, price, qty)
	o.StopPrice = stopPrice
	return o
}

// establishPrice trades once so the engine has a last price to compare stops
// against. Returns the price that printed.
func establishPrice(t *testing.T, e *Engine, price float64, qty int) {
	t.Helper()

	e.processOrder(order("seed-ask-"+t.Name(), "maker", models.SideSell, price, qty))
	e.processOrder(order("seed-bid-"+t.Name(), "taker", models.SideBuy, price, qty))

	if e.LastPrice() != price {
		t.Fatalf("setup: last price should be %.2f, got %.2f", price, e.LastPrice())
	}
}

// -----------------------------------------------------------------------
// Parking
// -----------------------------------------------------------------------

// A stop is not tradable liquidity, so it must stay out of the book entirely
// until it triggers — otherwise it would show up as depth nobody can hit.
func TestStopIsHeldOffBook(t *testing.T) {
	e, ob := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 105.00, 110.00, 5)
	e.processOrder(stop)

	if stop.Status != models.StatusPending {
		t.Errorf("status: want PENDING, got %s", stop.Status)
	}
	if e.PendingStopCount() != 1 {
		t.Errorf("want 1 pending stop, got %d", e.PendingStopCount())
	}

	for _, o := range append(ob.Snapshot().Bids, ob.Snapshot().Asks...) {
		if o.ID == "s1" {
			t.Errorf("a pending stop must not appear in the book")
		}
	}
}

// With no prints yet there is no price to compare against. Treating the zero
// value as a real price would fire every sell stop the instant it arrived.
func TestStopDoesNotTriggerBeforeAnyTrade(t *testing.T) {
	e, _ := newTestEngine()

	stop := stopOrder("s1", models.SideSell, 100.00, 90.00, 5)
	e.processOrder(stop)

	if stop.Status != models.StatusPending {
		t.Errorf("status: want PENDING, got %s", stop.Status)
	}
	if e.PendingStopCount() != 1 {
		t.Errorf("stop should still be waiting, got %d pending", e.PendingStopCount())
	}
}

func TestStopWithoutTriggerPriceIsRejected(t *testing.T) {
	e, _ := newTestEngine()

	stop := stopOrder("s1", models.SideBuy, 0, 100.00, 5)
	e.processOrder(stop)

	if stop.Status != models.StatusRejected {
		t.Errorf("status: want REJECTED, got %s", stop.Status)
	}
	if e.PendingStopCount() != 0 {
		t.Errorf("a rejected stop must not be parked")
	}
}

// -----------------------------------------------------------------------
// Triggering
// -----------------------------------------------------------------------

func TestBuyStopTriggersWhenPriceRises(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 105.00, 110.00, 5)
	e.processOrder(stop)

	// Ten at 106: five for the trade that lifts the price, five for the stop.
	e.processOrder(order("a2", "maker", models.SideSell, 106.00, 10))
	if e.PendingStopCount() != 1 {
		t.Fatalf("resting an ask must not trigger anything")
	}

	// Print at 106, above the 105 trigger.
	e.processOrder(order("b2", "taker", models.SideBuy, 106.00, 5))

	if e.PendingStopCount() != 0 {
		t.Errorf("stop should have triggered, %d still pending", e.PendingStopCount())
	}
	if stop.Status != models.StatusFilled {
		t.Errorf("triggered stop should have filled, status %s", stop.Status)
	}

	trades := e.GetTrades(0)
	last := trades[len(trades)-1]
	if last.Price != 106.00 || last.Quantity != 5 {
		t.Errorf("stop fill: want 5 @106, got %d @%.2f", last.Quantity, last.Price)
	}
}

func TestSellStopTriggersWhenPriceFalls(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	// Sell stop below the market: the classic stop-loss.
	stop := stopOrder("s1", models.SideSell, 95.00, 90.00, 5)
	e.processOrder(stop)

	if e.PendingStopCount() != 1 {
		t.Fatalf("setup: stop should be pending")
	}

	// Bids at 94 so a print can occur below the trigger and the stop has
	// somewhere to sell into.
	e.processOrder(order("b2", "maker", models.SideBuy, 94.00, 10))
	e.processOrder(order("a2", "taker", models.SideSell, 94.00, 5))

	if e.PendingStopCount() != 0 {
		t.Errorf("stop should have triggered, %d still pending", e.PendingStopCount())
	}
	if stop.Status != models.StatusFilled {
		t.Errorf("triggered stop should have filled, status %s", stop.Status)
	}
}

func TestStopStaysPendingWhileTriggerNotReached(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 120.00, 130.00, 5)
	e.processOrder(stop)

	// Trade up to 110 — still short of the 120 trigger.
	e.processOrder(order("a2", "maker", models.SideSell, 110.00, 5))
	e.processOrder(order("b2", "taker", models.SideBuy, 110.00, 5))

	if e.PendingStopCount() != 1 {
		t.Errorf("stop must not trigger below its price")
	}
	if stop.Status != models.StatusPending {
		t.Errorf("status: want PENDING, got %s", stop.Status)
	}
}

// A stop already in the money when it arrives fires straight away rather than
// waiting for another print that may never come.
func TestStopAlreadyThroughTriggerFiresImmediately(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	// Liquidity for it to take.
	e.processOrder(order("a2", "maker", models.SideSell, 101.00, 5))

	// Buy stop at 90, well below the last price of 100.
	stop := stopOrder("s1", models.SideBuy, 90.00, 105.00, 5)
	e.processOrder(stop)

	if e.PendingStopCount() != 0 {
		t.Errorf("an already-triggered stop should not be parked")
	}
	if stop.Status != models.StatusFilled {
		t.Errorf("status: want FILLED, got %s", stop.Status)
	}
}

// -----------------------------------------------------------------------
// Conversion on trigger
// -----------------------------------------------------------------------

// A stop with no price becomes a market order: it takes whatever is there
// rather than resting.
func TestStopMarketSweepsOnTrigger(t *testing.T) {
	e, ob := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 101.00, 0, 8)
	e.processOrder(stop)

	// Two levels totalling 9. The print below takes 1, leaving exactly the 8 the
	// stop needs to fill completely.
	e.processOrder(order("a2", "maker", models.SideSell, 102.00, 4))
	e.processOrder(order("a3", "maker", models.SideSell, 103.00, 5))

	// Print at 102 to cross the trigger.
	e.processOrder(order("b2", "taker", models.SideBuy, 102.00, 1))

	if stop.Type != models.TypeMarket {
		t.Errorf("a priceless stop should convert to MARKET, got %s", stop.Type)
	}
	if stop.Status != models.StatusFilled {
		t.Errorf("status: want FILLED, got %s", stop.Status)
	}
	if ob.Len() != 0 {
		t.Errorf("the stop should have consumed both levels, %d resting", ob.Len())
	}
}

// A stop with a price becomes a limit order, and therefore rests if it cannot
// fill completely.
func TestStopLimitRestsRemainderOnTrigger(t *testing.T) {
	e, ob := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 101.00, 102.00, 10)
	e.processOrder(stop)

	// Only 3 available at or below the stop's 102 limit.
	e.processOrder(order("a2", "maker", models.SideSell, 102.00, 3))
	e.processOrder(order("b2", "taker", models.SideBuy, 102.00, 1))

	if stop.Type != models.TypeLimit {
		t.Errorf("a priced stop should convert to LIMIT, got %s", stop.Type)
	}
	if stop.Status != models.StatusPartial {
		t.Errorf("status: want PARTIALLY_FILLED, got %s", stop.Status)
	}
	if restingAt(t, ob, models.SideBuy, 102.00) == 0 {
		t.Errorf("the unfilled remainder should be resting at its limit")
	}
}

// -----------------------------------------------------------------------
// Cascades
// -----------------------------------------------------------------------

// One stop's fill can move the price enough to trigger another. The cascade is
// processed iteratively, so a long chain must not recurse.
func TestStopCascadeActivatesFurtherStops(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	// First stop sweeps aggressively enough to reach the second's trigger.
	first := stopOrder("s1", models.SideBuy, 101.00, 0, 10)
	e.processOrder(first)

	second := stopOrder("s2", models.SideBuy, 120.00, 0, 5)
	e.processOrder(second)

	if e.PendingStopCount() != 2 {
		t.Fatalf("setup: want 2 pending stops, got %d", e.PendingStopCount())
	}

	// Liquidity spanning both triggers.
	e.processOrder(order("a2", "maker", models.SideSell, 102.00, 4))
	e.processOrder(order("a3", "maker", models.SideSell, 125.00, 20))

	// Print at 102 starts the chain.
	e.processOrder(order("b2", "taker", models.SideBuy, 102.00, 1))

	if e.PendingStopCount() != 0 {
		t.Errorf("both stops should have triggered, %d still pending", e.PendingStopCount())
	}
	if second.Status == models.StatusPending {
		t.Errorf("the second stop should have been activated by the cascade")
	}
}

// -----------------------------------------------------------------------
// Cancellation
// -----------------------------------------------------------------------

func TestPendingStopIsCancellable(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 150.00, 160.00, 5)
	e.processOrder(stop)

	if !e.CancelPendingStop("s1") {
		t.Fatalf("a pending stop should be cancellable")
	}
	if stop.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", stop.Status)
	}
	if e.PendingStopCount() != 0 {
		t.Errorf("cancelled stop should be gone, %d pending", e.PendingStopCount())
	}
}

func TestCancelUnknownStopFails(t *testing.T) {
	e, _ := newTestEngine()

	if e.CancelPendingStop("nope") {
		t.Errorf("cancelling an unknown stop should fail")
	}
}

// A cancelled stop must not come back to life when the market reaches its
// trigger price.
func TestCancelledStopDoesNotTrigger(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 105.00, 110.00, 5)
	e.processOrder(stop)
	e.CancelPendingStop("s1")

	tradesBefore := len(e.GetTrades(0))

	e.processOrder(order("a2", "maker", models.SideSell, 106.00, 10))
	e.processOrder(order("b2", "taker", models.SideBuy, 106.00, 5))

	// Only the one deliberate print, nothing from the cancelled stop.
	if got := len(e.GetTrades(0)); got != tradesBefore+1 {
		t.Errorf("cancelled stop should not have traded: %d trades added", got-tradesBefore)
	}
}

func TestPendingStopsListing(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	e.processOrder(stopOrder("s1", models.SideBuy, 150.00, 160.00, 5))
	e.processOrder(stopOrder("s2", models.SideBuy, 160.00, 170.00, 5))

	pending := e.PendingStops()
	if len(pending) != 2 {
		t.Fatalf("want 2 pending stops, got %d", len(pending))
	}
	// Ordered by submission time so a cascade is deterministic.
	if pending[0].Timestamp > pending[1].Timestamp {
		t.Errorf("pending stops should be ordered by submission time")
	}
	// Listing returns copies, so mutating one must not affect the engine.
	pending[0].StopPrice = 1
	if e.PendingStops()[0].StopPrice == 1 {
		t.Errorf("PendingStops must not expose internal state")
	}
}

// A triggered stop is rewritten to LIMIT or MARKET, but its trigger price must
// survive: it is the only record of why the order reached the book, and the
// order history displays it.
func TestTriggeredStopRetainsItsTriggerPrice(t *testing.T) {
	e, _ := newTestEngine()
	establishPrice(t, e, 100.00, 5)

	stop := stopOrder("s1", models.SideBuy, 105.00, 110.00, 5)
	e.processOrder(stop)

	e.processOrder(order("a2", "maker", models.SideSell, 106.00, 10))
	e.processOrder(order("b2", "taker", models.SideBuy, 106.00, 5))

	if stop.Status != models.StatusFilled {
		t.Fatalf("setup: stop should have triggered and filled, got %s", stop.Status)
	}
	if stop.Type != models.TypeLimit {
		t.Errorf("type after trigger: want LIMIT, got %s", stop.Type)
	}
	if stop.StopPrice != 105.00 {
		t.Errorf("trigger price should survive activation, got %.2f", stop.StopPrice)
	}
}
