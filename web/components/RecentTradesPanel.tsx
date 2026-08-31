"use client";

import { AnimatePresence, motion } from "motion/react";
import type { TapePrint } from "@/lib/book";
import { formatCompact, formatPrice, formatSize, formatTime } from "@/lib/format";

/** Rows rendered. The tape is capped so a long history stays cheap to animate. */
const VISIBLE_PRINTS = 60;

function toneFor(direction: TapePrint["direction"]): string {
  if (direction === "up") return "text-up";
  if (direction === "down") return "text-down";
  return "text-txt-dim";
}

interface RecentTradesPanelProps {
  /** Newest print first. */
  prints: TapePrint[];
  total: number;
  ownOrderIds: ReadonlySet<string>;
}

export function RecentTradesPanel({
  prints,
  total,
  ownOrderIds,
}: RecentTradesPanelProps) {
  const visible = prints.slice(0, VISIBLE_PRINTS);

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="panel-head justify-between gap-2">
        <span>Recent Trades</span>
        {total > 0 ? (
          <span className="text-[9px] normal-case tracking-normal text-txt-faint">
            {formatCompact(total)} prints
          </span>
        ) : null}
      </header>

      <div className="grid shrink-0 grid-cols-[1fr_1fr_1fr] border-b border-line px-2.5 py-1">
        <span className="col-head">Price</span>
        <span className="col-head text-right">Size</span>
        <span className="col-head text-right">Time</span>
      </div>

      {visible.length === 0 ? (
        <div className="flex flex-1 items-center justify-center p-4">
          <p className="max-w-[28ch] text-center text-[10.5px] leading-relaxed text-txt-faint">
            No executions yet. Trades print here the moment a buy and sell cross.
          </p>
        </div>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto">
          <AnimatePresence initial={false}>
            {visible.map((print) => {
              const isOwn =
                ownOrderIds.has(print.buy_order_id) ||
                ownOrderIds.has(print.sell_order_id);

              return (
                <motion.li
                  key={print.id}
                  layout="position"
                  initial={{ opacity: 0, y: -10 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={{ opacity: 0 }}
                  transition={{ duration: 0.28, ease: [0.16, 1, 0.3, 1] }}
                  className={`grid h-[18px] grid-cols-[1fr_1fr_1fr] items-center px-2.5 ${
                    isOwn ? "bg-accent/[0.07]" : ""
                  }`}
                >
                  <span className={`text-[10.5px] ${toneFor(print.direction)}`}>
                    {formatPrice(print.price)}
                  </span>
                  <span className="text-right text-[10.5px] text-txt">
                    {formatSize(print.quantity)}
                  </span>
                  <span className="text-right text-[10.5px] text-txt-dim">
                    {formatTime(print.timestamp)}
                  </span>
                </motion.li>
              );
            })}
          </AnimatePresence>
        </ul>
      )}
    </section>
  );
}
