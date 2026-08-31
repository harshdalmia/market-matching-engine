package accounts

import (
	"fmt"
	"math"
	"testing"

	"matching-engine/internal/models"
)

const startCash = 100_000.0

func newRegistry() *Registry {
	return New(startCash, 0, 0)
}

// execute books a fill between two traders at the given price and quantity.
func execute(r *Registry, symbol, buyer, seller string, price float64, qty int) {
	r.OnFill(
		models.Trade{Symbol: symbol, Price: price, Quantity: qty},
		models.Order{TraderID: buyer, Symbol: symbol, Side: models.SideBuy},
		models.Order{TraderID: seller, Symbol: symbol, Side: models.SideSell},
	)
}

func positionOf(t *testing.T, r *Registry, trader, symbol string) Position {
	t.Helper()

	for _, p := range r.Snapshot(trader, nil).Positions {
		if p.Symbol == symbol {
			return p
		}
	}
	t.Fatalf("no position in %s for %s", symbol, trader)
	return Position{}
}

func closeTo(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

// -----------------------------------------------------------------------
// Opening and averaging
// -----------------------------------------------------------------------

func TestBuyOpensLongAndDebitsCash(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)

	snapshot := r.Snapshot("alice", nil)
	if snapshot.Cash != startCash-1000 {
		t.Errorf("cash: want %.2f, got %.2f", startCash-1000, snapshot.Cash)
	}

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != 10 {
		t.Errorf("quantity: want 10, got %d", position.Quantity)
	}
	if !closeTo(position.AvgEntry, 100) {
		t.Errorf("avg entry: want 100, got %.4f", position.AvgEntry)
	}
}

func TestSellOpensShortAndCreditsCash(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "bob", "alice", 100.00, 10)

	snapshot := r.Snapshot("alice", nil)
	if snapshot.Cash != startCash+1000 {
		t.Errorf("cash: want %.2f, got %.2f", startCash+1000, snapshot.Cash)
	}

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != -10 {
		t.Errorf("quantity: want -10 (short), got %d", position.Quantity)
	}
	if !closeTo(position.AvgEntry, 100) {
		t.Errorf("avg entry: want 100, got %.4f", position.AvgEntry)
	}
}

// Adding to a position blends the entry price weighted by quantity.
func TestAddingToLongAveragesEntry(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)
	execute(r, "AAA", "alice", "bob", 110.00, 10)

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != 20 {
		t.Errorf("quantity: want 20, got %d", position.Quantity)
	}
	if !closeTo(position.AvgEntry, 105) {
		t.Errorf("avg entry: want 105, got %.4f", position.AvgEntry)
	}
}

func TestAddingToShortAveragesEntry(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "bob", "alice", 100.00, 10)
	execute(r, "AAA", "bob", "alice", 120.00, 10)

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != -20 {
		t.Errorf("quantity: want -20, got %d", position.Quantity)
	}
	if !closeTo(position.AvgEntry, 110) {
		t.Errorf("avg entry: want 110, got %.4f", position.AvgEntry)
	}
}

// -----------------------------------------------------------------------
// Realised profit
// -----------------------------------------------------------------------

func TestReducingLongRealisesProfit(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 20) // long 20 @100
	execute(r, "AAA", "bob", "alice", 120.00, 10) // sell 10 @120

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != 10 {
		t.Errorf("quantity: want 10, got %d", position.Quantity)
	}
	// (120 - 100) * 10
	if !closeTo(position.RealizedPnL, 200) {
		t.Errorf("realised: want 200, got %.4f", position.RealizedPnL)
	}
	// A reduction must not move the average entry of what remains.
	if !closeTo(position.AvgEntry, 100) {
		t.Errorf("avg entry should be unchanged at 100, got %.4f", position.AvgEntry)
	}
}

func TestReducingShortRealisesProfit(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "bob", "alice", 100.00, 20) // short 20 @100
	execute(r, "AAA", "alice", "bob", 90.00, 10)  // buy back 10 @90

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != -10 {
		t.Errorf("quantity: want -10, got %d", position.Quantity)
	}
	// Short profits as price falls: (100 - 90) * 10
	if !closeTo(position.RealizedPnL, 100) {
		t.Errorf("realised: want 100, got %.4f", position.RealizedPnL)
	}
}

func TestClosingFlatResetsEntry(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)
	execute(r, "AAA", "bob", "alice", 105.00, 10)

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != 0 {
		t.Errorf("quantity: want 0, got %d", position.Quantity)
	}
	if position.AvgEntry != 0 {
		t.Errorf("a flat position should have no entry price, got %.4f", position.AvgEntry)
	}
	if !closeTo(position.RealizedPnL, 50) {
		t.Errorf("realised: want 50, got %.4f", position.RealizedPnL)
	}
}

