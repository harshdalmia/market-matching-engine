package orderbook

import (
	"container/heap"
	"matching-engine/internal/models"
	"sort"
	"sync"
)

// -----------------------------------------------------------------------
// BidHeap — Max-heap (highest price = best bid)
// -----------------------------------------------------------------------

type BidHeap []*models.Order

func (h BidHeap) Len() int { return len(h) }

// Higher price wins; same price → earlier timestamp wins (FIFO)
func (h BidHeap) Less(i, j int) bool {
	if h[i].Price != h[j].Price {
		return h[i].Price > h[j].Price
	}
	return h[i].Timestamp < h[j].Timestamp
}

func (h BidHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *BidHeap) Push(x interface{}) {
	*h = append(*h, x.(*models.Order))
}

func (h *BidHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// -----------------------------------------------------------------------
// AskHeap — Min-heap (lowest price = best ask)
// -----------------------------------------------------------------------

type AskHeap []*models.Order

func (h AskHeap) Len() int { return len(h) }

// Lower price wins; same price → earlier timestamp wins (FIFO)
func (h AskHeap) Less(i, j int) bool {
	if h[i].Price != h[j].Price {
		return h[i].Price < h[j].Price
	}
	return h[i].Timestamp < h[j].Timestamp
}

func (h AskHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *AskHeap) Push(x interface{}) {
	*h = append(*h, x.(*models.Order))
}

func (h *AskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// -----------------------------------------------------------------------
// OrderBook
// -----------------------------------------------------------------------

type OrderBook struct {
	mu     sync.RWMutex
	bids   *BidHeap                 // buy orders
	asks   *AskHeap                 // sell orders
	orders map[string]*models.Order // fast lookup by ID

	// version increments on every structural change. The event stream reads it
	// to decide whether a depth snapshot is worth re-serialising, which keeps
	// conflated market data off the matching path.
	version uint64
}

// Version returns a counter that changes whenever the book is structurally
// modified. It does not change on partial fills, which mutate an order in
// place — callers that need to observe those should also watch the engine's
// processed-order count.
func (ob *OrderBook) Version() uint64 {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	return ob.version
}

func New() *OrderBook {
	bids := &BidHeap{}
	asks := &AskHeap{}
	heap.Init(bids)
	heap.Init(asks)
	return &OrderBook{
		bids:   bids,
		asks:   asks,
		orders: make(map[string]*models.Order),
	}
}

// AddOrder inserts an order into the correct heap
func (ob *OrderBook) AddOrder(order *models.Order) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	ob.version++
	ob.orders[order.ID] = order
	if order.Side == models.SideBuy {
		heap.Push(ob.bids, order)
	} else {
		heap.Push(ob.asks, order)
	}
}

// BestBid returns the highest buy order without removing it
func (ob *OrderBook) BestBid() *models.Order {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	if ob.bids.Len() == 0 {
		return nil
	}
	return (*ob.bids)[0]
}

// BestAsk returns the lowest sell order without removing it
func (ob *OrderBook) BestAsk() *models.Order {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	if ob.asks.Len() == 0 {
		return nil
	}
	return (*ob.asks)[0]
}

// PopBestBid removes and returns the best bid
func (ob *OrderBook) PopBestBid() *models.Order {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.bids.Len() == 0 {
		return nil
	}
	ob.version++
	order := heap.Pop(ob.bids).(*models.Order)
	delete(ob.orders, order.ID)
	return order
}

// PopBestAsk removes and returns the best ask
func (ob *OrderBook) PopBestAsk() *models.Order {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.asks.Len() == 0 {
		return nil
	}
	ob.version++
	order := heap.Pop(ob.asks).(*models.Order)
	delete(ob.orders, order.ID)
	return order
}

// CancelOrder removes an order by ID. Returns false if not found.
// Note: heap doesn't support arbitrary removal efficiently; we mark as
// CANCELLED and skip during matching (lazy deletion).
func (ob *OrderBook) CancelOrder(id string) bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	order, exists := ob.orders[id]
	if !exists {
		return false
	}
	ob.version++
	order.Status = models.StatusCancelled
	return true
}

// GetOrder returns an order by ID
func (ob *OrderBook) GetOrder(id string) (*models.Order, bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	o, ok := ob.orders[id]
	return o, ok
}

// Len returns the number of live resting orders on both sides.
func (ob *OrderBook) Len() int {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	return ob.bids.Len() + ob.asks.Len()
}

