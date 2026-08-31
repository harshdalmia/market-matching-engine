package models

// Order status constants.
//
// StatusPending is specific to stop orders: they sit off-book, invisible to the
// order book, until their trigger price is crossed.
const (
	StatusNew       = "NEW"
	StatusPending   = "PENDING"
	StatusPartial   = "PARTIALLY_FILLED"
	StatusFilled    = "FILLED"
	StatusCancelled = "CANCELLED"
	StatusRejected  = "REJECTED"
)

// Order side constants
const (
	SideBuy  = "BUY"
	SideSell = "SELL"
)

// Order type constants.
//
// A LIMIT order will not trade worse than its price. A MARKET order has no
// price limit and takes whatever the book offers; its Price field is ignored.
//
// A STOP order is held off-book until the last traded price crosses StopPrice.
// A buy stop triggers when the market rises to or through it; a sell stop
// triggers when the market falls to or through it. On triggering it becomes a
// LIMIT order if Price is set, or a MARKET order if it is not.
const (
	TypeLimit  = "LIMIT"
	TypeMarket = "MARKET"
	TypeStop   = "STOP"
)

// Time-in-force constants.
//
//	GTC — Good till cancelled. Any unfilled remainder rests in the book.
//	IOC — Immediate or cancel. Fill what is available now, cancel the rest.
//	FOK — Fill or kill. Fill the entire quantity immediately or do nothing.
//
// Only GTC can rest. MARKET orders never rest regardless of time-in-force,
// because resting requires a price and they have none.
const (
	TIFGTC = "GTC"
	TIFIOC = "IOC"
	TIFFOK = "FOK"
)

// Order represents a single order in the system
type Order struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	TraderID    string  `json:"trader_id"`
	Side        string  `json:"side"`          // BUY or SELL
	Type        string  `json:"type"`          // LIMIT, MARKET or STOP
	TimeInForce string  `json:"time_in_force"` // GTC, IOC or FOK
	Price       float64 `json:"price"`         // ignored for MARKET orders
	StopPrice   float64 `json:"stop_price"`    // trigger price, STOP orders only
	Quantity    int     `json:"quantity"`
	Remaining   int     `json:"remaining"`
	Timestamp   int64   `json:"timestamp"`
	Status      string  `json:"status"`
}

// Trade represents a matched trade between a buy and sell order
type Trade struct {
	ID          string  `json:"id"`
	Symbol      string  `json:"symbol"`
	BuyOrderID  string  `json:"buy_order_id"`
	SellOrderID string  `json:"sell_order_id"`
	Price       float64 `json:"price"`
	Quantity    int     `json:"quantity"`
	Timestamp   int64   `json:"timestamp"`
}

// PlaceOrderRequest is the incoming JSON body for placing an order.
//
// Type and TimeInForce are optional and default to LIMIT and GTC, so clients
// written against the original API keep working unchanged.
type PlaceOrderRequest struct {
	Symbol      string  `json:"symbol"`
	TraderID    string  `json:"trader_id"`
	Side        string  `json:"side"`
	Type        string  `json:"type"`
	TimeInForce string  `json:"time_in_force"`
	Price       float64 `json:"price"`
	StopPrice   float64 `json:"stop_price"`
	Quantity    int     `json:"quantity"`
}

// CancelOrderResult is returned after a cancel attempt
type CancelOrderResult struct {
	Success bool   `json:"success"`
	Symbol  string `json:"symbol"`
	Message string `json:"message"`
}

// OrderBookSnapshot is returned by GET /orderbook.
// Bids are ordered highest price first, asks lowest price first, and within a
// price by earliest timestamp — i.e. true price-time priority. Duplicate order
// IDs are filtered out before the snapshot is built.
type OrderBookSnapshot struct {
	Bids []*Order `json:"bids"`
	Asks []*Order `json:"asks"`
}

// PriceLevel is the aggregated resting quantity at a single price.
// Quantity is the sum of Remaining across every order at that price, and
// Cumulative is the running total walking away from the best price.
type PriceLevel struct {
	Price      float64 `json:"price"`
	Quantity   int     `json:"quantity"`
	OrderCount int     `json:"order_count"`
	Cumulative int     `json:"cumulative"`
}

// DepthSnapshot is returned by GET /depth. It is the aggregated form of
// OrderBookSnapshot and is much cheaper to send than every individual order.
// BestBid, BestAsk, Spread and Mid are nil when the relevant side is empty.
type DepthSnapshot struct {
	Symbol  string       `json:"symbol"`
	Bids    []PriceLevel `json:"bids"`
	Asks    []PriceLevel `json:"asks"`
	BestBid *float64     `json:"best_bid"`
	BestAsk *float64     `json:"best_ask"`
	Spread  *float64     `json:"spread"`
	Mid     *float64     `json:"mid"`
}

// HealthResponse is returned by GET /health for load balancer probes.
type HealthResponse struct {
	Status        string   `json:"status"`
	UptimeSeconds float64  `json:"uptime_seconds"`
	OrdersQueued  int      `json:"orders_queued"`
	RestingOrders int      `json:"resting_orders"`
	TradeCount    int      `json:"trade_count"`
	Symbols       []string `json:"symbols"`
}

// EngineResult wraps trades produced from a single order match cycle
type EngineResult struct {
	Trades  []*Trade
	OrderID string
	Latency int64 // nanoseconds
}
