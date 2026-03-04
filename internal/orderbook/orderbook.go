package orderbook

import (
	"container/heap"
	"matching-engine/internal/models"
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
	bids   *BidHeap            // buy orders
	asks   *AskHeap            // sell orders
	orders map[string]*models.Order // fast lookup by ID
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

// Snapshot returns a copy of bids and asks for the API response
func (ob *OrderBook) Snapshot() *models.OrderBookSnapshot {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	bids := make([]*models.Order, 0, ob.bids.Len())
	for _, o := range *ob.bids {
		if o.Status != models.StatusCancelled && o.Status != models.StatusFilled {
			bids = append(bids, o)
		}
	}

	asks := make([]*models.Order, 0, ob.asks.Len())
	for _, o := range *ob.asks {
		if o.Status != models.StatusCancelled && o.Status != models.StatusFilled {
			asks = append(asks, o)
		}
	}

	return &models.OrderBookSnapshot{
		Bids: bids,
		Asks: asks,
	}
}
