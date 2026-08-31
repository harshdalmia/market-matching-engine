package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"matching-engine/internal/exchange"
	"matching-engine/internal/stream"
)

// readFrames consumes the SSE response until it has read wantFrames complete
// frames or the deadline expires, returning the raw text read so far.
//
// Counting `data:` lines rather than `event:` lines matters: data is the last
// line of a frame, so stopping on it guarantees each counted frame's payload has
// actually been read.
func readFrames(t *testing.T, body *bufio.Reader, wantFrames int, deadline time.Duration) string {
	t.Helper()

	lines := make(chan string)

	go func() {
		defer close(lines)
		for {
			line, err := body.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()

	var sb strings.Builder
	seen := 0
	timeout := time.After(deadline)

	for seen < wantFrames {
		select {
		case line, open := <-lines:
			if !open {
				return sb.String()
			}
			sb.WriteString(line)
			if strings.HasPrefix(line, "data:") {
				seen++
			}
		case <-timeout:
			return sb.String()
		}
	}

	return sb.String()
}

// serveStream runs the handler against a live server so the response can be
// read incrementally. httptest.ResponseRecorder buffers, which would never
// surface frames from a handler that only returns on disconnect.
func serveStream(t *testing.T, router http.Handler, path string) (*bufio.Reader, func()) {
	t.Helper()

	srv := httptest.NewServer(router)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connecting to stream: %v", err)
	}

	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type: want text/event-stream, got %q", got)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control: want no-cache, got %q", got)
	}

	cleanup := func() {
		cancel()
		res.Body.Close()
		srv.Close()
	}

	return bufio.NewReader(res.Body), cleanup
}

// A client must be able to render immediately, so the stream opens with a
// snapshot rather than waiting for the first market event.
func TestStreamOpensWithSnapshot(t *testing.T) {
	router, _ := newTestServer(t)

	body, cleanup := serveStream(t, router, "/stream")
	defer cleanup()

	// One snapshot frame per hosted symbol.
	text := readFrames(t, body, len(testSymbols), 3*time.Second)

	if !strings.Contains(text, "event: "+stream.EventSnapshot) {
		t.Errorf("stream should open with a snapshot, got:\n%s", text)
	}
	for _, symbol := range testSymbols {
		if !strings.Contains(text, symbol) {
			t.Errorf("snapshot missing symbol %s, got:\n%s", symbol, text)
		}
	}
}

func TestStreamPushesTrades(t *testing.T) {
	router, x := newTestServer(t)

	body, cleanup := serveStream(t, router, "/stream")
	defer cleanup()

	venue := defaultVenue(t, x)
	doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+venue.Symbol+`","trader_id":"a","side":"SELL","price":100,"quantity":5}`)
	doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+venue.Symbol+`","trader_id":"b","side":"BUY","price":100,"quantity":5}`)

	text := readFrames(t, body, len(testSymbols)+3, 5*time.Second)

	if !strings.Contains(text, "event: "+stream.EventTrade) {
		t.Errorf("a crossing pair should push a trade event, got:\n%s", text)
	}
}

func TestStreamSymbolFilter(t *testing.T) {
	router, _ := newTestServer(t)

	// testSymbols[0] is the default venue; subscribe to the other one only.
	other := testSymbols[1]

	body, cleanup := serveStream(t, router, "/stream?symbols="+other)
	defer cleanup()

	text := readFrames(t, body, 1, 3*time.Second)

	if !strings.Contains(text, other) {
		t.Errorf("filtered stream should include %s, got:\n%s", other, text)
	}
	if strings.Contains(text, testSymbols[0]) {
		t.Errorf("filtered stream leaked %s, got:\n%s", testSymbols[0], text)
	}
}

func TestStreamRejectsUnknownSymbol(t *testing.T) {
	router, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/stream?symbols=NOPE", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", rec.Code)
	}
}

