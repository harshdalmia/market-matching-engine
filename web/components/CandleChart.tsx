"use client";

import { motion } from "motion/react";
import { useMemo } from "react";
import { formatPrice, formatTime } from "@/lib/format";
import { CANDLE_INTERVALS, type Candle, type CandleInterval } from "@/lib/types";

/**
 * The SVG uses a fixed coordinate space and stretches to its container.
 * preserveAspectRatio="none" would distort strokes, so every stroked element
 * carries vector-effect="non-scaling-stroke".
 */
const W = 900;
const H = 420;
const PAD = { top: 12, right: 58, bottom: 18, left: 10 };
const PLOT_W = W - PAD.left - PAD.right;
const PLOT_H = H - PAD.top - PAD.bottom;

/** Horizontal gridlines and the price axis on the right. */
function Grid({
  low,
  high,
  rows = 5,
}: {
  low: number;
  high: number;
  rows?: number;
}) {
  return (
    <g aria-hidden="true">
      {Array.from({ length: rows + 1 }).map((_, i) => {
        const y = PAD.top + (PLOT_H / rows) * i;
        const price = high - ((high - low) / rows) * i;

        return (
          <g key={i}>
            <line
              x1={PAD.left}
              x2={PAD.left + PLOT_W}
              y1={y}
              y2={y}
              stroke="currentColor"
              className="text-line-soft"
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
            />
            <text
              x={PAD.left + PLOT_W + 6}
              y={y + 3}
              className="fill-txt-faint"
              style={{ fontSize: 9 }}
            >
              {formatPrice(price)}
            </text>
          </g>
        );
      })}
    </g>
  );
}

interface CandleChartProps {
  candles: Candle[];
  interval: CandleInterval;
  onIntervalChange: (interval: CandleInterval) => void;
  /** Streamed last price, drawn as a live marker line above the candles. */
  livePrice: number | null;
  loading: boolean;
}

