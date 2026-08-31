"use client";

import { motion } from "motion/react";
import type { ReactNode } from "react";
import {
  formatCompact,
  formatPercent,
  formatPrice,
  formatSigned,
} from "@/lib/format";
import type { ConnectionStatus } from "@/lib/stream";
import type { MarketStats, SymbolStats } from "@/lib/types";
import { RollingNumber } from "./RollingNumber";
import { changeTone, HeaderStat, SymbolDropdown } from "./SymbolDropdown";

const STATUS_LABEL: Record<ConnectionStatus, string> = {
  connecting: "CONNECTING",
  live: "CONNECTED",
  reconnecting: "RECONNECTING",
  offline: "DISCONNECTED",
};

/** Green only when the stream is genuinely open, not merely attempted. */
function statusColour(status: ConnectionStatus): string {
  if (status === "live") return "text-up";
  if (status === "offline") return "text-down";
  return "text-warn";
}

function statusDot(status: ConnectionStatus): string {
  if (status === "live") return "bg-up";
  if (status === "offline") return "bg-down";
  return "bg-warn";
}

function ConnectionPill({ status }: { status: ConnectionStatus }) {
  return (
    <div className="flex items-center gap-1.5 border border-line bg-panel-raised px-2 py-[3px]">
      <span className="relative inline-flex h-1.5 w-1.5">
        {status === "live" ? (
          <span
            aria-hidden="true"
            className={`absolute inset-0 animate-ping-slow rounded-full ${statusDot(status)}`}
          />
        ) : null}
        <span className={`relative h-1.5 w-1.5 rounded-full ${statusDot(status)}`} />
      </span>
      <span className={`text-[9px] tracking-[0.1em] ${statusColour(status)}`}>
        {STATUS_LABEL[status]}
      </span>
    </div>
  );
}

/** Flat glyph buttons on the right of the bar. */
function IconButton({
  label,
  children,
  onClick,
  active = false,
}: {
  label: string;
  children: ReactNode;
  onClick?: () => void;
  active?: boolean;
}) {
  return (
    <motion.button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      whileTap={{ scale: 0.92 }}
      className={`flex h-6 w-6 items-center justify-center border border-line text-[11px] transition-colors ${
        active
          ? "bg-panel-raised text-accent"
          : "bg-panel text-txt-dim hover:border-txt-faint hover:text-txt"
      }`}
    >
      {children}
    </motion.button>
  );
}

interface TopBarProps {
  symbols: string[];
  activeSymbol: string | null;
  symbolStats: SymbolStats[];
  onSelectSymbol: (symbol: string) => void;
  stats: MarketStats | undefined;
  /** Last price from the live stream, which leads the polled stats. */
  livePrice: number | null;
  status: ConnectionStatus;
  simEnabled: boolean;
  onToggleSim: () => void;
  traderId: string;
  onCycleTrader: () => void;
}

export function TopBar({
  symbols,
  activeSymbol,
  symbolStats,
  onSelectSymbol,
  stats,
  livePrice,
  status,
  simEnabled,
  onToggleSim,
  traderId,
  onCycleTrader,
}: TopBarProps) {
  // Prefer the streamed price: it updates on every print, while stats are polled.
  const price = livePrice ?? stats?.last ?? null;
  const change = stats?.change ?? 0;
  const changePct = stats?.change_percent ?? 0;
  const tone = changeTone(change);

  return (
    <header className="flex h-12 shrink-0 items-center gap-4 border-b border-line bg-panel px-3">
      {/* Brand */}
      <div className="flex shrink-0 items-center gap-2">
        <span
          aria-hidden="true"
          className="flex h-[18px] w-[18px] items-center justify-center border border-txt-faint text-[9px] text-txt-dim"
        >
          ▤
        </span>
        <span className="text-[12.5px] font-semibold tracking-[0.06em]">
          TERMINAL
        </span>
      </div>

      <SymbolDropdown
        symbols={symbols}
        active={activeSymbol}
        stats={symbolStats}
        onSelect={onSelectSymbol}
      />

      <div className="h-6 w-px shrink-0 bg-line" aria-hidden="true" />

      {/* Market statistics */}
      <div className="flex items-center gap-5 overflow-hidden">
        <div className="flex flex-col gap-[3px]">
          <span className="col-head">Price</span>
          {price === null ? (
            <span className="text-[11.5px] font-semibold leading-none text-txt-faint">
              —
            </span>
          ) : (
            <RollingNumber
              value={price}
              format={(v) => `$${formatPrice(v)}`}
              className={`text-[11.5px] font-semibold leading-none ${
                tone === "up" ? "text-up" : "text-down"
              }`}
            />
          )}
        </div>

        <HeaderStat
          label="24h Change"
          tone={tone}
          value={
            stats && stats.trade_count > 0
              ? `${formatPercent(changePct)} ${formatSigned(change)}`
              : "—"
          }
        />

        <HeaderStat
          label="Volume"
          value={stats ? `${formatCompact(stats.quote_volume)}` : "—"}
        />

        <HeaderStat
          label="High / Low"
          value={
            stats && stats.trade_count > 0
              ? `${formatPrice(stats.high)} / ${formatPrice(stats.low)}`
              : "—"
          }
        />
      </div>

      {/* Right-hand controls */}
      <div className="ml-auto flex shrink-0 items-center gap-2">
        <ConnectionPill status={status} />

        <IconButton
          label={simEnabled ? "Stop simulated order flow" : "Start simulated order flow"}
          onClick={onToggleSim}
          active={simEnabled}
        >
          {simEnabled ? "■" : "▶"}
        </IconButton>

        <IconButton label={`Trader ${traderId} — click for a new identity`} onClick={onCycleTrader}>
          ◑
        </IconButton>
      </div>
    </header>
  );
}
