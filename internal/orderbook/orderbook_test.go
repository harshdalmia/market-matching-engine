package orderbook

import (
	"fmt"
	"math/rand"
	"testing"

	"matching-engine/internal/models"
)

var seq int64

func mk(id, side string, price float64, qty int) *models.Order {
	seq++
	return &models.Order{
		ID:        id,
		TraderID:  "t",
		Side:      side,
		Price:     price,
		Quantity:  qty,
		Remaining: qty,
		Timestamp: seq,
		Status:    models.StatusNew,
	}
}

// -----------------------------------------------------------------------
// Price-time priority
// -----------------------------------------------------------------------

func TestBestBidIsHighestPrice(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 99.00, 1))
	ob.AddOrder(mk("b2", models.SideBuy, 101.00, 1))
	ob.AddOrder(mk("b3", models.SideBuy, 100.00, 1))

	best := ob.BestBid()
	if best == nil || best.ID != "b2" {
		t.Fatalf("best bid should be the highest price (b2 @101), got %v", best)
	}
}

func TestBestAskIsLowestPrice(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("a1", models.SideSell, 101.00, 1))
	ob.AddOrder(mk("a2", models.SideSell, 99.00, 1))
	ob.AddOrder(mk("a3", models.SideSell, 100.00, 1))

	best := ob.BestAsk()
	if best == nil || best.ID != "a2" {
		t.Fatalf("best ask should be the lowest price (a2 @99), got %v", best)
	}
}

func TestFIFOWithinSamePrice(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("first", models.SideBuy, 100.00, 1))
	ob.AddOrder(mk("second", models.SideBuy, 100.00, 1))
	ob.AddOrder(mk("third", models.SideBuy, 100.00, 1))

	for _, want := range []string{"first", "second", "third"} {
		got := ob.PopBestBid()
		if got == nil || got.ID != want {
			t.Fatalf("FIFO order broken: want %s, got %v", want, got)
		}
	}
}

// -----------------------------------------------------------------------
// Snapshot ordering
//
// The heap's backing slice is only partially ordered — only index 0 is
// guaranteed to be the best price — so Snapshot has to sort explicitly. This is
// the bug the old struct comment ("sorted: highest price first") papered over.
// -----------------------------------------------------------------------

func TestSnapshotIsFullySorted(t *testing.T) {
	ob := New()

	// Enough orders in a deliberately awkward order that an unsorted heap slice
	// would almost certainly come out wrong.
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 60; i++ {
		price := 90.0 + float64(rng.Intn(2001))/100.0
		ob.AddOrder(mk(fmt.Sprintf("b%d", i), models.SideBuy, price, 1))
		ob.AddOrder(mk(fmt.Sprintf("a%d", i), models.SideSell, price, 1))
	}

	snap := ob.Snapshot()

	for i := 1; i < len(snap.Bids); i++ {
		if snap.Bids[i-1].Price < snap.Bids[i].Price {
			t.Fatalf("bids not descending at %d: %.2f then %.2f",
				i, snap.Bids[i-1].Price, snap.Bids[i].Price)
		}
	}
	for i := 1; i < len(snap.Asks); i++ {
		if snap.Asks[i-1].Price > snap.Asks[i].Price {
			t.Fatalf("asks not ascending at %d: %.2f then %.2f",
				i, snap.Asks[i-1].Price, snap.Asks[i].Price)
		}
	}
}

func TestSnapshotSortsTiesByTimestamp(t *testing.T) {
	ob := New()
	early := mk("early", models.SideBuy, 100.00, 1)
	late := mk("late", models.SideBuy, 100.00, 1)

	// Insert out of time order to make sure the sort, not the insert, decides.
	ob.AddOrder(late)
	ob.AddOrder(early)

	snap := ob.Snapshot()
	if len(snap.Bids) != 2 {
		t.Fatalf("want 2 bids, got %d", len(snap.Bids))
	}
	if snap.Bids[0].Timestamp > snap.Bids[1].Timestamp {
		t.Errorf("equal prices must be ordered earliest timestamp first")
	}
}

func TestSnapshotExcludesCancelledAndFilled(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("live", models.SideBuy, 100.00, 5))

	cancelled := mk("cancelled", models.SideBuy, 100.00, 5)
	ob.AddOrder(cancelled)
	ob.CancelOrder("cancelled")

	filled := mk("filled", models.SideBuy, 100.00, 5)
	filled.Remaining = 0
	filled.Status = models.StatusFilled
	ob.AddOrder(filled)

	snap := ob.Snapshot()
	if len(snap.Bids) != 1 || snap.Bids[0].ID != "live" {
		t.Errorf("snapshot should contain only the live order, got %d entries", len(snap.Bids))
	}
}

// The same *Order pointer can legitimately be pushed onto a heap more than
// once; the snapshot must not report it twice or depth double-counts.
func TestSnapshotDeduplicatesByID(t *testing.T) {
	ob := New()
	dup := mk("dup", models.SideBuy, 100.00, 5)
	ob.AddOrder(dup)
	ob.AddOrder(dup)

	snap := ob.Snapshot()
	if len(snap.Bids) != 1 {
		t.Errorf("duplicate order IDs must be collapsed, got %d entries", len(snap.Bids))
	}
}

// -----------------------------------------------------------------------
// Depth aggregation
// -----------------------------------------------------------------------

