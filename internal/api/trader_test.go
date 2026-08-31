package api

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"matching-engine/internal/exchange"
)

// crossPair posts a matching sell and buy, then waits for the print to land.
func crossPair(t *testing.T, router http.Handler, x *exchange.Exchange, symbol, buyer, seller string, price float64, qty int) {
	t.Helper()

	body := func(trader, side string) string {
		return `{"symbol":"` + symbol + `","trader_id":"` + trader + `","side":"` + side +
			`","type":"LIMIT","time_in_force":"GTC","price":` + ftoa(price) + `,"quantity":` + itoa(qty) + `}`
	}

	doJSON(t, router, http.MethodPost, "/order", body(seller, "SELL"))
	doJSON(t, router, http.MethodPost, "/order", body(buyer, "BUY"))

	venue, ok := x.Venue(symbol)
	if !ok {
		t.Fatalf("unknown symbol %s", symbol)
	}

	deadline := time.Now().Add(2 * time.Second)
	for venue.Engine.TradeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if venue.Engine.TradeCount() == 0 {
		t.Fatalf("no trade printed on %s", symbol)
	}
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func itoa(v int) string { return strconv.Itoa(v) }

// -----------------------------------------------------------------------
// /stats
// -----------------------------------------------------------------------

func TestStatsEndpoint(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	crossPair(t, router, x, symbol, "buyer", "seller", 100.00, 5)

	rec, body := doJSON(t, router, http.MethodGet, "/stats?symbol="+symbol, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	if body["symbol"] != symbol {
		t.Errorf("symbol: want %s, got %v", symbol, body["symbol"])
	}
	if body["last"] != 100.0 {
		t.Errorf("last: want 100, got %v", body["last"])
	}
	if body["volume"] != float64(5) {
		t.Errorf("volume: want 5, got %v", body["volume"])
	}
	// 100 * 5 — the notional turnover the design's VOLUME field shows.
	if body["quote_volume"] != 500.0 {
		t.Errorf("quote volume: want 500, got %v", body["quote_volume"])
	}
	for _, field := range []string{"open", "high", "low", "change", "change_percent", "window_seconds"} {
		if _, ok := body[field]; !ok {
			t.Errorf("stats response missing %q", field)
		}
	}
}

func TestStatsEmptyMarket(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["last"] != 0.0 || body["trade_count"] != 0.0 {
		t.Errorf("an untraded market should report zeroes, got %v", body)
	}
}

// -----------------------------------------------------------------------
// /candles
// -----------------------------------------------------------------------

func TestCandlesEndpoint(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	crossPair(t, router, x, symbol, "buyer", "seller", 100.00, 5)

	rec, body := doJSON(t, router, http.MethodGet, "/candles?symbol="+symbol+"&interval=1m", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	if body["interval"] != "1m" {
		t.Errorf("interval should be echoed, got %v", body["interval"])
	}

	candles, ok := body["candles"].([]any)
	if !ok {
		t.Fatalf("candles should be an array, got %T", body["candles"])
	}
	if len(candles) != 1 {
		t.Fatalf("want 1 candle, got %d", len(candles))
	}

	candle, ok := candles[0].(map[string]any)
	if !ok {
		t.Fatalf("candle should be an object, got %T", candles[0])
	}
	for _, field := range []string{"open_time", "close_time", "open", "high", "low", "close", "volume", "trades"} {
		if _, present := candle[field]; !present {
			t.Errorf("candle missing %q", field)
		}
	}
	if candle["close"] != 100.0 {
		t.Errorf("close: want 100, got %v", candle["close"])
	}
}

// An unknown interval is rejected rather than silently substituted, which would
// hand the client a chart at a timeframe it did not ask for.
func TestCandlesRejectsUnknownInterval(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/candles?interval=3s", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rec.Code)
	}
	if body["error"] == nil {
		t.Errorf("response should explain the rejection")
	}
}

func TestCandlesEmptyMarket(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/candles", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	candles, ok := body["candles"].([]any)
	if !ok || len(candles) != 0 {
		t.Errorf("want an empty array, got %v", body["candles"])
	}
}

// -----------------------------------------------------------------------
// /account
// -----------------------------------------------------------------------

func TestAccountReflectsFills(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	crossPair(t, router, x, symbol, "alice", "bob", 100.00, 5)

	rec, body := doJSON(t, router, http.MethodGet, "/account?trader_id=alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	// Bought 5 at 100, so 500 has left the cash balance.
	if body["cash"] != testStartingCash-500 {
		t.Errorf("cash: want %.2f, got %v", testStartingCash-500, body["cash"])
	}
	if body["starting_cash"] != testStartingCash {
		t.Errorf("starting cash: want %.2f, got %v", testStartingCash, body["starting_cash"])
	}

	positions, ok := body["positions"].([]any)
	if !ok || len(positions) != 1 {
		t.Fatalf("want 1 position, got %v", body["positions"])
	}

	position, ok := positions[0].(map[string]any)
	if !ok {
		t.Fatalf("position should be an object, got %T", positions[0])
	}
	if position["quantity"] != float64(5) {
		t.Errorf("quantity: want 5, got %v", position["quantity"])
	}
	if position["avg_entry"] != 100.0 {
		t.Errorf("avg entry: want 100, got %v", position["avg_entry"])
	}
	// Marked at the last trade, so the position is worth what it cost.
	if position["mark_price"] != 100.0 {
		t.Errorf("mark: want 100, got %v", position["mark_price"])
	}
}

func TestAccountSellerIsCredited(t *testing.T) {
	router, x := newTestServer(t)
	crossPair(t, router, x, testSymbols[0], "alice", "bob", 100.00, 5)

	_, body := doJSON(t, router, http.MethodGet, "/account?trader_id=bob", "")

	if body["cash"] != testStartingCash+500 {
		t.Errorf("seller cash: want %.2f, got %v", testStartingCash+500, body["cash"])
	}
}

func TestAccountRequiresTraderID(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/account", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rec.Code)
	}
	if body["error"] == nil {
		t.Errorf("response should say trader_id is required")
	}
}

func TestAccountUnknownTraderGetsOpeningBalance(t *testing.T) {
	router, _ := newTestServer(t)

	_, body := doJSON(t, router, http.MethodGet, "/account?trader_id=nobody", "")

	if body["cash"] != testStartingCash {
		t.Errorf("cash: want the opening balance %.2f, got %v", testStartingCash, body["cash"])
	}
	if body["equity"] != testStartingCash {
		t.Errorf("equity: want %.2f, got %v", testStartingCash, body["equity"])
	}
}

// -----------------------------------------------------------------------
// /orders
// -----------------------------------------------------------------------

func TestTraderOrdersSeparatesOpenFromHistory(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	// A resting order that stays open.
	doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+symbol+`","trader_id":"alice","side":"BUY","type":"LIMIT","time_in_force":"GTC","price":50.00,"quantity":5}`)

	// A crossing pair that finishes.
	crossPair(t, router, x, symbol, "alice", "bob", 100.00, 5)

	rec, body := doJSON(t, router, http.MethodGet, "/orders?trader_id=alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	open, _ := body["open"].([]any)
	history, _ := body["history"].([]any)

	if len(open) != 1 {
		t.Errorf("want 1 working order, got %d", len(open))
	}
	if len(history) != 1 {
		t.Errorf("want 1 finished order, got %d", len(history))
	}
}

func TestTraderOrdersRequiresTraderID(t *testing.T) {
	router, _ := newTestServer(t)

	rec, _ := doJSON(t, router, http.MethodGet, "/orders", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rec.Code)
	}
}

func TestTraderOrdersRejectsUnknownSymbol(t *testing.T) {
	router, _ := newTestServer(t)

	rec, _ := doJSON(t, router, http.MethodGet, "/orders?trader_id=alice&symbol=NOPE", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------
// STOP orders over HTTP
// -----------------------------------------------------------------------

func TestPlaceStopOrder(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	// Establish a price so the stop has something to compare against.
	crossPair(t, router, x, symbol, "buyer", "seller", 100.00, 5)

	rec, body := doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+symbol+`","trader_id":"alice","side":"BUY","type":"STOP","stop_price":150.00,"price":160.00,"quantity":5}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d (%v)", rec.Code, body["error"])
	}
	if body["type"] != "STOP" {
		t.Errorf("type: want STOP, got %v", body["type"])
	}
	if body["stop_price"] != 150.0 {
		t.Errorf("stop price should be echoed, got %v", body["stop_price"])
	}

	venue, _ := x.Venue(symbol)
	deadline := time.Now().Add(2 * time.Second)
	for venue.Engine.PendingStopCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if venue.Engine.PendingStopCount() != 1 {
		t.Errorf("want 1 pending stop, got %d", venue.Engine.PendingStopCount())
	}
	// A pending stop is not liquidity, so it must not appear in the book.
	if venue.Book.Len() != 0 {
		t.Errorf("a pending stop must stay off-book, %d resting", venue.Book.Len())
	}
}

func TestStopOrderRequiresStopPrice(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodPost, "/order",
		`{"trader_id":"alice","side":"BUY","type":"STOP","price":100.00,"quantity":5}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rec.Code)
	}
	if body["error"] == nil {
		t.Errorf("response should explain the missing stop price")
	}
}

func TestUnknownOrderTypeIsRejected(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodPost, "/order",
		`{"trader_id":"alice","side":"BUY","type":"TRAILING","price":100.00,"quantity":5}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", rec.Code)
	}
	if body["error"] == nil {
		t.Errorf("response should explain the rejection")
	}
}

// A stop waiting off-book still has to be cancellable, even though the order
// book itself cannot see it.
func TestPendingStopIsCancellableOverHTTP(t *testing.T) {
	router, x := newTestServer(t)
	symbol := testSymbols[0]

	crossPair(t, router, x, symbol, "buyer", "seller", 100.00, 5)

	_, placed := doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+symbol+`","trader_id":"alice","side":"BUY","type":"STOP","stop_price":150.00,"price":160.00,"quantity":5}`)

	orderID, ok := placed["order_id"].(string)
	if !ok || orderID == "" {
		t.Fatalf("no order id returned: %v", placed)
	}

	venue, _ := x.Venue(symbol)
	deadline := time.Now().Add(2 * time.Second)
	for venue.Engine.PendingStopCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	rec, body := doJSON(t, router, http.MethodDelete, "/order/"+orderID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["success"] != true {
		t.Errorf("want success, got %v", body)
	}
	if venue.Engine.PendingStopCount() != 0 {
		t.Errorf("cancelled stop should be gone, %d pending", venue.Engine.PendingStopCount())
	}
}