// Selling more than the long realises on the overlap only; the excess opens a
// short at the fill price.
func TestFlippingLongToShort(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10) // long 10 @100
	execute(r, "AAA", "bob", "alice", 110.00, 25) // sell 25 @110

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != -15 {
		t.Errorf("quantity: want -15, got %d", position.Quantity)
	}
	// Only the 10 that closed the long realises: (110 - 100) * 10
	if !closeTo(position.RealizedPnL, 100) {
		t.Errorf("realised: want 100, got %.4f", position.RealizedPnL)
	}
	// The new short was taken on at the fill price.
	if !closeTo(position.AvgEntry, 110) {
		t.Errorf("avg entry: want 110, got %.4f", position.AvgEntry)
	}
}

func TestFlippingShortToLong(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "bob", "alice", 100.00, 10) // short 10 @100
	execute(r, "AAA", "alice", "bob", 90.00, 30)  // buy 30 @90

	position := positionOf(t, r, "alice", "AAA")
	if position.Quantity != 20 {
		t.Errorf("quantity: want 20, got %d", position.Quantity)
	}
	if !closeTo(position.RealizedPnL, 100) {
		t.Errorf("realised: want 100, got %.4f", position.RealizedPnL)
	}
	if !closeTo(position.AvgEntry, 90) {
		t.Errorf("avg entry: want 90, got %.4f", position.AvgEntry)
	}
}

// -----------------------------------------------------------------------
// Cash and equity
// -----------------------------------------------------------------------

// A round trip at the same price must leave cash exactly where it started.
func TestRoundTripAtSamePriceLeavesCashUnchanged(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)
	execute(r, "AAA", "bob", "alice", 100.00, 10)

	snapshot := r.Snapshot("alice", nil)
	if !closeTo(snapshot.Cash, startCash) {
		t.Errorf("cash: want %.2f, got %.2f", startCash, snapshot.Cash)
	}
	if !closeTo(snapshot.Equity, startCash) {
		t.Errorf("equity: want %.2f, got %.2f", startCash, snapshot.Equity)
	}
}

// Both sides of a fill are booked, and what one pays the other receives.
func TestBothCounterpartiesAreBooked(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)

	alice := r.Snapshot("alice", nil)
	bob := r.Snapshot("bob", nil)

	if !closeTo(alice.Cash, startCash-1000) {
		t.Errorf("buyer cash: want %.2f, got %.2f", startCash-1000, alice.Cash)
	}
	if !closeTo(bob.Cash, startCash+1000) {
		t.Errorf("seller cash: want %.2f, got %.2f", startCash+1000, bob.Cash)
	}
	// Cash is conserved across the pair.
	if !closeTo(alice.Cash+bob.Cash, 2*startCash) {
		t.Errorf("cash not conserved: %.2f", alice.Cash+bob.Cash)
	}
}

func TestUnrealisedProfitUsesMark(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)

	snapshot := r.Snapshot("alice", map[string]float64{"AAA": 130.00})

	if !closeTo(snapshot.UnrealizedPnL, 300) {
		t.Errorf("unrealised: want 300, got %.4f", snapshot.UnrealizedPnL)
	}
	if !closeTo(snapshot.PositionValue, 1300) {
		t.Errorf("position value: want 1300, got %.4f", snapshot.PositionValue)
	}
	// Equity is cash plus what the holding is worth.
	if !closeTo(snapshot.Equity, startCash-1000+1300) {
		t.Errorf("equity: want %.2f, got %.2f", startCash-1000+1300, snapshot.Equity)
	}
	if !closeTo(snapshot.TotalPnL, 300) {
		t.Errorf("total pnl: want 300, got %.4f", snapshot.TotalPnL)
	}
}

func TestShortUnrealisedProfitWhenPriceFalls(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "bob", "alice", 100.00, 10) // alice short 10 @100

	snapshot := r.Snapshot("alice", map[string]float64{"AAA": 80.00})

	// Short gains as the mark falls: (80 - 100) * -10
	if !closeTo(snapshot.UnrealizedPnL, 200) {
		t.Errorf("unrealised: want 200, got %.4f", snapshot.UnrealizedPnL)
	}
}

// Without a mark there is nothing to value against, so unrealised profit must be
// zero rather than an invented number.
func TestMissingMarkYieldsZeroUnrealised(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 10)

	snapshot := r.Snapshot("alice", map[string]float64{})
	if !closeTo(snapshot.UnrealizedPnL, 0) {
		t.Errorf("unrealised: want 0, got %.4f", snapshot.UnrealizedPnL)
	}
}

func TestUnknownTraderGetsOpeningBalance(t *testing.T) {
	r := newRegistry()

	snapshot := r.Snapshot("nobody", nil)
	if snapshot.Cash != startCash || snapshot.Equity != startCash {
		t.Errorf("an untouched trader should hold the opening balance, got cash %.2f equity %.2f",
			snapshot.Cash, snapshot.Equity)
	}
	if len(snapshot.Positions) != 0 {
		t.Errorf("want no positions, got %d", len(snapshot.Positions))
	}
}

// -----------------------------------------------------------------------
// Order tracking
// -----------------------------------------------------------------------

func liveOrder(id, trader, symbol, status string) models.Order {
	return models.Order{
		ID: id, TraderID: trader, Symbol: symbol,
		Side: models.SideBuy, Type: models.TypeLimit, TimeInForce: models.TIFGTC,
		Price: 100, Quantity: 5, Remaining: 5, Status: status, Timestamp: 1,
	}
}

