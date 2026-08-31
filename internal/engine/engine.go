package engine

import (
	"sort"
	"sync"
	"time"

	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
	"matching-engine/internal/utils"

	"github.com/google/uuid"
)

// maxTradeHistory caps the in-memory trade tape. Older prints are discarded
// once this many trades are retained.
const maxTradeHistory = 10000

// Publisher receives engine events as they happen.
//
// Implementations are called from the matching goroutine and MUST NOT block —
// anything slow or unbounded here directly becomes matching latency.
type Publisher interface {
	Publish(eventType, symbol string, data interface{})
}

// Publishers fans one event out to several publishers.
//
// Both the market data stream and the account registry need to observe the same
// events, and neither should have to know about the other.
type Publishers []Publisher

func (p Publishers) Publish(eventType, symbol string, data interface{}) {
	for _, target := range p {
		target.Publish(eventType, symbol, data)
	}
}

// Observer receives executions with both counterparties already resolved, plus
// every order state transition.
//
// A raw Trade carries only order IDs, so anything that needs to attribute a fill
// to a trader — position keeping, a cash ledger — would otherwise have to join
// trades against order events itself and get the ordering right. This hands over
// the resolved view instead.
//
// Called from the matching goroutine, so implementations must not block. Values
// are copies, so implementations may retain them.
type Observer interface {
	OnFill(trade models.Trade, buy, sell models.Order)
	OnOrderState(order models.Order)
}

// Event type names the engine emits. They mirror the constants in the stream
// package, duplicated here so the engine does not depend on the transport.
const (
	eventTrade = "trade"
	eventOrder = "order"
)

// Engine is the core matching engine.
// All orders flow through a channel so the matching loop is single-threaded
// — no data races on the order book.
type Engine struct {
	symbol    string
	ob        *orderbook.OrderBook
	orderChan chan *models.Order // buffered: 1000
	trades    []*models.Trade
	tradesMu  sync.RWMutex
	stopCh    chan struct{}
	wg        sync.WaitGroup

	// metrics
	totalOrders    int64
	totalLatencyNs int64
	metricsMu      sync.Mutex

	// publisher is optional; nil means events are not broadcast.
	publisher Publisher

	// observer is optional; nil means fills are not reported.
	observer Observer

	// Stop orders wait here, off-book and invisible to the order book, until the
	// last traded price crosses their trigger. Guarded by its own mutex because
	// cancellation arrives from HTTP handlers while the matching goroutine reads.
	stopsMu sync.Mutex
	stops   map[string]*models.Order

	// lastPrice is the most recent print, used to evaluate stop triggers.
	// Only touched by the matching goroutine.
	lastPrice float64
}

// SetPublisher attaches an event publisher. Call before Start.
func (e *Engine) SetPublisher(p Publisher) {
	e.publisher = p
}

// SetObserver attaches a fill and order-state observer. Call before Start.
func (e *Engine) SetObserver(o Observer) {
	e.observer = o
}

// emit publishes an event if a publisher is attached.
func (e *Engine) emit(eventType string, data interface{}) {
	if e.publisher == nil {
		return
	}
	e.publisher.Publish(eventType, e.symbol, data)
}

// publishOrder reports an order state transition to both the event stream and
// the observer, from a single snapshot so they cannot disagree.
func (e *Engine) publishOrder(order *models.Order) {
	snapshot := snapshotOf(order)

	e.emit(eventOrder, snapshot)
	if e.observer != nil {
		e.observer.OnOrderState(snapshot)
	}
}

// New creates an engine for an unnamed book. Useful for tests and for
// single-instrument use.
func New(ob *orderbook.OrderBook) *Engine {
	return NewFor("", ob)
}

// NewFor creates an engine bound to a symbol. Every trade it records is stamped
// with that symbol, so the exchange can fan events out per instrument.
func NewFor(symbol string, ob *orderbook.OrderBook) *Engine {
	return &Engine{
		symbol:    symbol,
		ob:        ob,
		orderChan: make(chan *models.Order, 1000),
		trades:    make([]*models.Trade, 0),
		stopCh:    make(chan struct{}),
		stops:     make(map[string]*models.Order),
	}
}

// Symbol returns the instrument this engine matches.
func (e *Engine) Symbol() string { return e.symbol }

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

// Submit queues an order for matching without ever blocking.
//
// It reports false when the intake buffer is saturated so the HTTP layer can
// shed load with a 503 immediately. A bare channel send would instead stall the
// request goroutine until the server's write timeout fired, turning
// backpressure into a pile of hung connections.
func (e *Engine) Submit(order *models.Order) bool {
	select {
	case e.orderChan <- order:
		return true
	default:
		return false
	}
}

