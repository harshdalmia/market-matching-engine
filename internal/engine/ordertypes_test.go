package engine

import (
	"testing"

	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
)

// typed builds an order with an explicit type and time-in-force.
func typed(id, side, orderType, tif string, price float64, qty int) *models.Order {
	o := order(id, "t", side, price, qty)
	o.Type = orderType
	o.TimeInForce = tif
	return o
}

// -----------------------------------------------------------------------
// Defaults and backward compatibility
// -----------------------------------------------------------------------

// Orders submitted without a type or time-in-force must behave exactly as they
// did before those fields existed: a resting GTC limit order.
func TestUnspecifiedTypeDefaultsToGTCLimit(t *testing.T) {
	e, ob := newTestEngine()

	o := order("b1", "alice", models.SideBuy, 100.00, 5)
	o.Type = ""
	o.TimeInForce = ""
	e.processOrder(o)

	if o.Type != models.TypeLimit {
		t.Errorf("type: want LIMIT, got %q", o.Type)
	}
	if o.TimeInForce != models.TIFGTC {
		t.Errorf("time in force: want GTC, got %q", o.TimeInForce)
	}
	if restingAt(t, ob, models.SideBuy, 100.00) != 5 {
		t.Errorf("an unspecified order should rest like a GTC limit")
	}
}

// -----------------------------------------------------------------------
// MARKET orders
// -----------------------------------------------------------------------

func TestMarketOrderIgnoresPriceAndSweepsBook(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 3))
	e.processOrder(typed("a2", models.SideSell, models.TypeLimit, models.TIFGTC, 105.00, 3))
	e.processOrder(typed("a3", models.SideSell, models.TypeLimit, models.TIFGTC, 110.00, 3))

	// A market buy for 7 must take 100, 105 and part of 110 without any limit.
	mkt := typed("m1", models.SideBuy, models.TypeMarket, models.TIFIOC, 0, 7)
	e.processOrder(mkt)

	trades := e.GetTrades(0)
	if len(trades) != 3 {
		t.Fatalf("want 3 fills across 3 levels, got %d", len(trades))
	}
	if mkt.Remaining != 0 || mkt.Status != models.StatusFilled {
		t.Errorf("market order should be fully filled, got remaining %d status %s",
			mkt.Remaining, mkt.Status)
	}
	// 110.00 had 3, of which 1 was taken.
	if got := restingAt(t, ob, models.SideSell, 110.00); got != 2 {
		t.Errorf("deepest level remainder: want 2, got %d", got)
	}
}

// A market order has no price, so it cannot rest. Any unfillable remainder is
// cancelled rather than joining the book at a meaningless price.
func TestMarketOrderNeverRests(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 2))

	mkt := typed("m1", models.SideBuy, models.TypeMarket, models.TIFGTC, 0, 10)
	e.processOrder(mkt)

	if mkt.Remaining != 8 {
		t.Fatalf("want 8 unfilled, got %d", mkt.Remaining)
	}
	if mkt.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", mkt.Status)
	}
	// Even with GTC, it must not appear in the book.
	snap := ob.Snapshot()
	for _, o := range append(append([]*models.Order{}, snap.Bids...), snap.Asks...) {
		if o.ID == "m1" {
			t.Errorf("market order must never rest in the book")
		}
	}
}

func TestMarketOrderOnEmptyBookIsCancelled(t *testing.T) {
	e, ob := newTestEngine()

	mkt := typed("m1", models.SideBuy, models.TypeMarket, models.TIFIOC, 0, 5)
	e.processOrder(mkt)

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("no liquidity means no trades")
	}
	if mkt.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", mkt.Status)
	}
	if ob.Len() != 0 {
		t.Errorf("book should stay empty")
	}
}

