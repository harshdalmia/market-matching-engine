import type { Trade } from "./types";

export type TickDirection = "up" | "down" | "flat";

export interface TapePrint extends Trade {
  direction: TickDirection;
}

/**
 * Tags each print with an inferred direction.
 *
 * The engine's Trade struct has no side or aggressor flag, so direction cannot
 * be read off the wire — it is inferred by comparing consecutive prints.
 * `trades` must be oldest-first, which is the order the tape arrives in.
 */
export function annotateTrades(trades: readonly Trade[]): TapePrint[] {
  const out: TapePrint[] = [];
  let previous: number | null = null;

  for (const trade of trades) {
    let direction: TickDirection = "flat";
    if (previous !== null) {
      if (trade.price > previous) direction = "up";
      else if (trade.price < previous) direction = "down";
    }
    out.push({ ...trade, direction });
    previous = trade.price;
  }

  return out;
}
