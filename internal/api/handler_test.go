package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"matching-engine/internal/accounts"
	"matching-engine/internal/exchange"
	"matching-engine/internal/models"
	"matching-engine/internal/stream"
)

// testSymbols are the instruments every test exchange hosts. The first in sorted
// order is the default venue, which is what requests without a symbol hit.
var testSymbols = []string{"AAA", "BBB"}

// testStartingCash is a round number so ledger assertions are easy to read.
const testStartingCash = 100_000.0

// newTestServer wires a live multi-symbol exchange behind the real router.
func newTestServer(t *testing.T, origins ...string) (http.Handler, *exchange.Exchange) {
	t.Helper()

	x := exchange.New(testSymbols)

	// The engines must be wired to the same broker the handler serves from,
	// otherwise /stream registers subscribers that never receive anything.
	broker := stream.NewBroker()
	x.SetPublisher(broker)

	// Likewise the registry: without it the account endpoints see no fills.
	registry := accounts.New(testStartingCash, 0, 0)
	x.SetObserver(registry)

	x.Start()
	t.Cleanup(x.Stop)

	h := NewHandler(x, broker, registry, origins)
	return h.NewRouter(), x
}

// defaultVenue returns the venue that symbol-less requests route to.
func defaultVenue(t *testing.T, x *exchange.Exchange) *exchange.Venue {
	t.Helper()

	v, ok := x.Resolve("")
	if !ok {
		t.Fatalf("exchange has no default venue")
	}
	return v
}

func doJSON(t *testing.T, router http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s: response is not valid JSON: %v (body %q)", method, path, err, rec.Body.String())
		}
	}
	return rec, decoded
}

// -----------------------------------------------------------------------
// Validation
// -----------------------------------------------------------------------

func TestPlaceOrderValidation(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{
			name:     "malformed json",
			body:     `{not json`,
			wantCode: http.StatusBadRequest,
			wantErr:  "Invalid request body",
		},
		{
			name:     "bad side",
			body:     `{"trader_id":"a","side":"HOLD","price":100,"quantity":1}`,
			wantCode: http.StatusBadRequest,
			wantErr:  "Side must be BUY or SELL",
		},
		{
			name:     "zero price",
			body:     `{"trader_id":"a","side":"BUY","price":0,"quantity":1}`,
			wantCode: http.StatusBadRequest,
			wantErr:  "Price must be greater than 0",
		},
		{
			name:     "negative price",
			body:     `{"trader_id":"a","side":"BUY","price":-5,"quantity":1}`,
			wantCode: http.StatusBadRequest,
			wantErr:  "Price must be greater than 0",
		},
		{
			name:     "zero quantity",
			body:     `{"trader_id":"a","side":"BUY","price":100,"quantity":0}`,
			wantCode: http.StatusBadRequest,
			wantErr:  "Quantity must be greater than 0",
		},
		{
			name:     "missing trader id",
			body:     `{"trader_id":"","side":"BUY","price":100,"quantity":1}`,
			wantCode: http.StatusBadRequest,
			wantErr:  "TraderID is required",
		},
	}

	router, _ := newTestServer(t)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := doJSON(t, router, http.MethodPost, "/order", tc.body)

			if rec.Code != tc.wantCode {
				t.Errorf("status: want %d, got %d", tc.wantCode, rec.Code)
			}
			if got := body["error"]; got != tc.wantErr {
				t.Errorf("error: want %q, got %q", tc.wantErr, got)
			}
		})
	}
}

