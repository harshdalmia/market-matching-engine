# Matching Engine

A low-latency order matching engine built in Go.  
Implements price-time priority, partial fills, and concurrent order processing via goroutines and channels.

---

## Features

- **Price-Time Priority** — Best price first, FIFO within same price
- **Maker Pricing** — Trades print at the resting order's price, whichever side is the aggressor
- **Order Types** — `LIMIT`, `MARKET` and `STOP` (stop-limit or stop-market)
- **Time in Force** — `GTC` (rests), `IOC` (fill and cancel the rest), `FOK` (all or nothing)
- **Positions & Ledger** — Average-cost positions with realised/unrealised PnL and a cash balance per trader
- **Market Statistics** — Rolling-window open/high/low/change/volume, plus OHLC candles at five intervals
- **Partial Fills** — Orders match across multiple counterparties
- **Order States** — NEW → PARTIALLY_FILLED → FILLED / CANCELLED / REJECTED
- **Multi-Symbol** — One order book *and* one matching goroutine per instrument, fully isolated
- **Live Event Stream** — Server-Sent Events feed of trades, order updates and conflated depth
- **Crash Recovery** — Optional append-only write-ahead log, replayed on startup
- **Concurrent** — Orders flow through a buffered channel into a single matching goroutine per symbol (no race conditions)
- **Backpressure** — A saturated intake buffer returns `503` instead of blocking the request
- **Aggregated Depth** — `/depth` returns price levels with cumulative totals, not raw orders
- **Web Terminal** — Next.js + TypeScript frontend in `web/`, driven by the event stream
- **Tested** — 135 tests including property tests and a replay-equivalence test
- **Stress Test** — Generates 10,000 random orders to benchmark throughput

---

## Project Structure

```
matching-engine/
├── cmd/
│   └── main.go               # Entrypoint
├── internal/
│   ├── api/
│   │   ├── handler.go        # HTTP handlers (chi router + CORS)
│   │   └── stream.go         # Server-Sent Events endpoint
│   ├── accounts/
│   │   └── accounts.go       # Positions, cash ledger, order history
│   ├── exchange/
│   │   └── exchange.go       # One book + matching goroutine per symbol
│   ├── marketdata/
│   │   └── marketdata.go     # OHLC candles + rolling statistics
│   ├── engine/
│   │   ├── engine.go         # Matching logic + goroutine loop
│   │   └── generator.go      # Stress test order generator
│   ├── orderbook/
│   │   └── orderbook.go      # Min/Max heap order book + depth aggregation
│   ├── stream/
│   │   └── broker.go         # Non-blocking event fan-out
│   ├── wal/
│   │   └── wal.go            # Append-only log + replay
│   ├── models/
│   │   └── models.go         # Order, Trade, request/response structs
│   └── utils/
│       └── logger.go         # Structured logging
├── web/                      # Next.js trading terminal (see "Frontend")
│   ├── app/
│   ├── components/
│   └── lib/
├── Dockerfile                # Static Go binary on distroless
├── fly.toml                  # Single-instance deployment config
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

### Configuration

Flags take their defaults from the environment, so the same binary works locally
and on hosts that inject configuration.

| Variable | Flag | Default | Purpose |
|---|---|---|---|
| `PORT` | `-port` | `8080` | HTTP listen port |
| `ALLOWED_ORIGINS` | `-origins` | *(any origin)* | Comma-separated CORS allow-list |
| `SYMBOLS` | `-symbols` | `BTC/USD,ETH/USD,AAPL` | Instruments to host, one venue each |
| `WAL_PATH` | `-wal` | *(disabled)* | Write-ahead log path for crash recovery |
| `STARTING_CASH` | `-cash` | `100000` | Opening cash balance for every trader account |

```bash
# Two instruments, CORS pinned to one frontend, durable recovery enabled
SYMBOLS="AAPL,BTC-USD" \
ALLOWED_ORIGINS="https://your-frontend.vercel.app" \
WAL_PATH="./data/engine.wal" \
  go run cmd/main.go