// Snapshot returns bids and asks in true price-time priority order.
func (ob *OrderBook) Snapshot() *models.OrderBookSnapshot {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	return &models.OrderBookSnapshot{
		Bids: collect(*ob.bids, true),
		Asks: collect(*ob.asks, false),
	}
}

// Depth returns the aggregated book, capped at maxLevels per side.
// maxLevels <= 0 means every level.
func (ob *OrderBook) Depth(maxLevels int) *models.DepthSnapshot {
	ob.mu.RLock()
	bids := collect(*ob.bids, true)
	asks := collect(*ob.asks, false)
	ob.mu.RUnlock()

	snap := &models.DepthSnapshot{
		Bids: aggregate(bids, maxLevels),
		Asks: aggregate(asks, maxLevels),
	}

	if len(snap.Bids) > 0 {
		best := snap.Bids[0].Price
		snap.BestBid = &best
	}
	if len(snap.Asks) > 0 {
		best := snap.Asks[0].Price
		snap.BestAsk = &best
	}
	if snap.BestBid != nil && snap.BestAsk != nil {
		spread := *snap.BestAsk - *snap.BestBid
		mid := (*snap.BestAsk + *snap.BestBid) / 2
		snap.Spread = &spread
		snap.Mid = &mid
	}

	return snap
}

// LiquidityAgainst returns the total resting quantity that an incoming order on
// incomingSide could immediately trade against.
//
// anyPrice models a market order, which has no limit and will take every level.
// Otherwise only levels at or better than limitPrice count.
//
// This exists for the fill-or-kill pre-check, which has to know whether the
// whole quantity is available *before* any fill happens. It sums rather than
// walks in price order, so it needs no sorting and allocates nothing beyond the
// dedup set.
func (ob *OrderBook) LiquidityAgainst(incomingSide string, limitPrice float64, anyPrice bool) int {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	var src []*models.Order
	if incomingSide == models.SideBuy {
		src = *ob.asks
	} else {
		src = *ob.bids
	}

	seen := make(map[string]struct{}, len(src))
	total := 0

	for _, o := range src {
		if o.Status == models.StatusCancelled || o.Status == models.StatusFilled || o.Remaining <= 0 {
			continue
		}
		if _, dup := seen[o.ID]; dup {
			continue
		}
		seen[o.ID] = struct{}{}

		if !anyPrice {
			// A buyer will not pay above its limit; a seller will not go below.
			if incomingSide == models.SideBuy && limitPrice < o.Price {
				continue
			}
			if incomingSide == models.SideSell && limitPrice > o.Price {
				continue
			}
		}

		total += o.Remaining
	}

	return total
}

// collect returns the live orders from a heap's backing slice in price-time
// priority order.
//
// Two things make this more than a filter. A binary heap's backing slice is
// only partially ordered — just index 0 is guaranteed to hold the best price —
// so the result has to be sorted explicitly rather than returned as-is. And
// because the same *Order pointer can legitimately be pushed onto a heap more
// than once, IDs are deduplicated so aggregated depth never double-counts.
func collect(src []*models.Order, descending bool) []*models.Order {
	out := make([]*models.Order, 0, len(src))
	seen := make(map[string]struct{}, len(src))

	for _, o := range src {
		if o.Status == models.StatusCancelled || o.Status == models.StatusFilled {
			continue
		}
		if o.Remaining <= 0 {
			continue
		}
		if _, dup := seen[o.ID]; dup {
			continue
		}
		seen[o.ID] = struct{}{}
		out = append(out, o)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Price != out[j].Price {
			if descending {
				return out[i].Price > out[j].Price
			}
			return out[i].Price < out[j].Price
		}
		return out[i].Timestamp < out[j].Timestamp
	})

	return out
}

// aggregate groups already-sorted orders into price levels and computes the
// cumulative depth walking away from the best price.
func aggregate(orders []*models.Order, maxLevels int) []models.PriceLevel {
	levels := make([]models.PriceLevel, 0, 16)

	for _, o := range orders {
		n := len(levels)
		if n > 0 && levels[n-1].Price == o.Price {
			levels[n-1].Quantity += o.Remaining
			levels[n-1].OrderCount++
			continue
		}
		if maxLevels > 0 && n >= maxLevels {
			break
		}
		levels = append(levels, models.PriceLevel{
			Price:      o.Price,
			Quantity:   o.Remaining,
			OrderCount: 1,
		})
	}

	cumulative := 0
	for i := range levels {
		cumulative += levels[i].Quantity
		levels[i].Cumulative = cumulative
	}

	return levels
}
