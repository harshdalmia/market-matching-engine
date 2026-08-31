package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"matching-engine/internal/exchange"
	"matching-engine/internal/stream"
)

const (
	// How often conflated depth snapshots are considered for publication.
	bookTickInterval = 120 * time.Millisecond

	// Comment frames keep intermediaries from timing the connection out during
	// quiet periods.
	heartbeatInterval = 15 * time.Second

	// Depth levels included in streamed book snapshots.
	streamDepthLevels = 15
)

// venueWatermark tracks the last state we published depth for, so unchanged
// books are not re-serialised on every tick.
type venueWatermark struct {
	bookVersion uint64
	ordersSeen  int64
}

// Stream handles GET /stream — a Server-Sent Events feed of trades, order
// updates and conflated depth snapshots.
//
// Trades and order updates are pushed from the matching loop as they happen.
// Depth is conflated on a ticker instead: serialising a full book on every fill
// would put JSON encoding directly on the matching path, and a client cannot
// render faster than a few frames per second anyway.
//
// Query parameters:
//
//	symbols=AAPL,MSFT   restrict to these instruments (default: all)
//	depth=0             disable conflated book snapshots
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "Streaming unsupported by this server")
		return
	}

	symbols, err := h.parseStreamSymbols(r.URL.Query().Get("symbols"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	includeBook := r.URL.Query().Get("depth") != "0"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Defeats proxy buffering, which would otherwise hold frames back.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := h.broker.Subscribe(symbols)
	defer sub.Close()

	// Open with a snapshot so a client can render immediately rather than
	// waiting for the first event to arrive.
	h.writeSnapshot(w, symbols)
	flusher.Flush()

	bookTicker := time.NewTicker(bookTickInterval)
	defer bookTicker.Stop()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	watermarks := make(map[string]venueWatermark, len(symbols))
	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return // client disconnected

		case event, open := <-sub.Events():
			if !open {
				return
			}
			if err := writeSSE(w, event.Seq, event.Type, event); err != nil {
				return
			}
			flusher.Flush()

		case <-bookTicker.C:
			if !includeBook {
				continue
			}
			if h.writeChangedBooks(w, symbols, watermarks) {
				flusher.Flush()
			}

		case <-heartbeat.C:
			// A comment frame is invisible to EventSource consumers but keeps
			// the socket alive. Dropped counts ride along for observability.
			if _, err := fmt.Fprintf(w, ": heartbeat dropped=%d\n\n", sub.Dropped()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// parseStreamSymbols validates the requested symbol filter. An empty request
// means every symbol on the exchange.
func (h *Handler) parseStreamSymbols(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return h.exchange.Symbols(), nil
	}

	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		symbol := exchange.NormalizeSymbol(part)
		if symbol == "" {
			continue
		}
		if _, ok := h.exchange.Venue(symbol); !ok {
			return nil, fmt.Errorf("Unknown symbol: %s", symbol)
		}
		out = append(out, symbol)
	}

	if len(out) == 0 {
		return h.exchange.Symbols(), nil
	}
	return out, nil
}

// writeSnapshot sends the current depth for each requested symbol.
func (h *Handler) writeSnapshot(w http.ResponseWriter, symbols []string) {
	for _, symbol := range symbols {
		venue, ok := h.exchange.Venue(symbol)
		if !ok {
			continue
		}

		depth := venue.Book.Depth(streamDepthLevels)
		depth.Symbol = symbol

		event := stream.Event{
			Type:   stream.EventSnapshot,
			Symbol: symbol,
			Time:   time.Now().UnixNano(),
			Data:   depth,
		}
		if err := writeSSE(w, 0, stream.EventSnapshot, event); err != nil {
			return
		}
	}
}

// writeChangedBooks emits depth for any venue that has changed since the last
// tick. Returns whether anything was written.
func (h *Handler) writeChangedBooks(
	w http.ResponseWriter,
	symbols []string,
	watermarks map[string]venueWatermark,
) bool {
	wrote := false

	for _, symbol := range symbols {
		venue, ok := h.exchange.Venue(symbol)
		if !ok {
			continue
		}

		// Book version catches structural changes (adds, pops, cancels);
		// processed-order count catches partial fills, which mutate an order in
		// place without touching the heap. Together they cover every mutation.
		matched, _ := venue.Engine.Metrics()
		current := venueWatermark{
			bookVersion: venue.Book.Version(),
			ordersSeen:  matched,
		}

		if previous, seen := watermarks[symbol]; seen && previous == current {
			continue
		}
		watermarks[symbol] = current

		depth := venue.Book.Depth(streamDepthLevels)
		depth.Symbol = symbol

		event := stream.Event{
			Type:   stream.EventBook,
			Symbol: symbol,
			Time:   time.Now().UnixNano(),
			Data:   depth,
		}
		if err := writeSSE(w, 0, stream.EventBook, event); err != nil {
			return wrote
		}
		wrote = true
	}

	return wrote
}

// writeSSE emits one Server-Sent Event frame.
func writeSSE(w http.ResponseWriter, seq uint64, eventType string, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", seq); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", encoded)
	return err
}