```

Leaving `ALLOWED_ORIGINS` unset allows any origin. That is reasonable here
because the API has no authentication — anyone who can reach it can already
place orders — but it is worth pinning once deployed.

### Testing

```bash
go test ./...          # 192 tests
go test -race ./...    # requires a working 64-bit cgo toolchain
```

The suite is not only example-based. Alongside table-driven matching cases there
are property tests that hold for *any* order flow — quantity is conserved, the
book is never crossed, fills never exceed the submitted quantity, prints always
land inside the crossing band — plus a replay test asserting that a fresh engine
rebuilt from the write-ahead log is byte-for-byte equivalent to the original.

### Build binary

```bash
go build -o engine cmd/main.go
./engine -stress
```

---

## API Reference

All responses are `application/json`. Failures return a flat `{"error": "..."}`
envelope with an appropriate status code.

### Health

```
GET /health
```

**Response:**
```json
{
  "status": "ok",
  "uptime_seconds": 412.8,
  "orders_queued": 0,
  "resting_orders": 534,
  "trade_count": 2095
}
```

Intended for load balancer and uptime probes.

---

### Symbols

```
GET /symbols
```

**Response:**
```json
{
  "symbols": ["AAPL", "BTC-USD"],
  "default": "AAPL",
  "stats": [
    {
      "symbol": "AAPL",
      "best_bid": 99.00,
      "best_ask": 100.00,
      "spread": 1.00,
      "mid": 99.50,
      "last_price": 99.00,
      "resting_orders": 7,
      "trade_count": 6,
      "orders_queued": 0,
      "orders_matched": 14,
      "avg_latency_ms": 0.031
    }
  ]
}
```

Every read endpoint accepts an optional `?symbol=`. Omitting it uses `default`.
An unknown symbol returns `404`.

---

### Event Stream

```
GET /stream?symbols=AAPL,BTC-USD
```

A Server-Sent Events feed. Omitting `symbols` subscribes to everything; adding
`depth=0` suppresses book frames.

| Event | Payload | When |
|---|---|---|
| `snapshot` | `DepthSnapshot` | Once per symbol on connect, so a client can render immediately |
| `trade` | `Trade` | Pushed from the matching loop as each print happens |
| `order` | `Order` | Pushed on every order state change |
| `book` | `DepthSnapshot` | Conflated depth, at most every 120ms and only when changed |

```
event: trade
data: {"seq":10,"type":"trade","symbol":"AAPL","time":1788096380538188400,
       "data":{"id":"cbac...","symbol":"AAPL","price":99,"quantity":1,...}}
```

Two deliberate design choices:

- **Trades and order updates are event-driven; depth is conflated.** Serialising
  a full book on every fill would put JSON encoding directly on the matching
  path, and no client can render faster than a few frames per second anyway.
- **A slow consumer is dropped, not waited for.** Each subscriber has a bounded
  buffer and publishing uses a non-blocking send. A client that stops reading has
  events discarded and counted rather than being allowed to apply backpressure to
  matching. The drop count rides along on the heartbeat comment frames.

---

### Place Order

```
POST /order
Content-Type: application/json

