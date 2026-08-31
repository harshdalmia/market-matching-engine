"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, streamURL } from "./api";
import type { DepthSnapshot, Order, StreamEvent, Trade } from "./types";

/** Trades retained client-side for the chart and tape. */
const MAX_TRADES = 400;

/**
 * How often buffered events are committed to React state.
 *
 * A busy symbol emits hundreds of events per second. Calling setState on each
 * one would re-render the whole terminal far faster than a display can show it,
 * so events accumulate in refs and flush on this interval instead.
 */
const FLUSH_INTERVAL_MS = 100;

export type ConnectionStatus = "connecting" | "live" | "reconnecting" | "offline";

export interface EngineStream {
  status: ConnectionStatus;
  depth: DepthSnapshot | null;
  /** Oldest-first, capped at MAX_TRADES. */
  trades: Trade[];
  /** Live resting orders keyed by ID. Terminal orders are removed. */
  ordersById: Record<string, Order>;
  eventsReceived: number;
  /** Re-seeds history and reconnects. */
  reload: () => void;
}

interface StreamState {
  depth: DepthSnapshot | null;
  trades: Trade[];
  ordersById: Record<string, Order>;
  eventsReceived: number;
}

const EMPTY_STATE: StreamState = {
  depth: null,
  trades: [],
  ordersById: {},
  eventsReceived: 0,
};

function isTerminal(order: Order): boolean {
  return (
    order.status === "FILLED" ||
    order.status === "CANCELLED" ||
    order.status === "REJECTED"
  );
}

/**
 * Subscribes to the engine's Server-Sent Events feed for one symbol.
 *
 * The stream only carries events from the moment it connects, so history is
 * seeded once over REST — otherwise the chart and tape would start empty on
 * every symbol change.
 */
export function useEngineStream(symbol: string | null): EngineStream {
  const [state, setState] = useState<StreamState>(EMPTY_STATE);
  const [status, setStatus] = useState<ConnectionStatus>("connecting");
  const [reloadToken, setReloadToken] = useState(0);

  // Event buffers, drained by the flush timer below.
  const pendingDepth = useRef<DepthSnapshot | null>(null);
  const pendingTrades = useRef<Trade[]>([]);
  const pendingOrders = useRef<Order[]>([]);
  const receivedCount = useRef(0);
  const dirty = useRef(false);

  const reload = useCallback(() => setReloadToken((n) => n + 1), []);

  // ---- Commit buffered events on a fixed cadence -------------------------
  useEffect(() => {
    const timer = setInterval(() => {
      if (!dirty.current) return;
      dirty.current = false;

      const depth = pendingDepth.current;
      const trades = pendingTrades.current;
      const orders = pendingOrders.current;
      const received = receivedCount.current;

      pendingDepth.current = null;
      pendingTrades.current = [];
      pendingOrders.current = [];

      setState((prev) => {
        let nextTrades = prev.trades;
        if (trades.length > 0) {
          // The REST seed and the first streamed events can overlap, so dedupe
          // on trade ID rather than trusting arrival order.
          const seen = new Set(prev.trades.map((t) => t.id));
          const fresh = trades.filter((t) => !seen.has(t.id));
          if (fresh.length > 0) {
            nextTrades = [...prev.trades, ...fresh].slice(-MAX_TRADES);
          }
        }

        let nextOrders = prev.ordersById;
        if (orders.length > 0) {
          nextOrders = { ...prev.ordersById };
          for (const order of orders) {
            if (isTerminal(order)) {
              delete nextOrders[order.id];
            } else {
              nextOrders[order.id] = order;
            }
          }
        }

        return {
          depth: depth ?? prev.depth,
          trades: nextTrades,
          ordersById: nextOrders,
          eventsReceived: received,
        };
      });
    }, FLUSH_INTERVAL_MS);

    return () => clearInterval(timer);
  }, []);

  // ---- Connect and seed --------------------------------------------------
  useEffect(() => {
    if (!symbol) return;

    let cancelled = false;

    // Reset everything: state from the previous symbol is meaningless here.
    pendingDepth.current = null;
    pendingTrades.current = [];
    pendingOrders.current = [];
    receivedCount.current = 0;
    dirty.current = false;
    setState(EMPTY_STATE);
    setStatus("connecting");

    void (async () => {
      try {
        const [tradesRes, book] = await Promise.all([
          api.trades(symbol, MAX_TRADES),
          api.orderbook(symbol),
        ]);
        if (cancelled) return;

        const seeded: Record<string, Order> = {};
        for (const order of [...book.bids, ...book.asks]) {
          seeded[order.id] = order;
        }

        setState((prev) => ({
          ...prev,
          trades: tradesRes.trades,
          // Anything the stream already delivered wins: it is newer.
          ordersById: { ...seeded, ...prev.ordersById },
        }));
      } catch {
        // Seeding is best-effort. The stream may still connect and work.
      }
    })();

    const source = new EventSource(streamURL([symbol]));

    const handleDepth = (event: MessageEvent<string>) => {
      try {
        const parsed = JSON.parse(event.data) as StreamEvent<DepthSnapshot>;
        if (parsed.data) pendingDepth.current = parsed.data;
        receivedCount.current += 1;
        dirty.current = true;
      } catch {
        // Ignore a malformed frame rather than tearing down the connection.
      }
    };

    const handleTrade = (event: MessageEvent<string>) => {
      try {
        const parsed = JSON.parse(event.data) as StreamEvent<Trade>;
        if (parsed.data) pendingTrades.current.push(parsed.data);
        receivedCount.current += 1;
        dirty.current = true;
      } catch {
        // Ignore malformed frame.
      }
    };

    const handleOrder = (event: MessageEvent<string>) => {
      try {
        const parsed = JSON.parse(event.data) as StreamEvent<Order>;
        if (parsed.data) pendingOrders.current.push(parsed.data);
        receivedCount.current += 1;
        dirty.current = true;
      } catch {
        // Ignore malformed frame.
      }
    };

    source.addEventListener("snapshot", handleDepth);
    source.addEventListener("book", handleDepth);
    source.addEventListener("trade", handleTrade);
    source.addEventListener("order", handleOrder);

    source.onopen = () => setStatus("live");

    source.onerror = () => {
      // EventSource reconnects on its own unless it has been closed for good,
      // so distinguish "retrying" from "gave up".
      setStatus(source.readyState === EventSource.CLOSED ? "offline" : "reconnecting");
    };

    return () => {
      cancelled = true;
      source.removeEventListener("snapshot", handleDepth);
      source.removeEventListener("book", handleDepth);
      source.removeEventListener("trade", handleTrade);
      source.removeEventListener("order", handleOrder);
      source.close();
    };
  }, [symbol, reloadToken]);

  return {
    status,
    depth: state.depth,
    trades: state.trades,
    ordersById: state.ordersById,
    eventsReceived: state.eventsReceived,
    reload,
  };
}
