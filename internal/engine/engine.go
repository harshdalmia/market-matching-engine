package engine

import (
	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
	"matching-engine/internal/utils"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Engine is the core matching engine.
// All orders flow through a channel so the matching loop is single-threaded
// — no data races on the order book.
type Engine struct {
	ob        *orderbook.OrderBook
	orderChan chan *models.Order   // buffered: 1000
	trades    []*models.Trade
	tradesMu  sync.RWMutex
	stopCh    chan struct{}
	wg        sync.WaitGroup

	// metrics
	totalOrders    int64
	totalLatencyNs int64
	metricsMu      sync.Mutex
}

func New(ob *orderbook.OrderBook) *Engine {
	return &Engine{
		ob:        ob,
		orderChan: make(chan *models.Order, 1000),
		trades:    make([]*models.Trade, 0),
		stopCh:    make(chan struct{}),
	}
}

// Start launches the matching goroutine
func (e *Engine) Start() {
	e.wg.Add(1)
	go e.run()
	utils.LogInfo("Matching engine started")
}

// Stop gracefully shuts down the engine
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()
	utils.LogInfo("Matching engine stopped")
}

// Submit sends an order to the engine for processing (non-blocking if buffer available)
func (e *Engine) Submit(order *models.Order) {
	e.orderChan <- order
}

// GetTrades returns all executed trades
func (e *Engine) GetTrades() []*models.Trade {
	e.tradesMu.RLock()
	defer e.tradesMu.RUnlock()
	result := make([]*models.Trade, len(e.trades))
	copy(result, e.trades)
	return result
}

// run is the single-threaded event loop
func (e *Engine) run() {
	defer e.wg.Done()
	for {
		select {
		case order := <-e.orderChan:
			start := time.Now()
			e.processOrder(order)
			latency := time.Since(start).Nanoseconds()

			utils.LogLatency(order.ID, latency)

			e.metricsMu.Lock()
			e.totalOrders++
			e.totalLatencyNs += latency
			e.metricsMu.Unlock()

		case <-e.stopCh:
			return
		}
	}
}

// processOrder runs the price-time priority matching logic
func (e *Engine) processOrder(incoming *models.Order) {
	//utils.LogOrderReceived(incoming.ID, incoming.Side, incoming.Price, incoming.Quantity)

	// Validate
	if incoming.Price <= 0 || incoming.Quantity <= 0 {
		incoming.Status = models.StatusRejected
		//utils.LogOrderStateChange(incoming.ID, models.StatusNew, models.StatusRejected)
		return
	}

	if incoming.Side == models.SideBuy {
		e.matchBuy(incoming)
	} else {
		e.matchSell(incoming)
	}

	// If there's remaining quantity, add to the order book
	if incoming.Remaining > 0 && incoming.Status != models.StatusCancelled {
		e.ob.AddOrder(incoming)
	}
}

// matchBuy: BUY order matches against lowest SELL (asks)
func (e *Engine) matchBuy(buy *models.Order) {
	for buy.Remaining > 0 {
		bestAsk := e.ob.BestAsk()
		if bestAsk == nil {
			break
		}

		// Skip cancelled or filled orders (lazy deletion)
		if bestAsk.Status == models.StatusCancelled || bestAsk.Status == models.StatusFilled {
			e.ob.PopBestAsk()
			continue
		}

		// Price check: buy price must be >= ask price
		if buy.Price < bestAsk.Price {
			break
		}

		// Pop from book and execute trade
		ask := e.ob.PopBestAsk()
		e.executeTrade(buy, ask)
	}
}

// matchSell: SELL order matches against highest BUY (bids)
func (e *Engine) matchSell(sell *models.Order) {
	for sell.Remaining > 0 {
		bestBid := e.ob.BestBid()
		if bestBid == nil {
			break
		}

		// Skip cancelled or filled orders (lazy deletion)
		if bestBid.Status == models.StatusCancelled || bestBid.Status == models.StatusFilled {
			e.ob.PopBestBid()
			continue
		}

		// Price check: sell price must be <= bid price
		if sell.Price > bestBid.Price {
			break
		}

		// Pop from book and execute trade
		bid := e.ob.PopBestBid()
		e.executeTrade(bid, sell)
	}
}

// executeTrade creates a trade record and updates both orders' quantities/statuses
func (e *Engine) executeTrade(buy, sell *models.Order) {
	// Fill quantity = minimum of both remaining
	fillQty := min(buy.Remaining, sell.Remaining)

	// Trade price = the resting order's price (the order already in the book)
	tradePrice := sell.Price // sell is typically the resting order, but this can vary

	trade := &models.Trade{
		ID:          uuid.New().String(),
		BuyOrderID:  buy.ID,
		SellOrderID: sell.ID,
		Price:       tradePrice,
		Quantity:    fillQty,
		Timestamp:   time.Now().UnixNano(),
	}

	// Update buy order
	//prevBuyStatus := buy.Status
	buy.Remaining -= fillQty
	if buy.Remaining == 0 {
		buy.Status = models.StatusFilled
	} else {
		buy.Status = models.StatusPartial
	}
	//utils.LogOrderStateChange(buy.ID, prevBuyStatus, buy.Status)

	// Update sell order
	//prevSellStatus := sell.Status
	sell.Remaining -= fillQty
	if sell.Remaining == 0 {
		sell.Status = models.StatusFilled
	} else {
		sell.Status = models.StatusPartial
		// Sell was partially filled — put it back in the book
		e.ob.AddOrder(sell)
	}
	//utils.LogOrderStateChange(sell.ID, prevSellStatus, sell.Status)

	// Record trade
	e.tradesMu.Lock()
	e.trades = append(e.trades, trade)
	e.tradesMu.Unlock()

	//utils.LogTradeExecuted(trade.ID, trade.BuyOrderID, trade.SellOrderID, trade.Price, trade.Quantity)
}

// Metrics returns throughput stats
func (e *Engine) Metrics() (totalOrders int64, avgLatencyMs float64) {
	e.metricsMu.Lock()
	defer e.metricsMu.Unlock()
	if e.totalOrders == 0 {
		return 0, 0
	}
	return e.totalOrders, float64(e.totalLatencyNs) / float64(e.totalOrders) / 1e6
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
