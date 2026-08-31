"use client";

import { motion } from "motion/react";
import { useMemo } from "react";
import { formatPrice, formatSize } from "@/lib/format";
import type { DepthSnapshot, PriceLevel } from "@/lib/types";

const W = 900;
const H = 130;
const PAD = { top: 6, right: 6, bottom: 14, left: 6 };
const PLOT_W = W - PAD.left - PAD.right;
const PLOT_H = H - PAD.top - PAD.bottom;

/**
 * Builds a step path for one side of the book.
 *
 * Depth is constant between levels, so the curve steps: hold the previous depth
 * across to the next price, then move vertically. Interpolating diagonally would
 * imply liquidity at prices that have none.
 */
function stepPath(
  levels: PriceLevel[],
  x: (price: number) => number,
  y: (qty: number) => number,
  baseline: number,
): { line: string; area: string } | null {
  if (levels.length === 0) return null;

  const parts: string[] = [];
  let prevY = y(levels[0].cumulative);
  parts.push(`M${x(levels[0].price).toFixed(2)},${prevY.toFixed(2)}`);

  for (let i = 1; i < levels.length; i += 1) {
    const px = x(levels[i].price).toFixed(2);
    const py = y(levels[i].cumulative);
    parts.push(`L${px},${prevY.toFixed(2)}`);
    parts.push(`L${px},${py.toFixed(2)}`);
    prevY = py;
  }

  const line = parts.join(" ");
  const firstX = x(levels[0].price).toFixed(2);
  const lastX = x(levels[levels.length - 1].price).toFixed(2);

  return {
    line,
    area: `${line} L${lastX},${baseline.toFixed(2)} L${firstX},${baseline.toFixed(2)} Z`,
  };
}

export function MarketDepthPanel({ depth }: { depth: DepthSnapshot | null }) {
  const geometry = useMemo(() => {
    if (!depth) return null;

    // Bids arrive best-first; ascending price reads left to right with depth
    // building away from the mid.
    const bids = [...depth.bids].reverse();
    const asks = depth.asks;

    if (bids.length === 0 && asks.length === 0) return null;

    const prices = [...bids, ...asks].map((l) => l.price);
    const lo = Math.min(...prices);
    const hi = Math.max(...prices);
    const spanX = Math.max(hi - lo, 0.01);

    const maxQty = Math.max(
      bids.length > 0 ? bids[0].cumulative : 0,
      asks.length > 0 ? asks[asks.length - 1].cumulative : 0,
      1,
    );

    const x = (price: number) => PAD.left + ((price - lo) / spanX) * PLOT_W;
    const y = (qty: number) => PAD.top + PLOT_H - (qty / maxQty) * PLOT_H;
    const baseline = PAD.top + PLOT_H;

    return {
      bid: stepPath(bids, x, y, baseline),
      ask: stepPath(asks, x, y, baseline),
      lo,
      hi,
      maxQty,
      midX: depth.mid != null ? x(depth.mid) : null,
    };
  }, [depth]);

  const bidDepth = depth?.bids.at(-1)?.cumulative ?? 0;
  const askDepth = depth?.asks.at(-1)?.cumulative ?? 0;

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="panel-head justify-between gap-3">
        <span>Market Depth</span>
        <span className="flex items-center gap-3 text-[9.5px] normal-case tracking-normal">
          <span className="text-up">bid {formatSize(bidDepth)}</span>
          <span className="text-down">ask {formatSize(askDepth)}</span>
        </span>
      </header>

      <div className="relative min-h-0 flex-1">
        {geometry === null ? (
          <div className="flex h-full items-center justify-center">
            <p className="text-[10.5px] text-txt-faint">No resting liquidity</p>
          </div>
        ) : (
          <svg
            viewBox={`0 0 ${W} ${H}`}
            preserveAspectRatio="none"
            className="h-full w-full"
            role="img"
            aria-label="Cumulative order book depth by price"
          >
            <defs>
              <linearGradient id="depth-bid" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--color-up-solid)" stopOpacity={0.4} />
                <stop offset="100%" stopColor="var(--color-up-solid)" stopOpacity={0.05} />
              </linearGradient>
              <linearGradient id="depth-ask" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--color-down-solid)" stopOpacity={0.4} />
                <stop offset="100%" stopColor="var(--color-down-solid)" stopOpacity={0.05} />
              </linearGradient>
            </defs>

            {geometry.bid ? (
              <motion.g
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                transition={{ duration: 0.3 }}
              >
                <path d={geometry.bid.area} fill="url(#depth-bid)" />
                <path
                  d={geometry.bid.line}
                  fill="none"
                  stroke="var(--color-up)"
                  strokeWidth={1.25}
                  vectorEffect="non-scaling-stroke"
                />
              </motion.g>
            ) : null}

            {geometry.ask ? (
              <motion.g
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                transition={{ duration: 0.3 }}
              >
                <path d={geometry.ask.area} fill="url(#depth-ask)" />
                <path
                  d={geometry.ask.line}
                  fill="none"
                  stroke="var(--color-down)"
                  strokeWidth={1.25}
                  vectorEffect="non-scaling-stroke"
                />
              </motion.g>
            ) : null}

            {/* The mid divides the two sides. */}
            {geometry.midX != null ? (
              <line
                x1={geometry.midX}
                x2={geometry.midX}
                y1={PAD.top}
                y2={PAD.top + PLOT_H}
                stroke="currentColor"
                className="text-txt-faint"
                strokeWidth={1}
                strokeDasharray="3 3"
                vectorEffect="non-scaling-stroke"
              />
            ) : null}
          </svg>
        )}
      </div>

      {geometry !== null ? (
        <div className="flex shrink-0 items-center justify-between border-t border-line px-2.5 py-1 text-[9px] text-txt-faint">
          <span>{formatPrice(geometry.lo)}</span>
          <span>{depth?.mid != null ? formatPrice(depth.mid) : ""}</span>
          <span>{formatPrice(geometry.hi)}</span>
        </div>
      ) : null}
    </section>
  );
}
