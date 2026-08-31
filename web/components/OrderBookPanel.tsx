"use client";

import { AnimatePresence, motion } from "motion/react";
import { useTickFlash } from "@/lib/hooks";
import { formatPrice, formatSize, percentOf } from "@/lib/format";
import type { DepthSnapshot, OrderSide, PriceLevel } from "@/lib/types";
import { RollingNumber } from "./RollingNumber";

/** Levels rendered per side. Fixed so the pane never resizes as depth changes. */
const VISIBLE_LEVELS = 9;

interface RowProps {
  level: PriceLevel;
  side: OrderSide;
  /** Largest cumulative depth across both sides, for bar scaling. */
  max: number;
  onSelect?: (price: number, side: OrderSide) => void;
}

function LadderRow({ level, side, max, onSelect }: RowProps) {
  const isBid = side === "BUY";
  const width = percentOf(level.cumulative, max);

  // Flash when the resting size at this price changes.
  const flash = useTickFlash(level.quantity, 380);

  return (
    <motion.button
      type="button"
      layout="position"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.15 }}
      onClick={() => onSelect?.(level.price, side)}
      title={`${level.order_count} order${level.order_count === 1 ? "" : "s"} at ${formatPrice(level.price)} — click to load into the ticket`}
      className="relative grid h-[18px] w-full shrink-0 grid-cols-[1fr_1fr_1fr] items-center overflow-hidden px-2.5 text-left outline-none focus-visible:ring-1 focus-visible:ring-accent/50"
    >
      {/* Cumulative depth bar, filling from the right edge inward. */}
      <motion.span
        aria-hidden="true"
        className={`absolute inset-y-0 right-0 ${isBid ? "bg-up-solid/14" : "bg-down-solid/14"}`}
        initial={false}
        animate={{ width: `${width}%` }}
        transition={{ type: "spring", stiffness: 260, damping: 32 }}
      />

      {/* Transient wash when size changes. */}
      <AnimatePresence>
        {flash ? (
          <motion.span
            key={flash}
            aria-hidden="true"
            initial={{ opacity: 0.42 }}
            animate={{ opacity: 0 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.38 }}
            className={`absolute inset-0 ${flash === "up" ? "bg-up-solid/30" : "bg-down-solid/30"}`}
          />
        ) : null}
      </AnimatePresence>

      <span
        className={`relative z-10 text-[10.5px] ${isBid ? "text-up" : "text-down"}`}
      >
        {formatPrice(level.price)}
      </span>
      <span className="relative z-10 text-right text-[10.5px] text-txt">
        {formatSize(level.quantity)}
      </span>
      <span className="relative z-10 text-right text-[10.5px] text-txt-dim">
        {formatSize(level.cumulative)}
      </span>
    </motion.button>
  );
}

/** Blank rows so the asks block always occupies the same height. */
function Filler({ count }: { count: number }) {
  if (count <= 0) return null;
  return (
    <div aria-hidden="true" style={{ height: count * 18 }} className="shrink-0" />
  );
}

interface OrderBookPanelProps {
  depth: DepthSnapshot | null;
  /** Last traded price, shown in the centre block. */
  lastPrice: number | null;
  onSelectPrice?: (price: number, side: OrderSide) => void;
}

export function OrderBookPanel({
  depth,
  lastPrice,
  onSelectPrice,
}: OrderBookPanelProps) {
  const bids = (depth?.bids ?? []).slice(0, VISIBLE_LEVELS);
  const asks = (depth?.asks ?? []).slice(0, VISIBLE_LEVELS);

  // Both sides share a scale so the bars are visually comparable.
  const max = Math.max(
    bids.length > 0 ? bids[bids.length - 1].cumulative : 0,
    asks.length > 0 ? asks[asks.length - 1].cumulative : 0,
    1,
  );

  const mid = depth?.mid ?? null;
  const spread = depth?.spread ?? null;
  const priceTone = lastPrice != null && mid != null && lastPrice >= mid ? "up" : "down";

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="panel-head justify-between">
        <span>Order Book</span>
        <span className="text-[10px] text-txt-faint" aria-hidden="true">
          ☰
        </span>
      </header>

      {/* Column headings */}
      <div className="grid shrink-0 grid-cols-[1fr_1fr_1fr] border-b border-line px-2.5 py-1">
        <span className="col-head">Price</span>
        <span className="col-head text-right">Size</span>
        <span className="col-head text-right">Total</span>
      </div>

      {/* Asks, best offer sitting immediately above the spread block. */}
      <div className="flex flex-1 flex-col justify-end overflow-hidden">
        <Filler count={VISIBLE_LEVELS - asks.length} />
        <AnimatePresence initial={false}>
          {[...asks].reverse().map((level) => (
            <LadderRow
              key={`ask-${level.price}`}
              level={level}
              side="SELL"
              max={max}
              onSelect={onSelectPrice}
            />
          ))}
        </AnimatePresence>
      </div>

      {/* Last price and spread */}
      <div className="shrink-0 border-y border-line bg-panel-head px-2.5 py-1.5">
        <div className="flex items-center justify-between">
          {lastPrice === null ? (
            <span className="text-[16px] font-semibold text-txt-faint">—</span>
          ) : (
            <RollingNumber
              value={lastPrice}
              format={(v) => formatPrice(v)}
              className={`text-[16px] font-semibold ${
                priceTone === "up" ? "text-up" : "text-down"
              }`}
            />
          )}
          <span className="text-[9.5px] text-txt-dim">
            Spread: {spread != null ? formatPrice(spread) : "—"}
          </span>
        </div>
        <span className="text-[9.5px] text-txt-faint">
          {mid != null ? `Mid $${formatPrice(mid)}` : "no two-sided quote"}
        </span>
      </div>

      {/* Bids, best bid immediately below the spread block. */}
      <div className="flex flex-1 flex-col justify-start overflow-hidden">
        <AnimatePresence initial={false}>
          {bids.map((level) => (
            <LadderRow
              key={`bid-${level.price}`}
              level={level}
              side="BUY"
              max={max}
              onSelect={onSelectPrice}
            />
          ))}
        </AnimatePresence>
        <Filler count={VISIBLE_LEVELS - bids.length} />
      </div>
    </section>
  );
}
