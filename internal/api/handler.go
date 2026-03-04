package api

import (
	"encoding/json"
	"matching-engine/internal/engine"
	"matching-engine/internal/models"
	"matching-engine/internal/orderbook"
	"matching-engine/internal/utils"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	engine    *engine.Engine
	orderBook *orderbook.OrderBook
}

func NewHandler(e *engine.Engine, ob *orderbook.OrderBook) *Handler {
	return &Handler{engine: e, orderBook: ob}
}

// NewRouter sets up all routes
func (h *Handler) NewRouter() http.Handler {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Routes
	r.Post("/order", h.PlaceOrder)
	r.Delete("/order/{id}", h.CancelOrder)
	r.Get("/orderbook", h.GetOrderBook)
	r.Get("/trades", h.GetTrades)
	r.Get("/metrics", h.GetMetrics)

	return r
}

// PlaceOrder handles POST /order
func (h *Handler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	var req models.PlaceOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Basic validation
	if req.Side != models.SideBuy && req.Side != models.SideSell {
		writeError(w, http.StatusBadRequest, "Side must be BUY or SELL")
		return
	}
	if req.Price <= 0 {
		writeError(w, http.StatusBadRequest, "Price must be greater than 0")
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

	order := &models.Order{
		ID:        uuid.New().String(),
		TraderID:  req.TraderID,
		Side:      req.Side,
		Price:     req.Price,
		Quantity:  req.Quantity,
		Remaining: req.Quantity,
		Timestamp: time.Now().UnixNano(),
		Status:    models.StatusNew,
	}

	// Submit to engine asynchronously
	h.engine.Submit(order)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"order_id": order.ID,
		"status":   order.Status,
		"message":  "Order submitted to matching engine",
	})
}

// CancelOrder handles DELETE /order/{id}
func (h *Handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "Order ID is required")
		return
	}

	ok := h.orderBook.CancelOrder(id)
	if !ok {
		writeError(w, http.StatusNotFound, "Order not found or already completed")
		return
	}

	utils.LogOrderCancelled(id)
	writeJSON(w, http.StatusOK, models.CancelOrderResult{
		Success: true,
		Message: "Order cancelled successfully",
	})
}

// GetOrderBook handles GET /orderbook
func (h *Handler) GetOrderBook(w http.ResponseWriter, r *http.Request) {
	snapshot := h.orderBook.Snapshot()
	writeJSON(w, http.StatusOK, snapshot)
}

// GetTrades handles GET /trades
func (h *Handler) GetTrades(w http.ResponseWriter, r *http.Request) {
	trades := h.engine.GetTrades()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":  len(trades),
		"trades": trades,
	})
}

// GetMetrics handles GET /metrics
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	totalOrders, avgLatencyMs := h.engine.Metrics()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_orders_processed": totalOrders,
		"avg_latency_ms":         avgLatencyMs,
	})
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