export function CandleChart({
  candles,
  interval,
  onIntervalChange,
  livePrice,
  loading,
}: CandleChartProps) {
  const geometry = useMemo(() => {
    if (candles.length === 0) return null;

    let low = candles[0].low;
    let high = candles[0].high;
    for (const c of candles) {
      if (c.low < low) low = c.low;
      if (c.high > high) high = c.high;
    }
    if (livePrice != null && livePrice > 0) {
      low = Math.min(low, livePrice);
      high = Math.max(high, livePrice);
    }

    // Pad the domain so a flat series does not collapse onto a single line.
    const span = Math.max(high - low, 0.02);
    const lo = low - span * 0.08;
    const hi = high + span * 0.08;

    const slot = PLOT_W / candles.length;
    // Leave a gap between candles, and keep them legible when there are few.
    const bodyWidth = Math.max(1.5, Math.min(slot * 0.66, 18));

    const y = (price: number) =>
      PAD.top + PLOT_H - ((price - lo) / (hi - lo)) * PLOT_H;

    const bars = candles.map((candle, i) => {
      const centre = PAD.left + slot * i + slot / 2;
      const openY = y(candle.open);
      const closeY = y(candle.close);
      const rising = candle.close >= candle.open;

      return {
        key: `${candle.open_time}`,
        centre,
        rising,
        wickTop: y(candle.high),
        wickBottom: y(candle.low),
        bodyTop: Math.min(openY, closeY),
        // A doji would otherwise render as nothing at all.
        bodyHeight: Math.max(1, Math.abs(closeY - openY)),
        candle,
      };
    });

    return { bars, bodyWidth, low: lo, high: hi, y };
  }, [candles, livePrice]);

  const last = candles.length > 0 ? candles[candles.length - 1] : null;

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="panel-head justify-between gap-3">
        <div className="flex items-center gap-4">
          <span>Chart</span>
          <div className="flex items-center gap-0.5">
            {CANDLE_INTERVALS.map((option) => {
              const active = option === interval;
              return (
                <button
                  key={option}
                  type="button"
                  onClick={() => onIntervalChange(option)}
                  aria-pressed={active}
                  className={`relative px-1.5 py-0.5 text-[9.5px] uppercase tracking-[0.06em] transition-colors ${
                    active ? "text-txt" : "text-txt-faint hover:text-txt-dim"
                  }`}
                >
                  {active ? (
                    <motion.span
                      layoutId="chart-interval-underline"
                      className="absolute inset-x-0 -bottom-[3px] h-[1.5px] bg-accent"
                      transition={{ type: "spring", stiffness: 380, damping: 32 }}
                    />
                  ) : null}
                  {option}
                </button>
              );
            })}
          </div>
        </div>

        {last ? (
          <span className="flex items-center gap-3 text-[9.5px] normal-case tracking-normal text-txt-faint">
            <span>O {formatPrice(last.open)}</span>
            <span>H {formatPrice(last.high)}</span>
            <span>L {formatPrice(last.low)}</span>
            <span className={last.close >= last.open ? "text-up" : "text-down"}>
              C {formatPrice(last.close)}
            </span>
          </span>
        ) : null}
      </header>

      <div className="relative min-h-0 flex-1">
        {geometry === null ? (
          <div className="flex h-full items-center justify-center">
            <p className="max-w-[36ch] text-center text-[10.5px] leading-relaxed text-txt-faint">
              {loading
                ? "Loading candles…"
                : `No trades in this window. Candles appear as soon as orders cross — try a shorter interval, or start simulated flow.`}
            </p>
          </div>
        ) : (
          <svg
            viewBox={`0 0 ${W} ${H}`}
            preserveAspectRatio="none"
            className="h-full w-full"
            role="img"
            aria-label={`Candlestick chart at ${interval} intervals`}
          >
            <Grid low={geometry.low} high={geometry.high} />

            {geometry.bars.map((bar, index) => {
              const colour = bar.rising ? "var(--color-up)" : "var(--color-down)";

              return (
                <motion.g
                  key={bar.key}
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  // Only the newest candle is worth animating in; replaying the
                  // whole series on every poll would strobe.
                  transition={{
                    duration: 0.25,
                    delay: index === geometry.bars.length - 1 ? 0 : 0,
                  }}
                >
                  <line
                    x1={bar.centre}
                    x2={bar.centre}
                    y1={bar.wickTop}
                    y2={bar.wickBottom}
                    stroke={colour}
                    strokeWidth={1}
                    vectorEffect="non-scaling-stroke"
                  />
                  <rect
                    x={bar.centre - geometry.bodyWidth / 2}
                    y={bar.bodyTop}
                    width={geometry.bodyWidth}
                    height={bar.bodyHeight}
                    fill={colour}
                  />
                </motion.g>
              );
            })}

            {/* Live price marker, driven by the event stream rather than the poll. */}
            {livePrice != null && livePrice > 0 ? (
              <g>
                <motion.line
                  x1={PAD.left}
                  x2={PAD.left + PLOT_W}
                  y1={geometry.y(livePrice)}
                  y2={geometry.y(livePrice)}
                  stroke="var(--color-accent)"
                  strokeWidth={1}
                  strokeDasharray="4 4"
                  vectorEffect="non-scaling-stroke"
                  initial={false}
                  animate={{ y1: geometry.y(livePrice), y2: geometry.y(livePrice) }}
                  transition={{ type: "spring", stiffness: 200, damping: 30 }}
                  opacity={0.7}
                />
                <rect
                  x={PAD.left + PLOT_W + 2}
                  y={geometry.y(livePrice) - 7}
                  width={PAD.right - 4}
                  height={14}
                  fill="var(--color-accent)"
                />
                <text
                  x={PAD.left + PLOT_W + 6}
                  y={geometry.y(livePrice) + 3}
                  className="fill-bg"
                  style={{ fontSize: 9, fontWeight: 600 }}
                >
                  {formatPrice(livePrice)}
                </text>
              </g>
            ) : null}
          </svg>
        )}
      </div>

      {/* Time axis footer */}
      {candles.length > 0 ? (
        <div className="flex shrink-0 items-center justify-between border-t border-line px-2.5 py-1 text-[9px] text-txt-faint">
          <span>{formatTime(candles[0].open_time)}</span>
          <span>
            {candles.length} candle{candles.length === 1 ? "" : "s"} · {interval}
          </span>
          <span>{formatTime(candles[candles.length - 1].close_time)}</span>
        </div>
      ) : null}
    </section>
  );
}
