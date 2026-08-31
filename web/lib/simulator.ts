"use client";

import { useEffect, useRef } from "react";
import { api, ApiError } from "./api";
import type { OrderSide, OrderType, TimeInForce } from "./types";

/**
 * Synthetic order flow, driven from the browser.
 *
 * The Go generator only runs as a one-shot burst behind the -stress flag and is
 * not reachable over HTTP, so a freshly started engine sits completely still.
 * This posts randomised orders through the public API instead, which keeps the
 * demo alive without requiring an extra backend endpoint.
 *
 * Simulated flow deliberately uses its own trader IDs so it never pollutes the
 * operator's "My Orders" panel.
 */
const SIM_TRADERS = [
  "sim-maker-a",
  "sim-maker-b",
  "sim-taker-a",
  "sim-taker-b",
  "sim-vol-desk",
] as const;

/** Fraction of simulated limit orders priced to cross the spread. */
const AGGRESSIVE_RATIO = 0.38;

const FALLBACK_ANCHOR = 100;

/**
 * Picks an order type and time-in-force. Mostly resting limits, with a minority
 * of immediate types so the tape and status column show real variety.
 */
function pickOrderStyle(): { type: OrderType; tif: TimeInForce } {
  const roll = Math.random();
  if (roll < 0.06) return { type: "MARKET", tif: "IOC" };
  if (roll < 0.12) return { type: "LIMIT", tif: "IOC" };
  if (roll < 0.16) return { type: "LIMIT", tif: "FOK" };
  return { type: "LIMIT", tif: "GTC" };
}

export interface SimulatorOptions {
  enabled: boolean;
  symbol: string | null;
  /** Current mid price; the simulator prices around it. Null falls back to 100. */
  mid: number | null;
  ordersPerSecond?: number;
}

export function useFlowSimulator({
  enabled,
  symbol,
  mid,
  ordersPerSecond = 3,
}: SimulatorOptions): void {
  // Held in refs so price and symbol updates don't tear down the interval.
  const midRef = useRef<number | null>(mid);
  midRef.current = mid;

  const symbolRef = useRef<string | null>(symbol);
  symbolRef.current = symbol;

  useEffect(() => {
    if (!enabled) return;

    let cancelled = false;
    // Backoff counter so a saturated or unreachable engine isn't hammered.
    let skipTicks = 0;

    const tick = async () => {
      if (cancelled) return;

      const targetSymbol = symbolRef.current;
      if (!targetSymbol) return;

      if (skipTicks > 0) {
        skipTicks -= 1;
        return;
      }

      const anchor = midRef.current ?? FALLBACK_ANCHOR;
      const drift = (Math.random() - 0.5) * 0.6;
      const centre = anchor + drift;

      const side: OrderSide = Math.random() < 0.5 ? "BUY" : "SELL";
      const { type, tif } = pickOrderStyle();

      const aggressive = Math.random() < AGGRESSIVE_RATIO;
      const edge = 0.04 + Math.random() * 0.9;

      // An aggressive buy bids above the mid; an aggressive sell offers below it.
      const signed = side === "BUY" ? 1 : -1;
      const limit = centre + signed * (aggressive ? edge : -edge);

      // Market orders carry no price; the engine ignores the field entirely.
      const price = type === "MARKET" ? 0 : Math.max(0.01, Math.round(limit * 100) / 100);

      // Keep fill-or-kill sizes small, or almost every one gets killed and the
      // simulated flow stops producing prints.
      const quantity =
        tif === "FOK" ? 1 + Math.floor(Math.random() * 8) : 1 + Math.floor(Math.random() * 60);

      const trader = SIM_TRADERS[Math.floor(Math.random() * SIM_TRADERS.length)];

      try {
        await api.placeOrder({
          symbol: targetSymbol,
          trader_id: trader,
          side,
          type,
          time_in_force: tif,
          price,
          // The simulator only places limit and market orders; stops would sit
          // pending and produce no visible flow.
          stop_price: 0,
          quantity,
        });
      } catch (err) {
        // 503 means the intake buffer is full; anything else usually means the
        // engine is unreachable. Either way, ease off briefly.
        skipTicks = err instanceof ApiError && err.status === 503 ? 3 : 10;
      }
    };

    const intervalMs = Math.max(60, Math.round(1000 / ordersPerSecond));
    const handle = setInterval(tick, intervalMs);
    void tick();

    return () => {
      cancelled = true;
      clearInterval(handle);
    };
  }, [enabled, ordersPerSecond]);
}
