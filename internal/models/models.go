package models

// Order status constants
const (
	StatusNew       = "NEW"
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

// Order represents a single order in the system
type Order struct {
	ID        string  `json:"id"`
	TraderID  string  `json:"trader_id"`
	Side      string  `json:"side"`      // BUY or SELL
	Price     float64 `json:"price"`
	Quantity  int     `json:"quantity"`
	Remaining int     `json:"remaining"`
	Timestamp int64   `json:"timestamp"`
	Status    string  `json:"status"`
}

// Trade represents a matched trade between a buy and sell order
type Trade struct {
	ID          string  `json:"id"`
	BuyOrderID  string  `json:"buy_order_id"`
	SellOrderID string  `json:"sell_order_id"`
	Price       float64 `json:"price"`
	Quantity    int     `json:"quantity"`
	Timestamp   int64   `json:"timestamp"`
}

// PlaceOrderRequest is the incoming JSON body for placing an order
type PlaceOrderRequest struct {
	TraderID string  `json:"trader_id"`
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	Quantity int     `json:"quantity"`
}

// CancelOrderResult is returned after a cancel attempt
type CancelOrderResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// OrderBookSnapshot is returned by GET /orderbook
type OrderBookSnapshot struct {
	Bids []*Order `json:"bids"` // sorted: highest price first
	Asks []*Order `json:"asks"` // sorted: lowest price first
}

// EngineResult wraps trades produced from a single order match cycle
type EngineResult struct {
	Trades    []*Trade
	OrderID   string
	Latency   int64 // nanoseconds
}
