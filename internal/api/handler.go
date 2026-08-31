package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"matching-engine/internal/accounts"
	"matching-engine/internal/exchange"
	"matching-engine/internal/marketdata"
	"matching-engine/internal/models"
	"matching-engine/internal/stream"
	"matching-engine/internal/utils"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
)

// Default and maximum sizes for the paged/limited endpoints.
const (
	defaultDepthLevels = 15
	maxDepthLevels     = 100
	defaultTradeLimit  = 500
	maxTradeLimit      = 5000
	defaultCandleLimit = 200
	maxCandleLimit     = 1000

	// 24 hours, matching the design's "24H CHANGE" readout.
	defaultStatsWindowSecs = 24 * 60 * 60
	maxStatsWindowSecs     = 7 * 24 * 60 * 60
)

// Recorder durably records accepted commands before they reach the engine.
// A nil recorder disables logging.
type Recorder interface {
	AppendOrder(*models.Order) error
	AppendCancel(symbol, orderID string) error
}

type Handler struct {
	exchange       *exchange.Exchange
	broker         *stream.Broker
	accounts       *accounts.Registry
	recorder       Recorder
	allowedOrigins []string
	startedAt      time.Time
}

// SetRecorder attaches a write-ahead log. Call before serving traffic.
func (h *Handler) SetRecorder(r Recorder) {
	h.recorder = r
}

// NewHandler builds the HTTP handler. allowedOrigins is the CORS allow-list;
// pass a single "*" (or nothing) to allow any origin. broker may be nil, in
// which case /stream serves snapshots and heartbeats but no live events.
func NewHandler(
	x *exchange.Exchange,
	broker *stream.Broker,
	registry *accounts.Registry,
	allowedOrigins []string,
) *Handler {
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"*"}
	}
	if broker == nil {
		broker = stream.NewBroker()
	}
	// A nil registry would make every account endpoint a nil dereference; an
	// untracked one at least answers coherently with empty accounts.
	if registry == nil {
		registry = accounts.New(0, 0, 0)
	}
	return &Handler{
		exchange:       x,
		broker:         broker,
		accounts:       registry,
		allowedOrigins: allowedOrigins,
		startedAt:      time.Now(),
	}
}

// NewRouter sets up all routes
func (h *Handler) NewRouter() http.Handler {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// CORS has to run before routing so that preflight OPTIONS requests are
	// answered for DELETE /order/{id}, which no route handler would match.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   h.allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "X-Requested-With", "Last-Event-ID"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Routes
	r.Get("/health", h.Health)
	r.Get("/symbols", h.GetSymbols)
	r.Post("/order", h.PlaceOrder)
	r.Delete("/order/{id}", h.CancelOrder)
	r.Get("/orderbook", h.GetOrderBook)
	r.Get("/depth", h.GetDepth)
	r.Get("/trades", h.GetTrades)
	r.Get("/stats", h.GetStats)
	r.Get("/candles", h.GetCandles)
	r.Get("/orders", h.GetTraderOrders)
	r.Get("/account", h.GetAccount)
	r.Get("/metrics", h.GetMetrics)
	r.Get("/stream", h.Stream)

	return r
}

// GetStats handles GET /stats?symbol=&window= — rolling market statistics.
//
// window is in seconds and defaults to 24 hours, matching the design's
// "24H CHANGE" field.
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	venue, ok := h.resolveVenue(w, r)
	if !ok {
		return
	}

	windowSeconds := queryInt(r, "window", defaultStatsWindowSecs, maxStatsWindowSecs)
	if windowSeconds <= 0 {
		windowSeconds = defaultStatsWindowSecs
	}

	stats := marketdata.Summarise(
		venue.Symbol,
		venue.Engine.GetTrades(0),
		time.Duration(windowSeconds)*time.Second,
		time.Now().UnixNano(),
	)

	writeJSON(w, http.StatusOK, stats)
}

// GetCandles handles GET /candles?symbol=&interval=&limit= — OHLC buckets.
func (h *Handler) GetCandles(w http.ResponseWriter, r *http.Request) {
	venue, ok := h.resolveVenue(w, r)
	if !ok {
		return
	}

	label := r.URL.Query().Get("interval")
	if label == "" {
		label = marketdata.DefaultInterval
	}

	interval, valid := marketdata.ParseInterval(label)
	if !valid {
		writeError(w, http.StatusBadRequest,
			"Unsupported interval: "+label+" (supported: "+strings.Join(marketdata.Intervals(), ", ")+")")
		return
	}

	limit := queryInt(r, "limit", defaultCandleLimit, maxCandleLimit)

	writeJSON(w, http.StatusOK, marketdata.CandleSeries{
		Symbol:   venue.Symbol,
		Interval: label,
		Candles:  marketdata.Candles(venue.Engine.GetTrades(0), interval, limit),
	})
}