// A market order does not need a price, so the price check must not reject it.
func TestMarketOrderWithZeroPriceIsNotRejected(t *testing.T) {
	e, _ := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 5))

	mkt := typed("m1", models.SideBuy, models.TypeMarket, models.TIFIOC, 0, 5)
	e.processOrder(mkt)

	if mkt.Status == models.StatusRejected {
		t.Errorf("a market order with no price must not be rejected")
	}
	if mkt.Status != models.StatusFilled {
		t.Errorf("status: want FILLED, got %s", mkt.Status)
	}
}

// -----------------------------------------------------------------------
// IOC
// -----------------------------------------------------------------------

func TestIOCFillsWhatItCanAndCancelsTheRest(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 4))

	ioc := typed("i1", models.SideBuy, models.TypeLimit, models.TIFIOC, 100.00, 10)
	e.processOrder(ioc)

	if ioc.Remaining != 6 {
		t.Fatalf("want 6 unfilled, got %d", ioc.Remaining)
	}
	if ioc.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", ioc.Status)
	}
	if got := restingAt(t, ob, models.SideBuy, 100.00); got != 0 {
		t.Errorf("IOC remainder must not rest, %d found in the book", got)
	}

	// The filled portion is still recoverable from the order.
	if filled := ioc.Quantity - ioc.Remaining; filled != 4 {
		t.Errorf("filled quantity: want 4, got %d", filled)
	}
}

func TestIOCFullyFilledIsMarkedFilled(t *testing.T) {
	e, _ := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 10))

	ioc := typed("i1", models.SideBuy, models.TypeLimit, models.TIFIOC, 100.00, 10)
	e.processOrder(ioc)

	if ioc.Status != models.StatusFilled {
		t.Errorf("status: want FILLED, got %s", ioc.Status)
	}
}

func TestIOCRespectsItsLimitPrice(t *testing.T) {
	e, _ := newTestEngine()

	// Only liquidity is above the IOC's limit, so nothing should trade.
	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 105.00, 10))

	ioc := typed("i1", models.SideBuy, models.TypeLimit, models.TIFIOC, 100.00, 10)
	e.processOrder(ioc)

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("IOC must not trade through its limit price")
	}
	if ioc.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", ioc.Status)
	}
}

// -----------------------------------------------------------------------
// FOK
// -----------------------------------------------------------------------

// The defining property: if the book cannot fill the whole quantity, nothing
// trades at all. A partial fill would be a correctness failure, not a nuance.
func TestFOKKilledLeavesNoFills(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 4))

	fok := typed("f1", models.SideBuy, models.TypeLimit, models.TIFFOK, 100.00, 10)
	e.processOrder(fok)

	if len(e.GetTrades(0)) != 0 {
		t.Fatalf("an unfillable FOK must produce zero trades, got %d", len(e.GetTrades(0)))
	}
	if fok.Remaining != 10 {
		t.Errorf("quantity must be untouched, remaining %d", fok.Remaining)
	}
	if fok.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", fok.Status)
	}
	// The resting liquidity it declined must be completely undisturbed.
	if got := restingAt(t, ob, models.SideSell, 100.00); got != 4 {
		t.Errorf("resting ask should be untouched at 4, got %d", got)
	}
}

func TestFOKFillsCompletelyWhenLiquidityIsSufficient(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 6))
	e.processOrder(typed("a2", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 6))

	fok := typed("f1", models.SideBuy, models.TypeLimit, models.TIFFOK, 100.00, 10)
	e.processOrder(fok)

	if fok.Remaining != 0 || fok.Status != models.StatusFilled {
		t.Errorf("FOK should fill completely, got remaining %d status %s",
			fok.Remaining, fok.Status)
	}
	if got := restingAt(t, ob, models.SideSell, 100.00); got != 2 {
		t.Errorf("want 2 left resting, got %d", got)
	}
}

