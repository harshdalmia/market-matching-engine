// Package exchange hosts one independent matching venue per instrument.
//
// Each symbol gets its own order book and its own matching goroutine, so two
// symbols never contend for the same lock or the same intake buffer. A slow or
// saturated instrument cannot stall the others.
package exchange

import (
	"sort"
	"strings"
	"time"

	"matching-engine/internal/engine"
	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
)

// FallbackSymbol is used when no symbols are configured at all.
const FallbackSymbol = "DEMO"

// Venue is a single instrument: its book, and the engine that matches into it.
type Venue struct {
	Symbol string
	Book   *orderbook.OrderBook
	Engine *engine.Engine
}

// Exchange is a fixed set of venues.
//
// The venue map is built once in New and never mutated afterwards, so lookups
// need no locking. Listing symbols at runtime is therefore also free of
// contention with matching.
type Exchange struct {
	venues        map[string]*Venue
	symbols       []string
	defaultSymbol string
}

// NormalizeSymbol canonicalises a symbol for lookup. Symbols are
// case-insensitive on the wire and stored uppercase.
func NormalizeSymbol(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// ParseSymbols turns a comma-separated list into a clean, deduplicated,
// ordered set. An empty or unusable list yields the fallback symbol so the
// process always has somewhere to route orders.
func ParseSymbols(raw string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 4)

	for _, part := range strings.Split(raw, ",") {
		s := NormalizeSymbol(part)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	if len(out) == 0 {
		return []string{FallbackSymbol}
	}

	sort.Strings(out)
	return out
}

// New builds an exchange over the given symbols. The first symbol in sorted
// order becomes the default for clients that do not name one.
func New(symbols []string) *Exchange {
	if len(symbols) == 0 {
		symbols = []string{FallbackSymbol}
	}

	x := &Exchange{
		venues:  make(map[string]*Venue, len(symbols)),
		symbols: make([]string, 0, len(symbols)),
	}

	for _, raw := range symbols {
		symbol := NormalizeSymbol(raw)
		if symbol == "" {
			continue
		}
		if _, exists := x.venues[symbol]; exists {
			continue
		}

		book := orderbook.New()
		x.venues[symbol] = &Venue{
			Symbol: symbol,
			Book:   book,
			Engine: engine.NewFor(symbol, book),
		}
		x.symbols = append(x.symbols, symbol)
	}

	sort.Strings(x.symbols)
	x.defaultSymbol = x.symbols[0]

	return x
}

// Symbols returns the tradable symbols in a stable order.
func (x *Exchange) Symbols() []string {
	out := make([]string, len(x.symbols))
	copy(out, x.symbols)
	return out
}

// DefaultSymbol is the venue used when a request omits a symbol.
func (x *Exchange) DefaultSymbol() string { return x.defaultSymbol }

// Venue looks up an exact, already-normalised symbol.
func (x *Exchange) Venue(symbol string) (*Venue, bool) {
	v, ok := x.venues[symbol]
	return v, ok
}

// Resolve maps a client-supplied symbol to a venue. An empty symbol resolves to
// the default, which is what keeps single-instrument clients working unchanged.
func (x *Exchange) Resolve(symbol string) (*Venue, bool) {
	normalized := NormalizeSymbol(symbol)
	if normalized == "" {
		normalized = x.defaultSymbol
	}
	v, ok := x.venues[normalized]
	return v, ok
}

// SetPublisher attaches an event publisher to every venue's engine.
//
// Wiring lives here rather than at the call site so there is exactly one place
// to get it right: a caller that constructs an exchange and a broker cannot
// silently forget to connect half the venues.
//
// Call before Start so no events are missed.
func (x *Exchange) SetPublisher(p engine.Publisher) {
	for _, symbol := range x.symbols {
		x.venues[symbol].Engine.SetPublisher(p)
	}
}

// SetObserver attaches a fill and order-state observer to every venue's engine.
//
// Centralised here for the same reason as SetPublisher: a caller wiring an
// exchange to a registry must not be able to connect only some of the venues.
//
// Call before Start so no fills are missed.
func (x *Exchange) SetObserver(o engine.Observer) {
	for _, symbol := range x.symbols {
		x.venues[symbol].Engine.SetObserver(o)
	}
}

// Marks returns the reference price for each symbol, used to value open
// positions. The last traded price, falling back to the mid for a venue that has
// not printed yet, and absent entirely for one with neither.
func (x *Exchange) Marks() map[string]float64 {
	out := make(map[string]float64, len(x.symbols))

	for _, symbol := range x.symbols {
		venue := x.venues[symbol]

		if last := venue.Engine.LastPrice(); last > 0 {
			out[symbol] = last
			continue
		}
		if depth := venue.Book.Depth(1); depth.Mid != nil {
			out[symbol] = *depth.Mid
		}
	}

	return out
}

// Start launches every venue's matching goroutine.
func (x *Exchange) Start() {
	for _, symbol := range x.symbols {
		x.venues[symbol].Engine.Start()
	}
}

// Drain waits until every venue's intake queue is empty, up to timeout.
// Reports whether it finished draining rather than timing out.
func (x *Exchange) Drain(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		pending := 0
		for _, symbol := range x.symbols {
			pending += x.venues[symbol].Engine.QueueDepth()
		}

		if pending == 0 {
			// An empty queue means the last order has been taken off it, not
			// that matching has finished; give the loop a moment to settle.
			time.Sleep(50 * time.Millisecond)
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}

	return false
}

// Stop shuts every venue down and waits for its matching loop to exit.
func (x *Exchange) Stop() {
	for _, symbol := range x.symbols {
		x.venues[symbol].Engine.Stop()
	}
}

// CancelOrder cancels by ID without the client needing to know the symbol.
//
// Order IDs are UUIDs and therefore globally unique, so searching venues is
// unambiguous. It returns the symbol the order belonged to.
func (x *Exchange) CancelOrder(id string) (string, bool) {
	for _, symbol := range x.symbols {
		venue := x.venues[symbol]

		if venue.Book.CancelOrder(id) {
			return symbol, true
		}
		// Untriggered stop orders wait off-book, so the order book cannot see
		// them and they need cancelling through the engine.
		if venue.Engine.CancelPendingStop(id) {
			return symbol, true
		}
	}
	return "", false
}

// FindOrder locates a resting order across all venues.
func (x *Exchange) FindOrder(id string) (*models.Order, string, bool) {
	for _, symbol := range x.symbols {
		if o, ok := x.venues[symbol].Book.GetOrder(id); ok {
			return o, symbol, true
		}
	}
	return nil, "", false
}

// SymbolStats is a per-instrument summary.
type SymbolStats struct {
	Symbol        string   `json:"symbol"`
	BestBid       *float64 `json:"best_bid"`
	BestAsk       *float64 `json:"best_ask"`
	Spread        *float64 `json:"spread"`
	Mid           *float64 `json:"mid"`
	LastPrice     *float64 `json:"last_price"`
	RestingOrders int      `json:"resting_orders"`
	PendingStops  int      `json:"pending_stops"`
	TradeCount    int      `json:"trade_count"`
	OrdersQueued  int      `json:"orders_queued"`
	OrdersMatched int64    `json:"orders_matched"`
	AvgLatencyMs  float64  `json:"avg_latency_ms"`
}

// Stats summarises one venue.
func (v *Venue) Stats() SymbolStats {
	depth := v.Book.Depth(1)
	matched, latency := v.Engine.Metrics()

	stats := SymbolStats{
		Symbol:        v.Symbol,
		BestBid:       depth.BestBid,
		BestAsk:       depth.BestAsk,
		Spread:        depth.Spread,
		Mid:           depth.Mid,
		RestingOrders: v.Book.Len(),
		PendingStops:  v.Engine.PendingStopCount(),
		TradeCount:    v.Engine.TradeCount(),
		OrdersQueued:  v.Engine.QueueDepth(),
		OrdersMatched: matched,
		AvgLatencyMs:  latency,
	}

	if recent := v.Engine.GetTrades(1); len(recent) > 0 {
		price := recent[0].Price
		stats.LastPrice = &price
	}

	return stats
}

// AllStats summarises every venue, in symbol order.
func (x *Exchange) AllStats() []SymbolStats {
	out := make([]SymbolStats, 0, len(x.symbols))
	for _, symbol := range x.symbols {
		out = append(out, x.venues[symbol].Stats())
	}
	return out
}

// Totals aggregates counters across every venue.
func (x *Exchange) Totals() (ordersMatched int64, avgLatencyMs float64, queued, resting, trades int) {
	var weightedLatency float64

	for _, symbol := range x.symbols {
		v := x.venues[symbol]
		matched, latency := v.Engine.Metrics()

		ordersMatched += matched
		// Weight each venue's mean by how many orders it measured, otherwise a
		// quiet instrument would drag the average as much as a busy one.
		weightedLatency += latency * float64(matched)

		queued += v.Engine.QueueDepth()
		resting += v.Book.Len()
		trades += v.Engine.TradeCount()
	}

	if ordersMatched > 0 {
		avgLatencyMs = weightedLatency / float64(ordersMatched)
	}

	return ordersMatched, avgLatencyMs, queued, resting, trades
}