func TestDepthAggregatesByPrice(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 100.00, 5))
	ob.AddOrder(mk("b2", models.SideBuy, 100.00, 7))
	ob.AddOrder(mk("b3", models.SideBuy, 99.00, 3))

	depth := ob.Depth(0)
	if len(depth.Bids) != 2 {
		t.Fatalf("want 2 price levels, got %d", len(depth.Bids))
	}

	top := depth.Bids[0]
	if top.Price != 100.00 || top.Quantity != 12 || top.OrderCount != 2 {
		t.Errorf("top level: want 100.00 qty 12 count 2, got %.2f qty %d count %d",
			top.Price, top.Quantity, top.OrderCount)
	}
	if top.Cumulative != 12 {
		t.Errorf("first level cumulative: want 12, got %d", top.Cumulative)
	}
	if depth.Bids[1].Cumulative != 15 {
		t.Errorf("second level cumulative: want 15, got %d", depth.Bids[1].Cumulative)
	}
}

func TestDepthQuoteFields(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 99.00, 5))
	ob.AddOrder(mk("a1", models.SideSell, 101.00, 5))

	depth := ob.Depth(0)
	if depth.BestBid == nil || *depth.BestBid != 99.00 {
		t.Errorf("best bid: want 99.00, got %v", depth.BestBid)
	}
	if depth.BestAsk == nil || *depth.BestAsk != 101.00 {
		t.Errorf("best ask: want 101.00, got %v", depth.BestAsk)
	}
	if depth.Spread == nil || *depth.Spread != 2.00 {
		t.Errorf("spread: want 2.00, got %v", depth.Spread)
	}
	if depth.Mid == nil || *depth.Mid != 100.00 {
		t.Errorf("mid: want 100.00, got %v", depth.Mid)
	}
}

func TestDepthQuoteFieldsNilOnOneSidedBook(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 99.00, 5))

	depth := ob.Depth(0)
	if depth.BestBid == nil {
		t.Errorf("best bid should be present")
	}
	if depth.BestAsk != nil || depth.Spread != nil || depth.Mid != nil {
		t.Errorf("ask-derived fields must be nil when there are no asks")
	}
}

func TestDepthRespectsLevelCap(t *testing.T) {
	ob := New()
	for i := 0; i < 20; i++ {
		ob.AddOrder(mk(fmt.Sprintf("b%d", i), models.SideBuy, 100.00-float64(i), 1))
	}

	depth := ob.Depth(5)
	if len(depth.Bids) != 5 {
		t.Errorf("want 5 levels, got %d", len(depth.Bids))
	}
	// The cap must keep the levels nearest the touch, not an arbitrary slice.
	if depth.Bids[0].Price != 100.00 {
		t.Errorf("capped depth must start at the best price, got %.2f", depth.Bids[0].Price)
	}
}

func TestEmptyBookSnapshotsAreEmptyNotNil(t *testing.T) {
	ob := New()

	snap := ob.Snapshot()
	if snap.Bids == nil || snap.Asks == nil {
		t.Errorf("empty sides must serialise as [] not null")
	}

	depth := ob.Depth(0)
	if depth.Bids == nil || depth.Asks == nil {
		t.Errorf("empty depth sides must serialise as [] not null")
	}
}

// -----------------------------------------------------------------------
// Cancellation
// -----------------------------------------------------------------------

func TestCancelOrder(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 100.00, 5))

	if !ob.CancelOrder("b1") {
		t.Errorf("cancelling a resting order should succeed")
	}
	if ob.CancelOrder("missing") {
		t.Errorf("cancelling an unknown order should fail")
	}
}

func TestPoppedOrderIsNoLongerCancellable(t *testing.T) {
	ob := New()
	ob.AddOrder(mk("b1", models.SideBuy, 100.00, 5))
	ob.PopBestBid()

	if ob.CancelOrder("b1") {
		t.Errorf("an order removed from the book should not be cancellable")
	}
}

func TestLenTracksRestingOrders(t *testing.T) {
	ob := New()
	if ob.Len() != 0 {
		t.Errorf("new book should be empty")
	}

	ob.AddOrder(mk("b1", models.SideBuy, 100.00, 1))
	ob.AddOrder(mk("a1", models.SideSell, 101.00, 1))
	if ob.Len() != 2 {
		t.Errorf("want 2 resting orders, got %d", ob.Len())
	}

	ob.PopBestBid()
	if ob.Len() != 1 {
		t.Errorf("want 1 resting order after pop, got %d", ob.Len())
	}
}

func TestPopOnEmptyBookReturnsNil(t *testing.T) {
	ob := New()
	if ob.PopBestBid() != nil || ob.PopBestAsk() != nil {
		t.Errorf("popping an empty book must return nil, not panic")
	}
	if ob.BestBid() != nil || ob.BestAsk() != nil {
		t.Errorf("peeking an empty book must return nil")
	}
}

// -----------------------------------------------------------------------
// Property test
// -----------------------------------------------------------------------

// Repeatedly popping must yield monotonically worsening prices on both sides.
// This is the heap invariant that price priority depends on.
func TestPropertyPopOrderIsMonotonic(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		ob := New()
		rng := rand.New(rand.NewSource(seed))

		for i := 0; i < 200; i++ {
			price := 90.0 + float64(rng.Intn(2001))/100.0
			ob.AddOrder(mk(fmt.Sprintf("b%d-%d", seed, i), models.SideBuy, price, 1))
			ob.AddOrder(mk(fmt.Sprintf("a%d-%d", seed, i), models.SideSell, price, 1))
		}

		prev := 1e9
		for o := ob.PopBestBid(); o != nil; o = ob.PopBestBid() {
			if o.Price > prev {
				t.Fatalf("seed %d: bids popped out of order, %.2f after %.2f", seed, o.Price, prev)
			}
			prev = o.Price
		}

		prev = -1
		for o := ob.PopBestAsk(); o != nil; o = ob.PopBestAsk() {
			if o.Price < prev {
				t.Fatalf("seed %d: asks popped out of order, %.2f after %.2f", seed, o.Price, prev)
			}
			prev = o.Price
		}
	}
}
