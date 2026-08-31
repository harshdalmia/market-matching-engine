import type {
  AccountSnapshot,
  CancelOrderResult,
  CandleSeries,
  DepthSnapshot,
  HealthResponse,
  MarketStats,
  MetricsResponse,
  OrderBookSnapshot,
  PlaceOrderRequest,
  PlaceOrderResponse,
  SymbolsResponse,
  TraderOrders,
  TradesResponse,
} from "./types";

/** Base URL of the Go engine. Trailing slashes are stripped so paths join cleanly. */
export const ENGINE_URL = (
  process.env.NEXT_PUBLIC_ENGINE_URL ?? "http://localhost:8080"
).replace(/\/+$/, "");

/** URL of the Server-Sent Events feed, optionally filtered to some symbols. */
export function streamURL(symbols?: string[]): string {
  const url = new URL(`${ENGINE_URL}/stream`);
  if (symbols && symbols.length > 0) {
    url.searchParams.set("symbols", symbols.join(","));
  }
  return url.toString();
}

/**
 * Error carrying the HTTP status plus the engine's `{"error": "..."}` message.
 * The engine returns that flat envelope for every failure path.
 */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;

  try {
    res = await fetch(`${ENGINE_URL}${path}`, {
      ...init,
      headers: {
        Accept: "application/json",
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...init?.headers,
      },
      cache: "no-store",
    });
  } catch {
    // Network-level failure: engine down, wrong URL, or CORS rejection.
    throw new ApiError(0, "Cannot reach the matching engine");
  }

  const raw = await res.text();
  let body: unknown = null;
  if (raw) {
    try {
      body = JSON.parse(raw);
    } catch {
      body = null;
    }
  }

  if (!res.ok) {
    const message =
      typeof body === "object" && body !== null && "error" in body
        ? String((body as { error: unknown }).error)
        : `Request failed with status ${res.status}`;
    throw new ApiError(res.status, message);
  }

  return body as T;
}

/**
 * Builds a query string, omitting the symbol when absent so the engine applies
 * its own default instrument.
 *
 * Symbols contain slashes ("BTC/USD"), which URLSearchParams percent-encodes.
 */
function query(path: string, params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, value);
  }
  const encoded = search.toString();
  return encoded ? `${path}?${encoded}` : path;
}

export const api = {
  health: () => request<HealthResponse>("/health"),

  symbols: () => request<SymbolsResponse>("/symbols"),

  depth: (symbol?: string, levels = 15) =>
    request<DepthSnapshot>(query("/depth", { symbol, levels: String(levels) })),

  orderbook: (symbol?: string) =>
    request<OrderBookSnapshot>(query("/orderbook", { symbol })),

  trades: (symbol?: string, limit = 240) =>
    request<TradesResponse>(query("/trades", { symbol, limit: String(limit) })),

  stats: (symbol?: string, windowSeconds?: number) =>
    request<MarketStats>(
      query("/stats", {
        symbol,
        window: windowSeconds ? String(windowSeconds) : undefined,
      }),
    ),

  candles: (symbol?: string, interval = "1m", limit = 200) =>
    request<CandleSeries>(
      query("/candles", { symbol, interval, limit: String(limit) }),
    ),

  account: (traderId: string) =>
    request<AccountSnapshot>(query("/account", { trader_id: traderId })),

  traderOrders: (traderId: string, symbol?: string) =>
    request<TraderOrders>(query("/orders", { trader_id: traderId, symbol })),

  metrics: () => request<MetricsResponse>("/metrics"),

  placeOrder: (body: PlaceOrderRequest) =>
    request<PlaceOrderResponse>("/order", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  cancelOrder: (id: string) =>
    request<CancelOrderResult>(`/order/${encodeURIComponent(id)}`, {
      method: "DELETE",
    }),
};
