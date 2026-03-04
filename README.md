# Matching Engine

A low-latency order matching engine built in Go.  
Implements price-time priority, partial fills, and concurrent order processing via goroutines and channels.

---

## Features

- **Price-Time Priority** — Best price first, FIFO within same price
- **Partial Fills** — Orders match across multiple counterparties
- **Order States** — NEW → PARTIALLY_FILLED → FILLED / CANCELLED / REJECTED
- **Concurrent** — Orders flow through a buffered channel into a single matching goroutine (no race conditions)
- **Stress Test** — Generates 10,000 random orders to benchmark throughput

---

## Project Structure

```
matching-engine/
├── cmd/
│   └── main.go               # Entrypoint
├── internal/
│   ├── api/
│   │   └── handler.go        # HTTP handlers (chi router)
│   ├── engine/
│   │   ├── engine.go         # Matching logic + goroutine loop
│   │   └── generator.go      # Stress test order generator
│   ├── orderbook/
│   │   └── orderbook.go      # Min/Max heap order book
│   ├── models/
│   │   └── models.go         # Order, Trade, request/response structs
│   └── utils/
│       └── logger.go         # Structured logging
├── go.mod
└── README.md
```

---

## Setup & Run

### Prerequisites

- Go 1.21+ → https://go.dev/dl/

### Install & Run

```bash
# 1. Clone / enter the project
cd matching-engine

# 2. Download dependencies
go mod tidy

# 3. Run the server
go run cmd/main.go

# Server starts on http://localhost:8080
```

### Run with stress test first

```bash
go run cmd/main.go -stress -n 10000
```

### Custom port

```bash
go run cmd/main.go -port 9090
```

### Build binary

```bash
go build -o engine cmd/main.go
./engine -stress
```

---

## API Reference

### Place Order

```
POST /order
Content-Type: application/json

{
  "trader_id": "trader-1",
  "side": "BUY",
  "price": 100.50,
  "quantity": 10
}
```

**Response:**
```json
{
  "order_id": "uuid-here",
  "status": "NEW",
  "message": "Order submitted to matching engine"
}
```

---

### Cancel Order

```
DELETE /order/{id}
```

**Response:**
```json
{
  "success": true,
  "message": "Order cancelled successfully"
}
```

---

### Get Order Book

```
GET /orderbook
```

**Response:**
```json
{
  "bids": [ ... ],  // sorted: highest price first
  "asks": [ ... ]   // sorted: lowest price first
}
```

---

### Get Trades

```
GET /trades
```

**Response:**
```json
{
  "count": 42,
  "trades": [ ... ]
}
```

---

### Get Metrics

```
GET /metrics
```

**Response:**
```json
{
  "total_orders_processed": 10000,
  "avg_latency_ms": 0.043
}
```

---

## Quick Test with curl

```bash
# Place a SELL order
curl -X POST http://localhost:8080/order \
  -H "Content-Type: application/json" \
  -d '{"trader_id":"alice","side":"SELL","price":100.00,"quantity":5}'

# Place a BUY order that matches
curl -X POST http://localhost:8080/order \
  -H "Content-Type: application/json" \
  -d '{"trader_id":"bob","side":"BUY","price":100.00,"quantity":3}'

# View trades
curl http://localhost:8080/trades

# View order book
curl http://localhost:8080/orderbook

# View metrics
curl http://localhost:8080/metrics
```

---

## Design Decisions

| Decision | Why |
|---|---|
| `container/heap` for order book | O(log n) insert/remove, correct price-time priority |
| Buffered channel (1000) | Decouples HTTP layer from matching engine, backpressure |
| Single matching goroutine | Avoids all locking complexity in the matching logic itself |
| Lazy deletion for cancels | Heap doesn't support O(log n) arbitrary removal; cancelled orders are skipped during matching |
| Trade price = ask price | Resting order's price is used (standard exchange behavior) |