// GetTraderOrders handles GET /orders?trader_id=&symbol= — working and finished
// orders for one trader.
func (h *Handler) GetTraderOrders(w http.ResponseWriter, r *http.Request) {
	traderID := strings.TrimSpace(r.URL.Query().Get("trader_id"))
	if traderID == "" {
		writeError(w, http.StatusBadRequest, "trader_id is required")
		return
	}

	// An optional symbol filter still has to name a real instrument.
	symbol := ""
	if raw := r.URL.Query().Get("symbol"); raw != "" {
		venue, ok := h.exchange.Resolve(raw)
		if !ok {
			writeError(w, http.StatusNotFound, "Unknown symbol: "+raw)
			return
		}
		symbol = venue.Symbol
	}

	writeJSON(w, http.StatusOK, h.accounts.Orders(traderID, symbol))
}

// GetAccount handles GET /account?trader_id= — cash, positions and equity.
func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	traderID := strings.TrimSpace(r.URL.Query().Get("trader_id"))
	if traderID == "" {
		writeError(w, http.StatusBadRequest, "trader_id is required")
		return
	}

	// Positions are valued against the exchange's current marks.
	writeJSON(w, http.StatusOK, h.accounts.Snapshot(traderID, h.exchange.Marks()))
}

// Health handles GET /health for load balancer and uptime probes.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	_, _, queued, resting, trades := h.exchange.Totals()

	writeJSON(w, http.StatusOK, models.HealthResponse{
		Status:        "ok",
		UptimeSeconds: time.Since(h.startedAt).Seconds(),
		OrdersQueued:  queued,
		RestingOrders: resting,
		TradeCount:    trades,
		Symbols:       h.exchange.Symbols(),
	})
}

// GetSymbols handles GET /symbols — the tradable instruments and a summary of each.
func (h *Handler) GetSymbols(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"symbols": h.exchange.Symbols(),
		"default": h.exchange.DefaultSymbol(),
		"stats":   h.exchange.AllStats(),
	})
}

// PlaceOrder handles POST /order
func (h *Handler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	var req models.PlaceOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// An omitted symbol routes to the default venue, so clients written against
	// the single-instrument API keep working.
	venue, ok := h.exchange.Resolve(req.Symbol)
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown symbol: "+req.Symbol)
		return
	}

	if req.Side != models.SideBuy && req.Side != models.SideSell {
		writeError(w, http.StatusBadRequest, "Side must be BUY or SELL")
		return
	}

	// Type and time-in-force are optional; empty means LIMIT / GTC.
	orderType := req.Type
	if orderType == "" {
		orderType = models.TypeLimit
	}
	if orderType != models.TypeLimit &&
		orderType != models.TypeMarket &&
		orderType != models.TypeStop {
		writeError(w, http.StatusBadRequest, "Type must be LIMIT, MARKET or STOP")
		return
	}

	tif := req.TimeInForce
	if tif == "" {
		tif = models.TIFGTC
	}
	if tif != models.TIFGTC && tif != models.TIFIOC && tif != models.TIFFOK {
		writeError(w, http.StatusBadRequest, "TimeInForce must be GTC, IOC or FOK")
		return
	}

	// A market order has no limit, so a price is neither required nor used.
	if orderType == models.TypeLimit && req.Price <= 0 {
		writeError(w, http.StatusBadRequest, "Price must be greater than 0")
		return
	}
	// A stop needs a trigger. Its price is optional: supplied makes it a
	// stop-limit, omitted makes it a stop-market.
	if orderType == models.TypeStop && req.StopPrice <= 0 {
		writeError(w, http.StatusBadRequest, "StopPrice must be greater than 0 for STOP orders")
		return
	}
	if req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "Quantity must be greater than 0")
		return
	}
	if req.TraderID == "" {
		writeError(w, http.StatusBadRequest, "TraderID is required")
		return
	}

	price := req.Price
	if orderType == models.TypeMarket {
		price = 0
	}

	stopPrice := 0.0
	if orderType == models.TypeStop {
		stopPrice = req.StopPrice
	}

	order := &models.Order{
		ID:          uuid.New().String(),
		Symbol:      venue.Symbol,
		TraderID:    req.TraderID,
		Side:        req.Side,
		Type:        orderType,
		TimeInForce: tif,
		Price:       price,
		StopPrice:   stopPrice,
		Quantity:    req.Quantity,
		Remaining:   req.Quantity,
		Timestamp:   time.Now().UnixNano(),
		Status:      models.StatusNew,
	}

	// Record before matching. If the order were submitted first, a crash in
	// between would leave a trade that replay could never reproduce. Failing the
	// request instead is the honest outcome: an order that cannot be durably
	// recorded has not really been accepted.
	if h.recorder != nil {
		if err := h.recorder.AppendOrder(order); err != nil {
			utils.LogError("wal append failed", err)
			writeError(w, http.StatusInternalServerError, "Could not durably record order")
			return
		}
	}

	// Submit to the venue's engine asynchronously. A saturated intake buffer is
	// reported rather than waited on, so clients get a fast, retryable failure.
	if !venue.Engine.Submit(order) {
		writeError(w, http.StatusServiceUnavailable, "Engine is saturated, retry shortly")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"order_id":      order.ID,
		"symbol":        order.Symbol,
		"status":        models.StatusNew,
		"type":          order.Type,
		"time_in_force": order.TimeInForce,
		"stop_price":    order.StopPrice,
		"message":       "Order submitted to matching engine",
	})
}