{
  "symbol": "AAPL",
  "trader_id": "trader-1",
  "side": "BUY",
  "type": "LIMIT",
  "time_in_force": "GTC",
  "price": 100.50,
  "quantity": 10
}
```

`symbol`, `type` and `time_in_force` are optional. They default to the engine's
default instrument, `LIMIT` and `GTC`, so a request written against the original
single-instrument API still works unchanged.

`price` is required for `LIMIT` orders and ignored for `MARKET` orders, which
have no limit and take whatever the book offers. A `MARKET` order never rests —
it has no price to rest at — so any unfillable remainder is cancelled regardless
of time-in-force.

**Stop orders** need `stop_price` and are held *off-book* with status `PENDING`
until the last traded price crosses it: a buy stop fires on the way up, a sell
stop on the way down. On triggering it becomes a `LIMIT` order if `price` is set,
or a `MARKET` order if it is not.

```json
{
  "symbol": "AAPL",
  "trader_id": "trader-1",
  "side": "BUY",
  "type": "STOP",
  "stop_price": 105.00,
  "price": 110.00,
  "quantity": 5
}
```

Three things worth knowing:

- A pending stop is **not liquidity**. It does not appear in `/orderbook` or
  `/depth`, and cancelling it goes through the same `DELETE /order/{id}`.
- A stop already through its trigger when it arrives fires immediately, rather
  than waiting for a print that may never come.
- Activation is a **cascade**: a triggered stop's own fills can move the price
  enough to trigger others. That is processed iteratively, so a long chain
  cannot exhaust the stack. The trigger price survives activation, so the order
  history can still show why an order reached the book.

**Response:** `201 Created`
```json
{
  "order_id": "uuid-here",
  "status": "NEW",
  "message": "Order submitted to matching engine"
}
```

The engine acknowledges before matching, so `status` is always `NEW` and this
response never reports fills — poll `/trades` and `/orderbook` for the outcome.

Returns `503` if the engine's intake buffer is saturated. The submit path never
blocks, so a busy engine fails fast and the request is safe to retry.

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

Every individual resting order, in true price-time priority order (bids highest
price first, asks lowest first, earliest timestamp breaking ties). Cancelled and
filled orders are filtered out, and duplicate order IDs are removed.

**Response:**
```json
{
  "bids": [ ... ],
  "asks": [ ... ]
}
```

---

### Get Depth

```
GET /depth?levels=15
```

The aggregated book — one entry per price rather than per order. Far cheaper
than `/orderbook` for rendering a ladder or depth chart. `levels` defaults to
15 and is capped at 100.

**Response:**
```json
{
  "bids": [
    { "price": 97.02, "quantity": 68, "order_count": 1, "cumulative": 68 }
  ],
  "asks": [
    { "price": 101.95, "quantity": 4, "order_count": 2, "cumulative": 4 }
  ],
  "best_bid": 97.02,
  "best_ask": 101.95,
  "spread": 4.93,
  "mid": 99.485
}
```

`best_bid`, `best_ask`, `spread` and `mid` are `null` when the relevant side is
empty.

---

### Get Trades

```
GET /trades?limit=500
```

Returns retained trades oldest-first. `limit` defaults to 500 and is capped at
5000; `limit=0` returns the full retained tape. `total` is how many trades are
currently held in memory, which is capped at 10,000 — older prints are dropped.

**Response:**
```json
{
  "count": 42,
  "total": 2095,
  "trades": [ ... ]
}
```

---

### Market Statistics

```
GET /stats?symbol=BTC/USD&window=86400
```

Rolling-window summary backing the terminal's PRICE / 24H CHANGE / VOLUME
readouts. `window` is in seconds and defaults to 24 hours.

**Response:**
```json
{
  "symbol": "BTC/USD",
  "last": 67433, "open": 67433, "high": 67433, "low": 67433,
  "change": 0, "change_percent": 0,
  "volume": 2, "quote_volume": 134866,
  "trade_count": 1,
  "window_seconds": 86400,
  "covered_seconds": 0
}
```

`volume` is base quantity, `quote_volume` is notional turnover.
`covered_seconds` is how much of the window the retained tape actually spans, so
a client can tell a quiet market from a short history. When no trades fall inside
the window, `last` still reports the most recent print — a zero would look like a
broken feed.

---

### Candles

```
GET /candles?symbol=BTC/USD&interval=1m&limit=200
```

Supported intervals: `1m`, `5m`, `15m`, `1h`, `4h`, `1d`. An unrecognised
interval is rejected with `400` rather than silently substituted, which would
hand back a chart at a timeframe the client did not ask for.

**Response:**
```json
{
  "symbol": "BTC/USD",
  "interval": "1m",
  "candles": [
    {
      "open_time": 1788118080000000000,
      "close_time": 1788118140000000000,
      "open": 67433, "high": 67433, "low": 67433, "close": 67433,
      "volume": 2, "trades": 1
    }
  ]
}
```

Buckets align to absolute time rather than to the first trade, so a print always
lands in the same bucket whatever window is requested. Empty buckets are omitted:
the engine has no session concept, so a gap in activity is a genuine absence of
data rather than a flat candle.

---

### Account

```
GET /account?trader_id=trader-1
```

**Response:**
```json
{
  "trader_id": "trader-1",
  "cash": 115134, "starting_cash": 250000,
  "realized_pnl": 0, "unrealized_pnl": 0,
  "position_value": 134866, "equity": 250000, "total_pnl": 0,
  "open_orders": 0,
  "positions": [
    {
      "symbol": "BTC/USD",
      "quantity": 2, "avg_entry": 67433,
      "realized_pnl": 0, "unrealized_pnl": 0,
      "mark_price": 67433, "value": 134866
    }
  ]
}
```

Positions use average-cost accounting and a signed quantity — positive long,
negative short. Realised profit is booked when exposure is reduced; the average
entry is untouched by reductions and reset when a position flips. Unrealised
profit is computed at read time against the venue's last traded price, and is
exactly zero for a symbol with no mark rather than an invented number.

**The ledger is observational, not enforcing.** Buying power is never checked, so
a trader can go arbitrarily long or short and cash may go negative. Enforcement
would mean rejecting orders on a balance check, which is a different feature with
different failure modes.

---

### Trader Orders

```
GET /orders?trader_id=trader-1&symbol=BTC/USD
```

Working orders and finished ones, newest first. `symbol` is an optional filter.

**Response:**
```json
{
  "trader_id": "trader-1",
  "open": [ ... ],
  "history": [ ... ]
}
```

The engine itself forgets an order the moment it leaves the book, so history is
retained here instead, bounded per trader.

---

### Get Metrics

```
GET /metrics
```

**Response:**
```json
{
  "total_orders_processed": 10000,
  "avg_latency_ms": 0.043,
  "orders_queued": 0,
  "resting_orders": 534,
  "trade_count": 2095
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

## Frontend

`web/` is a Next.js 16 + TypeScript trading terminal: depth ladder, price and
cumulative-depth charts, trade tape, order ticket and working-order management.

```bash
cd web
npm install
cp .env.example .env.local     # defaults to http://localhost:8080
npm run dev                    # http://localhost:3000
```

Run the engine at the same time. With no orders in the book the terminal starts
empty, so either place orders yourself or flip on **Sim flow** in the top rail,
which posts randomised orders from the browser to keep the book moving.

| Variable | Purpose |
|---|---|
| `NEXT_PUBLIC_ENGINE_URL` | Base URL of the engine. Defaults to `http://localhost:8080` |

Two things worth knowing about how it works:

Layout: order book on the left, candle chart over a market-depth curve in the
centre, trade tape over the order ticket on the right, with working
orders/positions/history along the bottom and engine health in the status bar.
Clicking any ladder level loads it into the ticket.

- **Market data is pushed, not polled.** The terminal holds one `EventSource` to
  `/stream` and applies trade, order and depth events as they arrive. History is
  seeded once over REST on connect, because the stream only carries events from
  the moment it opens. Events are buffered and committed to React state every
  100ms — a busy symbol emits hundreds per second, far more than a display can
  show.
- **What stays polled is deliberate.** Candles, statistics and per-trader account
  state have no engine event, and none of them is latency-sensitive. They refresh
  on 1.2–2s intervals.
- **The percent-of-balance buttons need the ledger.** 25/50/75/100% size against
  free cash for a buy and against the position held for a sell. Without
  `/account` they would be percentages of nothing.
- **Trader identity is client-side.** The engine has no accounts; `trader_id` is
  just a string on each order. The terminal generates one, persists it to
  `localStorage`, and lets you rename it. Renaming does not move orders already
  resting in the book.

---

## Deployment

The frontend and engine deploy separately.

**Engine → any long-running container host** (Fly.io, Railway, Render, a VM):

```bash
fly secrets set ALLOWED_ORIGINS="https://your-frontend.vercel.app"
fly deploy
```

**Frontend → Vercel**, with the project's root directory set to `web` and
`NEXT_PUBLIC_ENGINE_URL` set to the engine's public HTTPS URL.

### The engine cannot be scaled horizontally

All state — the order book, the trade tape, the metrics counters — lives in one
process's memory. That has hard consequences:

- **Exactly one instance.** Two replicas serve two independent, divergent order
  books; a request routed to the wrong one sees a different market. Symbols are
  isolated *within* a process, which is not the same as being shardable across
  processes — routing a symbol to its own instance would need a router that does
  not exist here.
- **No serverless.** Vercel Functions, Lambda and similar lose the book between
  invocations.
- **Restarts erase everything unless `WAL_PATH` is set.** With a write-ahead log
  on a persistent volume, state is rebuilt on boot by replaying the log; without
  one, a redeploy starts from an empty book.
- **Scale-to-zero must be off.** `fly.toml` sets `auto_stop_machines = false`
  and `min_machines_running = 1` for exactly this reason.

The write-ahead log makes recovery possible but is not a complete durability
story: it grows without bound. A production system would snapshot state
periodically and truncate the log behind the snapshot.

---

## Design Decisions

| Decision | Why |
|---|---|
| `container/heap` for order book | O(log n) insert/remove, correct price-time priority |
| Buffered channel (1000) | Decouples HTTP layer from matching engine, backpressure |
| One matching goroutine per symbol | Avoids locking complexity in matching, and keeps instruments from contending |
| Lazy deletion for cancels | Heap doesn't support O(log n) arbitrary removal; cancelled orders are skipped during matching |
| Fill resting orders in place | Popping and re-adding would churn the heap, lose time priority, and briefly make the order uncancellable |
| Trade price = resting order's price | Maker pricing, independent of which side was the aggressor |
| FOK checked before any fill | A partial fill cannot be un-printed, so availability must be known up front |
| Non-blocking event publish | A slow market-data consumer must never become matching latency |
| WAL append before submit | An order that cannot be durably recorded has not really been accepted |
| Stops held off-book | An untriggered stop is not liquidity and must not appear as depth |
| Stop cascades processed iteratively | A chain of triggers would otherwise recurse without bound |
| Resolved fill observer | A raw trade carries only order IDs; position keeping needs the traders, so the engine hands over both sides rather than making listeners join events |
| Candle buckets on absolute time | A print lands in the same bucket regardless of the window requested |