// Liquidity above the limit must not count toward the fill-or-kill check.
func TestFOKIgnoresLiquidityBeyondItsLimit(t *testing.T) {
	e, _ := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 4))
	e.processOrder(typed("a2", models.SideSell, models.TypeLimit, models.TIFGTC, 106.00, 50))

	// 4 available at or below 100; the 50 at 106 is out of reach.
	fok := typed("f1", models.SideBuy, models.TypeLimit, models.TIFFOK, 100.00, 10)
	e.processOrder(fok)

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("out-of-limit liquidity must not satisfy a FOK")
	}
}

func TestFOKMarketOrderUsesAllLiquidity(t *testing.T) {
	e, _ := newTestEngine()

	// A market FOK has no limit, so every level counts toward availability.
	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 4))
	e.processOrder(typed("a2", models.SideSell, models.TypeLimit, models.TIFGTC, 200.00, 6))

	fok := typed("f1", models.SideBuy, models.TypeMarket, models.TIFFOK, 0, 10)
	e.processOrder(fok)

	if fok.Status != models.StatusFilled {
		t.Errorf("market FOK should fill across all levels, got %s", fok.Status)
	}
	if len(e.GetTrades(0)) != 2 {
		t.Errorf("want 2 fills, got %d", len(e.GetTrades(0)))
	}
}

func TestFOKSellSide(t *testing.T) {
	e, _ := newTestEngine()

	e.processOrder(typed("b1", models.SideBuy, models.TypeLimit, models.TIFGTC, 100.00, 3))

	// Not enough bid liquidity: must kill.
	fok := typed("f1", models.SideSell, models.TypeLimit, models.TIFFOK, 100.00, 10)
	e.processOrder(fok)

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("unfillable sell-side FOK must not trade")
	}
	if fok.Status != models.StatusCancelled {
		t.Errorf("status: want CANCELLED, got %s", fok.Status)
	}
}

// A cancelled resting order must not count as available liquidity for a FOK.
func TestFOKIgnoresCancelledLiquidity(t *testing.T) {
	e, ob := newTestEngine()

	e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 10))
	if !ob.CancelOrder("a1") {
		t.Fatalf("setup: cancel should succeed")
	}

	fok := typed("f1", models.SideBuy, models.TypeLimit, models.TIFFOK, 100.00, 10)
	e.processOrder(fok)

	if len(e.GetTrades(0)) != 0 {
		t.Errorf("cancelled orders must not satisfy a FOK")
	}
}

// -----------------------------------------------------------------------
// Invariants across all combinations
// -----------------------------------------------------------------------

// Whatever the type and time-in-force, an order must never end up resting when
// it is not allowed to, and never be left in a non-terminal state otherwise.
func TestRestingIsAllowedOnlyForGTCLimits(t *testing.T) {
	combos := []struct {
		orderType string
		tif       string
		canRest   bool
	}{
		{models.TypeLimit, models.TIFGTC, true},
		{models.TypeLimit, models.TIFIOC, false},
		{models.TypeLimit, models.TIFFOK, false},
		{models.TypeMarket, models.TIFGTC, false},
		{models.TypeMarket, models.TIFIOC, false},
		{models.TypeMarket, models.TIFFOK, false},
	}

	for _, c := range combos {
		name := c.orderType + "/" + c.tif
		t.Run(name, func(t *testing.T) {
			ob := orderbook.New()
			e := New(ob)

			// One ask for 2, so a quantity of 5 can only ever partially fill.
			e.processOrder(typed("a1", models.SideSell, models.TypeLimit, models.TIFGTC, 100.00, 2))

			price := 100.00
			if c.orderType == models.TypeMarket {
				price = 0
			}
			o := typed("x1", models.SideBuy, c.orderType, c.tif, price, 5)
			e.processOrder(o)

			resting := restingAt(t, ob, models.SideBuy, 100.00)
			if c.canRest && resting == 0 {
				t.Errorf("%s should rest its remainder", name)
			}
			if !c.canRest && resting != 0 {
				t.Errorf("%s must not rest, found %d in the book", name, resting)
			}
		})
	}
}