// CancelOrder handles DELETE /order/{id}
func (h *Handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "Order ID is required")
		return
	}

	// Order IDs are UUIDs, so the symbol does not need to be supplied.
	symbol, ok := h.exchange.CancelOrder(id)
	if !ok {
		writeError(w, http.StatusNotFound, "Order not found or already completed")
		return
	}

	// Logged after the fact: a cancel that did not apply is not a state change,
	// and replaying a no-op cancel would be harmless but misleading.
	if h.recorder != nil {
		if err := h.recorder.AppendCancel(symbol, id); err != nil {
			utils.LogError("wal append failed", err)
		}
	}

	utils.LogOrderCancelled(id)
	writeJSON(w, http.StatusOK, models.CancelOrderResult{
		Success: true,
		Symbol:  symbol,
		Message: "Order cancelled successfully",
	})
}

// GetOrderBook handles GET /orderbook?symbol=
func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	venue, ok := h.resolveVenue(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, venue.Book.Snapshot())
}

// GetDepth handles GET /depth?symbol=&levels=N — the aggregated book.
func (h *Handler) GetDepth(w http.ResponseWriter, r *http.Request) {
	venue, ok := h.resolveVenue(w, r)
	if !ok {
		return
	}

	levels := queryInt(r, "levels", defaultDepthLevels, maxDepthLevels)
	depth := venue.Book.Depth(levels)
	depth.Symbol = venue.Symbol

	writeJSON(w, http.StatusOK, depth)
}

// GetTrades handles GET /trades?symbol=&limit=N. limit=0 returns the full
// retained tape for that symbol.
func (h *Handler) GetTrades(w http.ResponseWriter, r *http.Request) {
	venue, ok := h.resolveVenue(w, r)
	if !ok {
		return
	}

	limit := queryInt(r, "limit", defaultTradeLimit, maxTradeLimit)
	trades := venue.Engine.GetTrades(limit)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"symbol": venue.Symbol,
		"count":  len(trades),
		"total":  venue.Engine.TradeCount(),
		"trades": trades,
	})
}

// GetMetrics handles GET /metrics — aggregate counters plus a per-symbol breakdown.
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	matched, avgLatencyMs, queued, resting, trades := h.exchange.Totals()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_orders_processed": matched,
		"avg_latency_ms":         avgLatencyMs,
		"orders_queued":          queued,
		"resting_orders":         resting,
		"trade_count":            trades,
		"stream_subscribers":     h.broker.SubscriberCount(),
		"symbols":                h.exchange.AllStats(),
	})
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// resolveVenue reads the optional ?symbol= parameter and writes a 404 if it
// names an instrument this exchange does not host.
func (h *Handler) resolveVenue(w http.ResponseWriter, r *http.Request) (*exchange.Venue, bool) {
	requested := r.URL.Query().Get("symbol")

	venue, ok := h.exchange.Resolve(requested)
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown symbol: "+requested)
		return nil, false
	}
	return venue, true
}

// queryInt reads a non-negative integer query param, falling back to def when
// absent or unparseable and clamping to max. Zero is preserved as "no limit".
func queryInt(r *http.Request, name string, def, max int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
