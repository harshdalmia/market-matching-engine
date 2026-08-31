"use client";

import { AnimatePresence, motion } from "motion/react";
import { useState } from "react";
import {
  formatCompact,
  formatLatency,
  formatPrice,
  formatSigned,
} from "@/lib/format";
import type { AccountSnapshot, MetricsResponse } from "@/lib/types";

interface StatusBarProps {
  account: AccountSnapshot | undefined;
  metrics: MetricsResponse | undefined;
  openOrderCount: number;
  traderId: string;
}

/**
 * The footer strip. Left side summarises the account, right side reports engine
 * health straight from /metrics.
 */
export function StatusBar({
  account,
  metrics,
  openOrderCount,
  traderId,
}: StatusBarProps) {
  const [assetsOpen, setAssetsOpen] = useState(false);

  const positions = (account?.positions ?? []).filter((p) => p.quantity !== 0);
  const pnl = account?.total_pnl ?? 0;

  return (
    <footer className="relative flex h-7 shrink-0 items-center gap-4 border-t border-line bg-panel px-3 text-[9.5px] tracking-[0.06em]">
      <span className="text-txt-dim">
        POSITIONS <span className="text-txt">({positions.length})</span>
      </span>
      <span className="text-txt-dim">
        OPEN ORDERS <span className="text-txt">({openOrderCount})</span>
      </span>
      <span className="text-txt-dim">
        HISTORY <span className="text-txt">({account?.open_orders ?? 0})</span>
      </span>

      {/* Assets popover: the cash ledger the percent buttons size against. */}
      <div className="relative">
        <button
          type="button"
          onClick={() => setAssetsOpen((v) => !v)}
          aria-expanded={assetsOpen}
          className={`transition-colors ${assetsOpen ? "text-accent" : "text-txt-dim hover:text-txt"}`}
        >
          ASSETS
        </button>

        <AnimatePresence>
          {assetsOpen ? (
            <motion.div
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: 6 }}
              transition={{ duration: 0.16 }}
              className="absolute bottom-[26px] left-0 z-50 w-[280px] border border-line bg-panel p-2.5 shadow-2xl shadow-black/70"
            >
              <p className="col-head mb-2">Account · {traderId}</p>

              <dl className="flex flex-col gap-1 text-[10.5px] tracking-normal">
                <div className="flex justify-between">
                  <dt className="text-txt-faint">Cash</dt>
                  <dd className="text-txt">
                    {account ? formatPrice(account.cash) : "—"}
                  </dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-txt-faint">Positions value</dt>
                  <dd className="text-txt">
                    {account ? formatPrice(account.position_value) : "—"}
                  </dd>
                </div>
                <div className="flex justify-between border-t border-line pt-1">
                  <dt className="text-txt-faint">Equity</dt>
                  <dd className="text-txt">
                    {account ? formatPrice(account.equity) : "—"}
                  </dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-txt-faint">Realised</dt>
                  <dd className={(account?.realized_pnl ?? 0) >= 0 ? "text-up" : "text-down"}>
                    {account ? formatSigned(account.realized_pnl) : "—"}
                  </dd>
                </div>
                <div className="flex justify-between">
                  <dt className="text-txt-faint">Unrealised</dt>
                  <dd className={(account?.unrealized_pnl ?? 0) >= 0 ? "text-up" : "text-down"}>
                    {account ? formatSigned(account.unrealized_pnl) : "—"}
                  </dd>
                </div>
                <div className="flex justify-between border-t border-line pt-1">
                  <dt className="text-txt-faint">Total P&amp;L</dt>
                  <dd className={pnl >= 0 ? "text-up" : "text-down"}>
                    {account ? formatSigned(pnl) : "—"}
                  </dd>
                </div>
              </dl>

              <p className="mt-2 border-t border-line pt-1.5 text-[9px] leading-relaxed tracking-normal text-txt-faint">
                Balances are observational: the engine does not check buying power,
                so a position can exceed the cash that funded it.
              </p>
            </motion.div>
          ) : null}
        </AnimatePresence>
      </div>

      {account ? (
        <span className="text-txt-dim">
          P&amp;L{" "}
          <span className={pnl >= 0 ? "text-up" : "text-down"}>
            {formatSigned(pnl)}
          </span>
        </span>
      ) : null}

      {/* Engine health */}
      <div className="ml-auto flex items-center gap-4">
        <span className="text-txt-dim">
          LATENCY{" "}
          <span className="text-up">
            {metrics ? formatLatency(metrics.avg_latency_ms) : "—"}
          </span>
        </span>
        <span className="text-txt-dim">
          QUEUED <span className="text-txt">{metrics?.orders_queued ?? 0}</span>
        </span>
        <span className="text-txt-dim">
          ENGINE TOTAL{" "}
          <span className="text-txt">
            {metrics ? formatCompact(metrics.total_orders_processed) : "—"} ORDERS
          </span>
        </span>
        <span className="text-txt-dim">
          <span className="text-txt">
            {metrics ? formatCompact(metrics.trade_count) : "—"}
          </span>{" "}
          TRADES
        </span>
      </div>
    </footer>
  );
}
