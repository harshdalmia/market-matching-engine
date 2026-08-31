"use client";

import { AnimatePresence, motion } from "motion/react";
import { useEffect, useRef, useState } from "react";
import { formatPrice } from "@/lib/format";
import type { SymbolStats } from "@/lib/types";

interface SymbolDropdownProps {
  symbols: string[];
  active: string | null;
  stats: SymbolStats[];
  onSelect: (symbol: string) => void;
}

/**
 * Instrument selector.
 *
 * Each symbol is an independent venue on the engine — its own book and its own
 * matching goroutine — so switching resubscribes the event stream rather than
 * filtering data that is already loaded.
 */
export function SymbolDropdown({
  symbols,
  active,
  stats,
  onSelect,
}: SymbolDropdownProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Dismiss on an outside click or Escape, the way a native select behaves.
  useEffect(() => {
    if (!open) return;

    const onPointerDown = (event: MouseEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };

    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  const statsBySymbol = new Map(stats.map((s) => [s.symbol, s]));

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="flex h-[26px] w-[120px] items-center justify-between gap-2 border border-line bg-panel-raised px-2 text-[11.5px] font-semibold text-txt transition-colors hover:border-txt-faint"
      >
        <span>{active ?? "—"}</span>
        <motion.span
          aria-hidden="true"
          animate={{ rotate: open ? 180 : 0 }}
          transition={{ duration: 0.18 }}
          className="text-[8px] text-txt-dim"
        >
          ▼
        </motion.span>
      </button>

      <AnimatePresence>
        {open ? (
          <motion.ul
            role="listbox"
            initial={{ opacity: 0, y: -4 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -4 }}
            transition={{ duration: 0.14 }}
            className="absolute left-0 top-[30px] z-50 w-[210px] border border-line bg-panel shadow-2xl shadow-black/60"
          >
            {symbols.map((symbol) => {
              const stat = statsBySymbol.get(symbol);
              const isActive = symbol === active;

              return (
                <li key={symbol}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={isActive}
                    onClick={() => {
                      onSelect(symbol);
                      setOpen(false);
                    }}
                    className={`flex w-full items-center justify-between gap-3 border-b border-line-soft px-2.5 py-1.5 text-left transition-colors last:border-b-0 hover:bg-panel-raised ${
                      isActive ? "bg-panel-raised" : ""
                    }`}
                  >
                    <span
                      className={`text-[11px] font-semibold ${
                        isActive ? "text-accent" : "text-txt"
                      }`}
                    >
                      {symbol}
                    </span>
                    <span className="flex items-baseline gap-2">
                      <span className="text-[10.5px] text-txt-dim">
                        {stat?.last_price != null
                          ? formatPrice(stat.last_price)
                          : stat?.mid != null
                            ? formatPrice(stat.mid)
                            : "—"}
                      </span>
                      {stat ? (
                        <span className="text-[9px] text-txt-faint">
                          {stat.resting_orders}
                          {stat.pending_stops > 0 ? `+${stat.pending_stops}` : ""}
                        </span>
                      ) : null}
                    </span>
                  </button>
                </li>
              );
            })}
          </motion.ul>
        ) : null}
      </AnimatePresence>
    </div>
  );
}

/** Small labelled figure used in the header stat strip. */
export function HeaderStat({
  label,
  value,
  tone = "default",
}: {
  label: string;
  value: string;
  tone?: "default" | "up" | "down";
}) {
  const toneClass =
    tone === "up" ? "text-up" : tone === "down" ? "text-down" : "text-txt";

  return (
    <div className="flex flex-col gap-[3px]">
      <span className="col-head whitespace-nowrap">{label}</span>
      <span className={`text-[11.5px] font-semibold leading-none ${toneClass}`}>
        {value}
      </span>
    </div>
  );
}

/** Tone for a signed figure: green when flat or rising, red when falling. */
export function changeTone(change: number): "up" | "down" {
  return change >= 0 ? "up" : "down";
}
