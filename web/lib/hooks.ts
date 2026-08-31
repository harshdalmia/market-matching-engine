"use client";

import { useEffect, useRef, useState } from "react";
import useSWR, { type SWRConfiguration } from "swr";
import { api } from "./api";
import type {
  AccountSnapshot,
  CandleSeries,
  MarketStats,
  MetricsResponse,
  SymbolsResponse,
  TraderOrders,
} from "./types";

/**
 * Order book depth, trades and order updates arrive over Server-Sent Events
 * (see lib/stream.ts). What stays polled here is everything the engine has no
 * event for: aggregated statistics, candle buckets and per-trader account state.
 * None of it is latency-critical, and none of it changes faster than a person
 * can read it.
 */
const base: SWRConfiguration = {
  keepPreviousData: true,
  revalidateOnFocus: false,
  // A failed poll should retry on the next tick, not spiral into a retry storm.
  shouldRetryOnError: false,
};

/** The tradable instruments. Fixed at engine startup, so this rarely changes. */
export function useSymbols() {
  return useSWR<SymbolsResponse>("symbols", () => api.symbols(), {
    ...base,
    refreshInterval: 30_000,
  });
}

export function useMetrics(refreshInterval = 2000) {
  return useSWR<MetricsResponse>("metrics", () => api.metrics(), {
    ...base,
    refreshInterval,
  });
}

export function useStats(symbol: string | null, refreshInterval = 2000) {
  return useSWR<MarketStats>(
    symbol ? ["stats", symbol] : null,
    () => api.stats(symbol ?? undefined),
    { ...base, refreshInterval },
  );
}

export function useCandles(
  symbol: string | null,
  interval: string,
  refreshInterval = 1500,
) {
  return useSWR<CandleSeries>(
    symbol ? ["candles", symbol, interval] : null,
    () => api.candles(symbol ?? undefined, interval),
    { ...base, refreshInterval },
  );
}

export function useAccount(traderId: string, refreshInterval = 1500) {
  return useSWR<AccountSnapshot>(
    traderId ? ["account", traderId] : null,
    () => api.account(traderId),
    { ...base, refreshInterval },
  );
}

export function useTraderOrders(traderId: string, refreshInterval = 1200) {
  return useSWR<TraderOrders>(
    traderId ? ["trader-orders", traderId] : null,
    () => api.traderOrders(traderId),
    { ...base, refreshInterval },
  );
}

/** Previous committed value of `value`, or undefined on first render. */
export function usePrevious<T>(value: T): T | undefined {
  const ref = useRef<T | undefined>(undefined);
  useEffect(() => {
    ref.current = value;
  }, [value]);
  return ref.current;
}

/**
 * Emits "up" or "down" for `duration` ms whenever `value` changes, then clears.
 * Drives the price flash on ladder rows and the headline quote.
 */
export function useTickFlash(
  value: number | null | undefined,
  duration = 400,
): "up" | "down" | null {
  const [flash, setFlash] = useState<"up" | "down" | null>(null);
  const previous = useRef<number | null>(null);

  useEffect(() => {
    if (value == null) return;

    const prev = previous.current;
    previous.current = value;

    if (prev === null || value === prev) return;

    setFlash(value > prev ? "up" : "down");
    const timer = setTimeout(() => setFlash(null), duration);
    return () => clearTimeout(timer);
  }, [value, duration]);

  return flash;
}

/** True once the component has mounted on the client. */
export function useMounted(): boolean {
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  return mounted;
}
