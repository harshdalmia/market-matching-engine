/**
 * Wire types for the Go matching engine.
 *
 * Every field name here mirrors a `json:"..."` tag in the Go source. Keep them
 * in sync with internal/models, internal/accounts and internal/marketdata.
 */

export type OrderSide = "BUY" | "SELL";

/**
 * A STOP order is held off-book until the last traded price crosses its
 * stop_price, then becomes a LIMIT (if price is set) or MARKET order.
 */
export type OrderType = "LIMIT" | "MARKET" | "STOP";

/**
 * GTC — rests in the book until filled or cancelled.
 * IOC — fills what is available now, cancels the rest.
 * FOK — fills entirely or does nothing at all.
 */
export type TimeInForce = "GTC" | "IOC" | "FOK";

export type OrderStatus =
  | "NEW"
  | "PENDING"
  | "PARTIALLY_FILLED"
  | "FILLED"
  | "CANCELLED"
  | "REJECTED";

/**
 * Timestamps from the engine are `time.Now().UnixNano()` — roughly 1.7e18,
 * which exceeds Number.MAX_SAFE_INTEGER (9.0e15). JSON.parse silently rounds
 * them to within ~256ns. Harmless for display, but never use a timestamp as an
 * identity or dedup key; use `id` instead.
 */
export interface Order {
  id: string;
  symbol: string;
  trader_id: string;
  side: OrderSide;
  type: OrderType;
  time_in_force: TimeInForce;
  /** Always 0 for MARKET orders, which have no price limit. */
  price: number;
  /** Trigger price. Non-zero only for STOP orders. */
  stop_price: number;
  quantity: number;
  remaining: number;
  timestamp: number;
  status: OrderStatus;
}

export interface Trade {
  id: string;
  symbol: string;
  buy_order_id: string;
  sell_order_id: string;
  price: number;
  quantity: number;
  timestamp: number;
}

export interface OrderBookSnapshot {
  bids: Order[];
  asks: Order[];
}

export interface PriceLevel {
  price: number;
  quantity: number;
  order_count: number;
  cumulative: number;
}

export interface DepthSnapshot {
  symbol: string;
  bids: PriceLevel[];
  asks: PriceLevel[];
  best_bid: number | null;
  best_ask: number | null;
  spread: number | null;
  mid: number | null;
}

export interface PlaceOrderRequest {
  symbol: string;
  trader_id: string;
  side: OrderSide;
  type: OrderType;
  time_in_force: TimeInForce;
  price: number;
  stop_price: number;
  quantity: number;
}

export interface PlaceOrderResponse {
  order_id: string;
  symbol: string;
  status: OrderStatus;
  type: OrderType;
  time_in_force: TimeInForce;
  stop_price: number;
  message: string;
}

export interface CancelOrderResult {
  success: boolean;
  symbol: string;
  message: string;
}

export interface TradesResponse {
  symbol: string;
  count: number;
  total: number;
  trades: Trade[];
}

// ---------------------------------------------------------------------------
// Market data
// ---------------------------------------------------------------------------

/** Rolling-window statistics. Backs the header's PRICE / 24H CHANGE / VOLUME. */
export interface MarketStats {
  symbol: string;
  last: number;
  open: number;
  high: number;
  low: number;
  change: number;
  change_percent: number;
  /** Base quantity traded. */
  volume: number;
  /** Notional turnover — quantity times price. */
  quote_volume: number;
  trade_count: number;
  window_seconds: number;
  /**
   * How much of the window the retained tape actually spans. Lets the UI tell a
   * quiet market from a short history.
   */
  covered_seconds: number;
}

export interface Candle {
  open_time: number;
  close_time: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  trades: number;
}

export interface CandleSeries {
  symbol: string;
  interval: string;
  candles: Candle[];
}

/** Intervals the engine accepts, in the order the chart tabs show them. */
export const CANDLE_INTERVALS = ["1m", "15m", "1h", "4h", "1d"] as const;

export type CandleInterval = (typeof CANDLE_INTERVALS)[number];

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

/** Quantity is signed: positive is long, negative is short. */
export interface Position {
  symbol: string;
  quantity: number;
  avg_entry: number;
  realized_pnl: number;
  unrealized_pnl: number;
  mark_price: number;
  value: number;
}

export interface AccountSnapshot {
  trader_id: string;
  cash: number;
  starting_cash: number;
  realized_pnl: number;
  unrealized_pnl: number;
  position_value: number;
  equity: number;
  total_pnl: number;
  positions: Position[];
  open_orders: number;
}

export interface TraderOrders {
  trader_id: string;
  open: Order[];
  history: Order[];
}

// ---------------------------------------------------------------------------
// Exchange metadata
// ---------------------------------------------------------------------------

export interface SymbolStats {
  symbol: string;
  best_bid: number | null;
  best_ask: number | null;
  spread: number | null;
  mid: number | null;
  last_price: number | null;
  resting_orders: number;
  pending_stops: number;
  trade_count: number;
  orders_queued: number;
  orders_matched: number;
  avg_latency_ms: number;
}

export interface SymbolsResponse {
  symbols: string[];
  default: string;
  stats: SymbolStats[];
}

export interface MetricsResponse {
  total_orders_processed: number;
  avg_latency_ms: number;
  orders_queued: number;
  resting_orders: number;
  trade_count: number;
  stream_subscribers: number;
  symbols: SymbolStats[];
}

export interface HealthResponse {
  status: string;
  uptime_seconds: number;
  orders_queued: number;
  resting_orders: number;
  trade_count: number;
  symbols: string[];
}

// ---------------------------------------------------------------------------
// Server-Sent Events
// ---------------------------------------------------------------------------

export type StreamEventType =
  | "snapshot"
  | "book"
  | "trade"
  | "order"
  | "heartbeat";

/** Envelope emitted by GET /stream. Mirrors internal/stream.Event. */
export interface StreamEvent<T = unknown> {
  seq: number;
  type: StreamEventType;
  symbol: string;
  time: number;
  data?: T;
}