// SubmitBlocking queues an order, waiting for buffer space if necessary.
//
// Used where dropping an order would be worse than waiting: the synthetic
// generator (dropping would invalidate its benchmark) and write-ahead log
// replay (dropping would silently corrupt recovered state). Never use it on the
// request path, where blocking becomes a hung connection.
func (e *Engine) SubmitBlocking(order *models.Order) {
	e.orderChan <- order
}

// QueueDepth reports how many orders are waiting to be matched.
func (e *Engine) QueueDepth() int {
	return len(e.orderChan)
}

// TradeCount returns the number of trades currently retained in memory.
func (e *Engine) TradeCount() int {
	e.tradesMu.RLock()
	defer e.tradesMu.RUnlock()
	return len(e.trades)
}

// GetTrades returns retained trades oldest-first. A limit greater than zero
// returns only the most recent limit trades.
func (e *Engine) GetTrades(limit int) []*models.Trade {
	e.tradesMu.RLock()
	defer e.tradesMu.RUnlock()

	src := e.trades
	if limit > 0 && limit < len(src) {
		src = src[len(src)-limit:]
	}

	result := make([]*models.Trade, len(src))
	copy(result, src)
	return result
}

// recordTrade appends a trade, discarding the oldest prints once the history
// reaches maxTradeHistory. Without this the slice — and the /trades response
// built from it — grows without bound for the lifetime of the process.
func (e *Engine) recordTrade(trade *models.Trade) {
	e.tradesMu.Lock()
	defer e.tradesMu.Unlock()

	e.trades = append(e.trades, trade)
	if excess := len(e.trades) - maxTradeHistory; excess > 0 {
		e.trades = append(e.trades[:0], e.trades[excess:]...)
	}
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

// normalize fills in the optional order attributes so the rest of the engine can
// assume they are always set. Clients written against the original API send
// neither, and those orders must keep behaving as GTC limits.
func normalize(order *models.Order) {
	if order.Type == "" {
		order.Type = models.TypeLimit
	}
	if order.TimeInForce == "" {
		order.TimeInForce = models.TIFGTC
	}
	// A market order has no price. Clearing it here means nothing downstream can
	// accidentally treat a stale value as a limit.
	if order.Type == models.TypeMarket {
		order.Price = 0
	}
	// StopPrice is deliberately preserved here even though the order may no
	// longer be of type STOP. A triggered stop is rewritten to LIMIT or MARKET,
	// and clearing the trigger at that point would erase the only evidence of
	// why the order entered the book — which the order history needs. The API
	// layer already guarantees a stop price is only set on STOP orders.
}

// isValid reports whether an order can be matched at all, marking it REJECTED
// if not. Market orders are exempt from the price check by design.
func isValid(order *models.Order) bool {
	if order.Quantity <= 0 {
		order.Status = models.StatusRejected
		return false
	}
	if order.Type == models.TypeLimit && order.Price <= 0 {
		order.Status = models.StatusRejected
		return false
	}
	// A stop with no trigger price would either fire immediately or never; either
	// way it is not what the client meant.
	if order.Type == models.TypeStop && order.StopPrice <= 0 {
		order.Status = models.StatusRejected
		return false
	}
	if order.Side != models.SideBuy && order.Side != models.SideSell {
		order.Status = models.StatusRejected
		return false
	}
	return true
}

// crosses reports whether an incoming order is willing to trade at price.
func crosses(incoming *models.Order, price float64) bool {
	if incoming.Type == models.TypeMarket {
		return true // no price limit
	}
	if incoming.Side == models.SideBuy {
		return incoming.Price >= price
	}
	return incoming.Price <= price
}

// canRest reports whether an unfilled remainder may join the book.
//
// Only a good-till-cancelled limit order can rest. IOC and FOK are immediate by
// definition, and a market order has no price to rest at.
func canRest(order *models.Order) bool {
	return order.Type == models.TypeLimit && order.TimeInForce == models.TIFGTC
}

// processOrder handles an order and then drains any stop orders its prints
// triggered.
//
// The cascade is iterative rather than recursive: a triggered stop can move the
// price enough to trigger another, and letting that recurse would put an
// unbounded chain of activations on the goroutine stack.
func (e *Engine) processOrder(incoming *models.Order) {
	e.handle(incoming)

	for {
		triggered := e.takeTriggeredStops()
		if len(triggered) == 0 {
			return
		}
		for _, order := range triggered {
			e.handle(order)
		}
	}
}

// takeTriggeredStops removes and returns every pending stop whose trigger price
// the market has reached, converted into its executable form.
//
// Activation order is by submission time so a cascade is deterministic rather
// than dependent on Go's random map iteration order.
func (e *Engine) takeTriggeredStops() []*models.Order {
	e.stopsMu.Lock()

	// With no prints yet there is no price to compare against, and treating the
	// zero value as a real price would fire every sell stop immediately.
	if e.lastPrice <= 0 || len(e.stops) == 0 {
		e.stopsMu.Unlock()
		return nil
	}

	var ready []*models.Order
	for id, order := range e.stops {
		if !stopTriggered(order, e.lastPrice) {
			continue
		}
		ready = append(ready, order)
		delete(e.stops, id)
	}
	e.stopsMu.Unlock()

	if len(ready) == 0 {
		return nil
	}

	sort.Slice(ready, func(i, j int) bool {
		return ready[i].Timestamp < ready[j].Timestamp
	})

	for _, order := range ready {
		// A stop with a price becomes a limit order; without one it becomes a
		// market order and takes whatever the book offers.
		if order.Price > 0 {
			order.Type = models.TypeLimit
		} else {
			order.Type = models.TypeMarket
		}
		order.Status = models.StatusNew
	}

	return ready
}

// stopTriggered reports whether the market has reached a stop's trigger price.
// A buy stop fires on the way up, a sell stop on the way down.
func stopTriggered(order *models.Order, lastPrice float64) bool {
	if order.Side == models.SideBuy {
		return lastPrice >= order.StopPrice
	}
	return lastPrice <= order.StopPrice
}

// CancelPendingStop cancels a stop order that has not triggered yet.
//
// Pending stops are not in the order book, so the book's own CancelOrder cannot
// see them. Reports whether the order was found.
func (e *Engine) CancelPendingStop(id string) bool {
	e.stopsMu.Lock()
	order, ok := e.stops[id]
	if ok {
		delete(e.stops, id)
	}
	e.stopsMu.Unlock()

	if !ok {
		return false
	}

	order.Status = models.StatusCancelled
	e.publishOrder(order)
	return true
}

// PendingStops returns the stop orders waiting to trigger.
func (e *Engine) PendingStops() []*models.Order {
	e.stopsMu.Lock()
	defer e.stopsMu.Unlock()

	out := make([]*models.Order, 0, len(e.stops))
	for _, order := range e.stops {
		copied := *order
		out = append(out, &copied)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

// PendingStopCount reports how many stop orders are waiting to trigger.
func (e *Engine) PendingStopCount() int {
	e.stopsMu.Lock()
	defer e.stopsMu.Unlock()
	return len(e.stops)
}

// handle runs the price-time priority matching logic for a single order.
func (e *Engine) handle(incoming *models.Order) {
	normalize(incoming)

	if !isValid(incoming) {
		e.publishOrder(incoming)
		return
	}

	// A stop order is not tradable yet: park it off-book and wait for the market
	// to come to it.
	if incoming.Type == models.TypeStop {
		e.stopsMu.Lock()
		e.stops[incoming.ID] = incoming
		e.stopsMu.Unlock()

		incoming.Status = models.StatusPending
		e.publishOrder(incoming)
		return
	}

	// Fill-or-kill is all-or-nothing, so availability has to be checked before
	// any fill occurs — once a trade has printed it cannot be taken back.
	if incoming.TimeInForce == models.TIFFOK {
		available := e.ob.LiquidityAgainst(
			incoming.Side,
			incoming.Price,
			incoming.Type == models.TypeMarket,
		)
		if available < incoming.Remaining {
			incoming.Status = models.StatusCancelled
			e.publishOrder(incoming)
			return
		}
	}

	if incoming.Side == models.SideBuy {
		e.matchBuy(incoming)
	} else {
		e.matchSell(incoming)
	}

	if incoming.Remaining <= 0 {
		return // fully filled; status and event already handled by fill
	}

	// Anything that cannot rest is terminal with its remainder unfilled. The
	// filled amount is still recoverable as Quantity - Remaining.
	if !canRest(incoming) {
		incoming.Status = models.StatusCancelled
		e.publishOrder(incoming)
		return
	}

	if incoming.Status != models.StatusCancelled {
		e.ob.AddOrder(incoming)
		e.publishOrder(incoming)
	}
}

// matchBuy walks the asks from the lowest price up, filling the incoming buy.
//
// The resting order is filled in place at the top of the heap and only popped
// once it is exhausted. Popping first and re-adding the remainder — as this used
// to do — both destroyed liquidity when the remainder was dropped and briefly
// removed the order from the ID index, making it uncancellable.
func (e *Engine) matchBuy(buy *models.Order) {
	for buy.Remaining > 0 {
		best := e.ob.BestAsk()
		if best == nil {
			break
		}

		// Lazy deletion: cancelled and exhausted orders linger in the heap.
		if isDead(best) {
			e.ob.PopBestAsk()
			continue
		}

		// Stop as soon as the book no longer crosses. A market order always
		// crosses, so it walks the book until filled or the asks run out.
		if !crosses(buy, best.Price) {
			break
		}

		e.fill(best, buy)

		if best.Remaining == 0 {
			e.ob.PopBestAsk()
		}
	}
}

// matchSell walks the bids from the highest price down, filling the incoming sell.
func (e *Engine) matchSell(sell *models.Order) {
	for sell.Remaining > 0 {
		best := e.ob.BestBid()
		if best == nil {
			break
		}

		if isDead(best) {
			e.ob.PopBestBid()
			continue
		}

		if !crosses(sell, best.Price) {
			break
		}

		e.fill(best, sell)

		if best.Remaining == 0 {
			e.ob.PopBestBid()
		}
	}
}

// isDead reports whether a resting order should be swept from the book instead
// of matched against.
func isDead(order *models.Order) bool {
	return order.Status == models.StatusCancelled ||
		order.Status == models.StatusFilled ||
		order.Remaining <= 0
}

// fill executes one trade between a resting order and the incoming aggressor,
// mutating both and recording the print.
//
// The two roles are explicit parameters rather than "buy" and "sell" because the
// price and the book bookkeeping depend on which order was resting, not on which
// side it was. The previous signature assumed the sell was always the resting
// order — true for buy-aggressor flow, backwards for sell-aggressor flow — which
// mispriced half of all prints and dropped resting bids on the floor.
//
// Book membership is deliberately not touched here. The match loop owns it, so
// a partially filled resting order keeps its heap position and therefore its
// time priority at that price.
func (e *Engine) fill(resting, incoming *models.Order) {
	fillQty := min(resting.Remaining, incoming.Remaining)
	if fillQty <= 0 {
		return
	}

	// Maker pricing: the resting order was there first and sets the price.
	tradePrice := resting.Price

	buyID, sellID := resting.ID, incoming.ID
	if incoming.Side == models.SideBuy {
		buyID, sellID = incoming.ID, resting.ID
	}

	trade := &models.Trade{
		ID:          uuid.New().String(),
		Symbol:      e.symbol,
		BuyOrderID:  buyID,
		SellOrderID: sellID,
		Price:       tradePrice,
		Quantity:    fillQty,
		Timestamp:   time.Now().UnixNano(),
	}

	applyFill(resting, fillQty)
	applyFill(incoming, fillQty)

	e.recordTrade(trade)

	// Drives stop triggering. Only the matching goroutine touches this.
	e.lastPrice = tradePrice

	e.emit(eventTrade, trade)

	// Both sides changed state, so both are worth reporting alongside the print.
	e.publishOrder(resting)
	e.publishOrder(incoming)

	if e.observer != nil {
		buyOrder, sellOrder := resting, incoming
		if incoming.Side == models.SideBuy {
			buyOrder, sellOrder = incoming, resting
		}
		e.observer.OnFill(*trade, *buyOrder, *sellOrder)
	}
}

// LastPrice returns the most recent traded price, or 0 before the first print.
func (e *Engine) LastPrice() float64 {
	e.tradesMu.RLock()
	defer e.tradesMu.RUnlock()
	if len(e.trades) == 0 {
		return 0
	}
	return e.trades[len(e.trades)-1].Price
}

// snapshotOf copies an order for publication.
//
// Resting orders are mutated in place by later fills, so handing the live
// pointer to subscribers would let them observe a value that changes after the
// event was sent — or race with the matching loop outright.
func snapshotOf(order *models.Order) models.Order {
	return *order
}

// applyFill decrements an order's remaining quantity and advances its status.
func applyFill(order *models.Order, qty int) {
	order.Remaining -= qty
	if order.Remaining <= 0 {
		order.Status = models.StatusFilled
		return
	}
	order.Status = models.StatusPartial
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
