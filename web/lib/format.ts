const NS_PER_MS = 1_000_000;

/** Engine timestamps are Unix nanoseconds; JS Dates take milliseconds. */
export function nsToDate(ns: number): Date {
  return new Date(ns / NS_PER_MS);
}

export function formatPrice(value: number, dp = 2): string {
  return value.toLocaleString("en-US", {
    minimumFractionDigits: dp,
    maximumFractionDigits: dp,
  });
}

/** Sizes are integers on the wire but read better with a fixed decimal tail. */
export function formatSize(value: number, dp = 3): string {
  return value.toLocaleString("en-US", {
    minimumFractionDigits: dp,
    maximumFractionDigits: dp,
  });
}

export function formatQty(value: number): string {
  return value.toLocaleString("en-US");
}

/** Compact form for large counters, e.g. 1_200_000 -> "1.2M". */
export function formatCompact(value: number): string {
  if (Math.abs(value) < 1000) return String(Math.round(value));
  return value
    .toLocaleString("en-US", { notation: "compact", maximumFractionDigits: 1 })
    .toUpperCase();
}

/** Signed value with an explicit plus, for changes and PnL. */
export function formatSigned(value: number, dp = 2): string {
  const sign = value > 0 ? "+" : value < 0 ? "-" : "";
  return `${sign}${formatPrice(Math.abs(value), dp)}`;
}

export function formatPercent(value: number, dp = 2): string {
  const sign = value > 0 ? "+" : value < 0 ? "-" : "";
  return `${sign}${Math.abs(value).toFixed(dp)}%`;
}

/** HH:MM:SS in local time — the tape's TIME column. */
export function formatTime(ns: number): string {
  const d = nsToDate(ns);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** HH:MM:SS.mmm for anything that needs sub-second resolution. */
export function formatTimeMs(ns: number): string {
  const d = nsToDate(ns);
  const pad = (n: number, len = 2) => String(n).padStart(len, "0");
  return `${formatTime(ns)}.${pad(d.getMilliseconds(), 3)}`;
}

/**
 * Sub-millisecond latencies are the norm here, so switch to microseconds rather
 * than rendering a string of leading zeros.
 */
export function formatLatency(ms: number): string {
  if (ms <= 0) return "—";
  if (ms < 1) return `${(ms * 1000).toFixed(0)}\u00b5s`;
  return `${ms.toFixed(1)}ms`;
}

export function formatUptime(seconds: number): string {
  const s = Math.floor(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

/** Percentage of a whole, clamped to 0–100 and safe against a zero total. */
export function percentOf(part: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(100, Math.max(0, (part / total) * 100));
}
