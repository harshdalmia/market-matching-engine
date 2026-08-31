"use client";

import { useCallback, useEffect, useState } from "react";

const TRADER_KEY = "mme.trader-id.v1";
const ORDERS_KEY = "mme.my-orders.v1";

/** Cap on locally remembered order IDs so localStorage cannot grow forever. */
const MAX_TRACKED_ORDERS = 250;

export function generateTraderId(): string {
  const suffix = Math.random().toString(16).slice(2, 8).padEnd(6, "0");
  return `trader-${suffix}`;
}

/**
 * The engine only rejects empty trader IDs, so normalisation here is about
 * keeping identities readable and safe to render rather than satisfying the API.
 */
export function normalizeTraderId(raw: string): string {
  return raw
    .trim()
    .replace(/\s+/g, "-")
    .replace(/[^A-Za-z0-9._-]/g, "")
    .slice(0, 32);
}

function readTracked(): string[] {
  try {
    const raw = window.localStorage.getItem(ORDERS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is string => typeof v === "string");
  } catch {
    return [];
  }
}

export interface TraderState {
  /** Empty string until the identity has been hydrated from localStorage. */
  traderId: string;
  /** False during the first client render, so the UI can avoid a hydration mismatch. */
  ready: boolean;
  myOrderIds: string[];
  setTraderId: (next: string) => void;
  regenerate: () => void;
  trackOrder: (orderId: string) => void;
  clearTracked: () => void;
}

/**
 * Persistent trader identity plus the set of order IDs submitted from this
 * browser.
 *
 * The identity is generated on the client inside an effect rather than during
 * render: Math.random() on the server would produce different markup than the
 * client and trip a hydration error.
 */
export function useTrader(): TraderState {
  const [traderId, setTraderIdState] = useState("");
  const [myOrderIds, setMyOrderIds] = useState<string[]>([]);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let id: string;
    try {
      const stored = window.localStorage.getItem(TRADER_KEY);
      id = stored && stored.trim() ? stored : generateTraderId();
      window.localStorage.setItem(TRADER_KEY, id);
      setMyOrderIds(readTracked());
    } catch {
      // Private browsing or disabled storage: fall back to an ephemeral ID.
      id = generateTraderId();
    }
    setTraderIdState(id);
    setReady(true);
  }, []);

  const persistTraderId = useCallback((next: string) => {
    setTraderIdState(next);
    try {
      window.localStorage.setItem(TRADER_KEY, next);
    } catch {
      // Non-fatal: identity simply will not survive a reload.
    }
  }, []);

  const setTraderId = useCallback(
    (next: string) => {
      const clean = normalizeTraderId(next);
      persistTraderId(clean || generateTraderId());
    },
    [persistTraderId],
  );

  const regenerate = useCallback(() => {
    persistTraderId(generateTraderId());
  }, [persistTraderId]);

  const trackOrder = useCallback((orderId: string) => {
    setMyOrderIds((prev) => {
      if (prev.includes(orderId)) return prev;
      const next = [orderId, ...prev].slice(0, MAX_TRACKED_ORDERS);
      try {
        window.localStorage.setItem(ORDERS_KEY, JSON.stringify(next));
      } catch {
        // Non-fatal.
      }
      return next;
    });
  }, []);

  const clearTracked = useCallback(() => {
    setMyOrderIds([]);
    try {
      window.localStorage.removeItem(ORDERS_KEY);
    } catch {
      // Non-fatal.
    }
  }, []);

  return {
    traderId,
    ready,
    myOrderIds,
    setTraderId,
    regenerate,
    trackOrder,
    clearTracked,
  };
}