// Disconnecting must release the subscriber, otherwise every dropped client
// leaks a channel that the matching loop keeps trying to publish into.
func TestStreamUnsubscribesOnDisconnect(t *testing.T) {
	x := exchange.New(testSymbols)
	broker := stream.NewBroker()
	x.SetPublisher(broker)
	x.Start()
	defer x.Stop()

	router := NewHandler(x, broker, nil, nil).NewRouter()

	body, cleanup := serveStream(t, router, "/stream")
	readFrames(t, body, len(testSymbols), 3*time.Second)

	if broker.SubscriberCount() != 1 {
		t.Fatalf("want 1 subscriber while connected, got %d", broker.SubscriberCount())
	}

	cleanup()

	// The handler returns on context cancellation; give it a moment to unwind.
	deadline := time.Now().Add(3 * time.Second)
	for broker.SubscriberCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := broker.SubscriberCount(); got != 0 {
		t.Errorf("subscriber leaked after disconnect: %d still registered", got)
	}
}

// -----------------------------------------------------------------------
// Symbol routing through the HTTP layer
// -----------------------------------------------------------------------

func TestSymbolsEndpoint(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodGet, "/symbols", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}

	symbols, ok := body["symbols"].([]any)
	if !ok || len(symbols) != len(testSymbols) {
		t.Fatalf("want %d symbols, got %v", len(testSymbols), body["symbols"])
	}
	if body["default"] != testSymbols[0] {
		t.Errorf("default: want %s, got %v", testSymbols[0], body["default"])
	}
	if _, ok := body["stats"].([]any); !ok {
		t.Errorf("symbols response should include per-instrument stats")
	}
}

func TestOrdersRouteToTheirOwnSymbol(t *testing.T) {
	router, x := newTestServer(t)

	target := testSymbols[1]
	doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"`+target+`","trader_id":"a","side":"BUY","price":100,"quantity":5}`)

	// Wait for the venue's matching loop to pick it up.
	venue, _ := x.Venue(target)
	deadline := time.Now().Add(2 * time.Second)
	for venue.Book.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if venue.Book.Len() != 1 {
		t.Errorf("%s should hold the order, got %d resting", target, venue.Book.Len())
	}

	otherVenue, _ := x.Venue(testSymbols[0])
	if otherVenue.Book.Len() != 0 {
		t.Errorf("%s must be untouched, got %d resting", testSymbols[0], otherVenue.Book.Len())
	}
}

func TestUnknownSymbolIsRejected(t *testing.T) {
	router, _ := newTestServer(t)

	rec, body := doJSON(t, router, http.MethodPost, "/order",
		`{"symbol":"NOPE","trader_id":"a","side":"BUY","price":100,"quantity":5}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", rec.Code)
	}
	if body["error"] == nil {
		t.Errorf("response should explain the unknown symbol")
	}
}

func TestReadEndpointsRejectUnknownSymbol(t *testing.T) {
	router, _ := newTestServer(t)

	for _, path := range []string{"/orderbook", "/depth", "/trades"} {
		t.Run(path, func(t *testing.T) {
			rec, _ := doJSON(t, router, http.MethodGet, path+"?symbol=NOPE", "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("status: want 404, got %d", rec.Code)
			}
		})
	}
}

// Omitting the symbol must keep working exactly as it did before multi-symbol
// support existed.
func TestOmittedSymbolUsesDefaultVenue(t *testing.T) {
	router, x := newTestServer(t)

	doJSON(t, router, http.MethodPost, "/order",
		`{"trader_id":"a","side":"BUY","price":100,"quantity":5}`)

	venue := defaultVenue(t, x)
	deadline := time.Now().Add(2 * time.Second)
	for venue.Book.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if venue.Book.Len() != 1 {
		t.Errorf("order without a symbol should land on the default venue")
	}

	rec, body := doJSON(t, router, http.MethodGet, "/depth", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
	if body["symbol"] != venue.Symbol {
		t.Errorf("depth should report the default symbol, got %v", body["symbol"])
	}
}