func TestPlaceOrderSuccess(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodPost, "/order",
		`{"trader_id":"alice","side":"BUY","price":100.5,"quantity":10}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: want 201, got %d", rec.Code)
	}
	if body["order_id"] == "" || body["order_id"] == nil {
		t.Errorf("response must carry a generated order_id")
	}
	// The engine acknowledges before matching, so this is always NEW.
	if body["status"] != models.StatusNew {
		t.Errorf("status field: want NEW, got %v", body["status"])
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
}

// A saturated intake buffer must shed load rather than block the request.
func TestPlaceOrderShedsLoadWhenSaturated(t *testing.T) {
	// Deliberately never started: nothing drains the intake channel.
	x := exchange.New(testSymbols)
	router := NewHandler(x, nil, nil, nil).NewRouter()

	venue := defaultVenue(t, x)

	// Fill the default venue's buffer.
	for {
		if !venue.Engine.Submit(&models.Order{
			ID: "filler", Symbol: venue.Symbol, TraderID: "x", Side: models.SideBuy,
			Price: 100, Quantity: 1, Remaining: 1, Status: models.StatusNew,
		}) {
			break
		}
	}

	done := make(chan struct{})
	var rec *httptest.ResponseRecorder
	go func() {
		rec, _ = doJSON(t, router, http.MethodPost, "/order",
			`{"trader_id":"a","side":"BUY","price":100,"quantity":1}`)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("PlaceOrder blocked on a saturated engine instead of shedding load")
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d", rec.Code)
	}
}

// -----------------------------------------------------------------------
// Cancel
// -----------------------------------------------------------------------

func TestCancelOrder(t *testing.T) {
	router, x := newTestServer(t)
	venue := defaultVenue(t, x)

	venue.Book.AddOrder(&models.Order{
		ID: "resting-1", Symbol: venue.Symbol, TraderID: "alice", Side: models.SideBuy,
		Price: 100, Quantity: 5, Remaining: 5,
		Timestamp: time.Now().UnixNano(), Status: models.StatusNew,
	})

	rec, body := doJSON(t, router, http.MethodDelete, "/order/resting-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["success"] != true {
		t.Errorf("want success true, got %v", body["success"])
	}
}

func TestCancelUnknownOrderReturns404(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodDelete, "/order/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", rec.Code)
	}
	// The failure envelope differs in shape from the success body, so clients
	// must branch on status rather than on a success field.
	if body["error"] == nil {
		t.Errorf("404 body should carry an error message, got %v", body)
	}
}

// -----------------------------------------------------------------------
// Read endpoints
// -----------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["status"] != "ok" {
		t.Errorf("want status ok, got %v", body["status"])
	}
	for _, field := range []string{"uptime_seconds", "orders_queued", "resting_orders", "trade_count"} {
		if _, ok := body[field]; !ok {
			t.Errorf("health response missing %q", field)
		}
	}
}

func TestDepthEndpoint(t *testing.T) {
	router, x := newTestServer(t)
	venue := defaultVenue(t, x)

	for i, price := range []float64{99.0, 98.0, 97.0} {
		venue.Book.AddOrder(&models.Order{
			ID: string(rune('a' + i)), Symbol: venue.Symbol, TraderID: "t", Side: models.SideBuy,
			Price: price, Quantity: 5, Remaining: 5,
			Timestamp: int64(i), Status: models.StatusNew,
		})
	}

	rec, body := doJSON(t, router, http.MethodGet, "/depth?levels=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	bids, ok := body["bids"].([]any)
	if !ok {
		t.Fatalf("bids should be an array, got %T", body["bids"])
	}
	if len(bids) != 2 {
		t.Errorf("levels=2 should cap the response at 2 levels, got %d", len(bids))
	}
	if body["best_bid"] != 99.0 {
		t.Errorf("best_bid: want 99, got %v", body["best_bid"])
	}
	// One-sided book: ask-derived fields must be explicitly null.
	if body["best_ask"] != nil || body["spread"] != nil {
		t.Errorf("ask-side fields should be null on a one-sided book")
	}
}

func TestTradesEndpointLimit(t *testing.T) {
	router, x := newTestServer(t)
	eng := defaultVenue(t, x).Engine

	// Five crossing pairs produce five trades.
	for i := 0; i < 5; i++ {
		doJSON(t, router, http.MethodPost, "/order",
			`{"trader_id":"a","side":"SELL","price":100,"quantity":1}`)
		doJSON(t, router, http.MethodPost, "/order",
			`{"trader_id":"b","side":"BUY","price":100,"quantity":1}`)
	}

	// Give the matching goroutine a moment to drain the channel.
	deadline := time.Now().Add(2 * time.Second)
	for eng.TradeCount() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	rec, body := doJSON(t, router, http.MethodGet, "/trades?limit=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["count"] != float64(2) {
		t.Errorf("count: want 2, got %v", body["count"])
	}
	if body["total"] != float64(5) {
		t.Errorf("total should report the full retained tape, got %v", body["total"])
	}
}

func TestMetricsEndpoint(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	for _, field := range []string{
		"total_orders_processed", "avg_latency_ms",
		"orders_queued", "resting_orders", "trade_count",
	} {
		if _, ok := body[field]; !ok {
			t.Errorf("metrics response missing %q", field)
		}
	}
}

// -----------------------------------------------------------------------
// CORS
// -----------------------------------------------------------------------

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	router, _ := newTestServer(t, "https://app.example.com")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("allowed origin should be echoed back, got %q", got)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	router, _ := newTestServer(t, "https://app.example.com")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unknown origin must not receive an allow header, got %q", got)
	}
}

func TestCORSDefaultsToWildcard(t *testing.T) {
	// No origins configured: NewHandler falls back to "*".
	router, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("want wildcard by default, got %q", got)
	}
}

// DELETE from a browser triggers a preflight that no route handler would match,
// so CORS middleware has to answer it before routing.
func TestCORSPreflightForDelete(t *testing.T) {
	router, _ := newTestServer(t, "https://app.example.com")

	req := httptest.NewRequest(http.MethodOptions, "/order/some-id", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "DELETE")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("preflight should succeed, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
		t.Errorf("preflight must allow DELETE, got %q", rec.Header().Get("Access-Control-Allow-Methods"))
	}
}