func TestOpenOrdersAreTracked(t *testing.T) {
	r := newRegistry()
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusNew))

	orders := r.Orders("alice", "")
	if len(orders.Open) != 1 || orders.Open[0].ID != "o1" {
		t.Errorf("want o1 open, got %+v", orders.Open)
	}
	if len(orders.History) != 0 {
		t.Errorf("want empty history, got %d", len(orders.History))
	}
}

func TestTerminalOrdersMoveToHistory(t *testing.T) {
	r := newRegistry()
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusNew))
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusFilled))

	orders := r.Orders("alice", "")
	if len(orders.Open) != 0 {
		t.Errorf("a filled order should leave the working set, got %d", len(orders.Open))
	}
	if len(orders.History) != 1 || orders.History[0].Status != models.StatusFilled {
		t.Errorf("want one FILLED entry in history, got %+v", orders.History)
	}
}

// A fully filled order can be reported once per fill, so history must not
// accumulate duplicates of the same order.
func TestHistoryDoesNotDuplicateAnOrder(t *testing.T) {
	r := newRegistry()
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusFilled))
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusFilled))

	orders := r.Orders("alice", "")
	if len(orders.History) != 1 {
		t.Errorf("want 1 history entry, got %d", len(orders.History))
	}
}

func TestHistoryIsNewestFirst(t *testing.T) {
	r := newRegistry()
	for i := 1; i <= 3; i++ {
		o := liveOrder(fmt.Sprintf("o%d", i), "alice", "AAA", models.StatusFilled)
		o.Timestamp = int64(i)
		r.OnOrderState(o)
	}

	history := r.Orders("alice", "").History
	if len(history) != 3 {
		t.Fatalf("want 3 entries, got %d", len(history))
	}
	if history[0].ID != "o3" || history[2].ID != "o1" {
		t.Errorf("history should be newest first, got %s..%s", history[0].ID, history[2].ID)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	const cap = 5
	r := New(startCash, cap, 0)

	for i := 0; i < cap*3; i++ {
		r.OnOrderState(liveOrder(fmt.Sprintf("o%d", i), "alice", "AAA", models.StatusFilled))
	}

	history := r.Orders("alice", "").History
	if len(history) != cap {
		t.Errorf("want the cap of %d entries, got %d", cap, len(history))
	}
	// The most recent survive, the oldest are dropped.
	if history[0].ID != fmt.Sprintf("o%d", cap*3-1) {
		t.Errorf("newest entry should be retained, got %s", history[0].ID)
	}
}

func TestOrdersFilterBySymbol(t *testing.T) {
	r := newRegistry()
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusNew))
	r.OnOrderState(liveOrder("o2", "alice", "BBB", models.StatusNew))

	if got := len(r.Orders("alice", "AAA").Open); got != 1 {
		t.Errorf("symbol filter: want 1 open order, got %d", got)
	}
	if got := len(r.Orders("alice", "").Open); got != 2 {
		t.Errorf("no filter: want 2 open orders, got %d", got)
	}
}

func TestOpenOrderCountAppearsInSnapshot(t *testing.T) {
	r := newRegistry()
	r.OnOrderState(liveOrder("o1", "alice", "AAA", models.StatusNew))
	r.OnOrderState(liveOrder("o2", "alice", "AAA", models.StatusNew))

	if got := r.Snapshot("alice", nil).OpenOrders; got != 2 {
		t.Errorf("want 2 open orders, got %d", got)
	}
}

// -----------------------------------------------------------------------
// Guards
// -----------------------------------------------------------------------

func TestEmptyTraderIDIsIgnored(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "", "", 100.00, 10)
	r.OnOrderState(liveOrder("o1", "", "AAA", models.StatusNew))

	if r.TraderCount() != 0 {
		t.Errorf("an order with no trader should not create an account, got %d", r.TraderCount())
	}
}

// The API is unauthenticated, so the account map is keyed by client-supplied
// text. It has to be bounded or it is a memory-growth vector.
func TestAccountCountIsCapped(t *testing.T) {
	const cap = 3
	r := New(startCash, 0, cap)

	for i := 0; i < cap*4; i++ {
		execute(r, "AAA", fmt.Sprintf("trader-%d", i), "counterparty", 100.00, 1)
	}

	if r.TraderCount() > cap {
		t.Errorf("tracked accounts should not exceed the cap of %d, got %d", cap, r.TraderCount())
	}
}

func TestZeroQuantityFillIsIgnored(t *testing.T) {
	r := newRegistry()
	execute(r, "AAA", "alice", "bob", 100.00, 0)

	if !closeTo(r.Snapshot("alice", nil).Cash, startCash) {
		t.Errorf("a zero-quantity fill must not move cash")
	}
}

func TestDefaultsApplyForNonPositiveConfig(t *testing.T) {
	r := New(0, 0, 0)

	if r.StartingCash() != DefaultStartingCash {
		t.Errorf("want the default starting cash, got %.2f", r.StartingCash())
	}
}
