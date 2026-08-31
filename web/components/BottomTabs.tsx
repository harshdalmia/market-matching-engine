"use client";

import { AnimatePresence, motion } from "motion/react";
import { useCallback, useState } from "react";
import { api, ApiError } from "@/lib/api";
import {
  formatPrice,
  formatSigned,
  formatSize,
  formatTime,
  percentOf,
} from "@/lib/format";
import type { AccountSnapshot, Order, TraderOrders } from "@/lib/types";

type TabId = "open" | "positions" | "history";

/** Grid template shared by the header row and the body rows of each table. */
const ORDER_COLUMNS =
  "grid-cols-[80px_60px_110px_1fr_1fr_70px_110px_80px]";
const POSITION_COLUMNS = "grid-cols-[110px_90px_1fr_1fr_1fr_1fr_1fr]";

function statusTone(status: Order["status"]): string {
  switch (status) {
    case "PARTIALLY_FILLED":
      return "text-warn";
    case "FILLED":
      return "text-up";
    case "PENDING":
      return "text-accent";
    case "CANCELLED":
    case "REJECTED":
      return "text-down";
    default:
      return "text-txt-dim";
  }
}

function statusLabel(order: Order): string {
  if (order.status === "PENDING") return "Armed";
  if (order.status === "PARTIALLY_FILLED") return "Partial";
  if (order.status === "NEW") return "Open";
  return order.status.charAt(0) + order.status.slice(1).toLowerCase();
}

/** Price column: market orders have none, stops show their trigger. */
function priceLabel(order: Order): string {
  if (order.type === "STOP" && order.status === "PENDING") {
    return `⇥ ${formatPrice(order.stop_price)}`;
  }
  if (order.type === "MARKET" || order.price <= 0) return "MKT";
  return formatPrice(order.price);
}

function OrderRow({
  order,
  onCancel,
  cancelling,
}: {
  order: Order;
  onCancel?: (id: string) => void;
  cancelling: boolean;
}) {
  const filled = order.quantity - order.remaining;
  const fillPct = percentOf(filled, order.quantity);

  return (
    <motion.li
      layout="position"
      initial={{ opacity: 0, y: -6 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.2 }}
      className={`relative grid ${ORDER_COLUMNS} h-[22px] items-center border-b border-line-soft px-2.5 text-[10.5px] last:border-b-0`}
    >
      {/* Fill progress as a subtle underlay. */}
      {filled > 0 ? (
        <motion.span
          aria-hidden="true"
          className="absolute inset-y-0 left-0 bg-accent/[0.06]"
          initial={{ width: 0 }}
          animate={{ width: `${fillPct}%` }}
          transition={{ type: "spring", stiffness: 200, damping: 30 }}
        />
      ) : null}

      <span className="relative z-10 text-txt-dim">{formatTime(order.timestamp)}</span>
      <span
        className={`relative z-10 ${order.side === "BUY" ? "text-up" : "text-down"}`}
      >
        {order.side}
      </span>
      <span className="relative z-10 text-txt-dim">{order.symbol}</span>
      <span className="relative z-10 text-txt">{priceLabel(order)}</span>
      <span className="relative z-10 text-txt">{formatSize(order.quantity)}</span>
      <span className="relative z-10 text-txt-dim">{fillPct.toFixed(0)}%</span>
      <span className={`relative z-10 ${statusTone(order.status)}`}>
        {statusLabel(order)}
        {order.time_in_force !== "GTC" ? (
          <span className="ml-1 text-txt-faint">{order.time_in_force}</span>
        ) : null}
        {/* A triggered stop is rewritten to LIMIT/MARKET but keeps its trigger,
            which is the only evidence of how it reached the book. */}
        {order.stop_price > 0 && order.status !== "PENDING" ? (
          <span
            className="ml-1 text-txt-faint"
            title={`Triggered from a stop at ${formatPrice(order.stop_price)}`}
          >
            ⇥
          </span>
        ) : null}
      </span>
      <span className="relative z-10 text-right">
        {onCancel ? (
          <button
            type="button"
            onClick={() => onCancel(order.id)}
            disabled={cancelling}
            className="border border-line px-1.5 py-[1px] text-[9px] tracking-[0.06em] text-txt-faint transition-colors hover:border-down/60 hover:text-down disabled:opacity-40"
          >
            {cancelling ? "…" : "CANCEL"}
          </button>
        ) : null}
      </span>
    </motion.li>
  );
}

interface BottomTabsProps {
  orders: TraderOrders | undefined;
  account: AccountSnapshot | undefined;
  onChanged: () => void;
}

export function BottomTabs({ orders, account, onChanged }: BottomTabsProps) {
  const [tab, setTab] = useState<TabId>("open");
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  const [note, setNote] = useState<{ text: string; ok: boolean } | null>(null);

  const open = orders?.open ?? [];
  const history = orders?.history ?? [];
  // A flat position carries realised profit worth showing, but is not exposure.
  const positions = (account?.positions ?? []).filter((p) => p.quantity !== 0);

  const cancel = useCallback(
    async (id: string) => {
      setPending((prev) => new Set(prev).add(id));
      setNote(null);

      try {
        await api.cancelOrder(id);
        setNote({ text: "Order cancelled", ok: true });
      } catch (err) {
        // A 404 is expected rather than exceptional: the order either filled
        // before the cancel landed or never rested.
        const text =
          err instanceof ApiError && err.status === 404
            ? "Already filled or no longer working"
            : err instanceof ApiError
              ? err.message
              : "Cancel failed";
        setNote({ text, ok: false });
      } finally {
        setPending((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
        onChanged();
        setTimeout(() => setNote(null), 2400);
      }
    },
    [onChanged],
  );

  const tabs: { id: TabId; label: string }[] = [
    { id: "open", label: `Open Orders (${open.length})` },
    { id: "positions", label: `Positions (${positions.length})` },
    { id: "history", label: "Order History" },
  ];

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="flex h-[26px] shrink-0 items-center gap-4 border-b border-line bg-panel-head px-2.5">
        {tabs.map((entry) => {
          const active = tab === entry.id;
          return (
            <button
              key={entry.id}
              type="button"
              onClick={() => setTab(entry.id)}
              aria-pressed={active}
              className={`relative h-full text-[9.5px] uppercase tracking-[0.1em] transition-colors ${
                active ? "text-txt" : "text-txt-faint hover:text-txt-dim"
              }`}
            >
              {active ? (
                <motion.span
                  layoutId="bottom-tab-underline"
                  className="absolute inset-x-0 bottom-0 h-[1.5px] bg-accent"
                  transition={{ type: "spring", stiffness: 400, damping: 32 }}
                />
              ) : null}
              {entry.label}
            </button>
          );
        })}

        <AnimatePresence>
          {note ? (
            <motion.span
              initial={{ opacity: 0, x: 8 }}
              animate={{ opacity: 1, x: 0 }}
              exit={{ opacity: 0 }}
              className={`ml-auto text-[9.5px] ${note.ok ? "text-up" : "text-warn"}`}
            >
              {note.text}
            </motion.span>
          ) : null}
        </AnimatePresence>
      </header>

      {tab === "positions" ? (
        <>
          <div
            className={`grid ${POSITION_COLUMNS} shrink-0 border-b border-line px-2.5 py-1`}
          >
            <span className="col-head">Market</span>
            <span className="col-head">Side</span>
            <span className="col-head">Size</span>
            <span className="col-head">Avg Entry</span>
            <span className="col-head">Mark</span>
            <span className="col-head">Unrealised</span>
            <span className="col-head">Realised</span>
          </div>

          {positions.length === 0 ? (
            <p className="p-3 text-[10.5px] text-txt-faint">
              No open positions. Fills build a position here, valued against the
              last traded price.
            </p>
          ) : (
            <ul className="min-h-0 flex-1 overflow-y-auto">
              {positions.map((position) => {
                const long = position.quantity > 0;
                return (
                  <li
                    key={position.symbol}
                    className={`grid ${POSITION_COLUMNS} h-[22px] items-center border-b border-line-soft px-2.5 text-[10.5px] last:border-b-0`}
                  >
                    <span className="text-txt-dim">{position.symbol}</span>
                    <span className={long ? "text-up" : "text-down"}>
                      {long ? "LONG" : "SHORT"}
                    </span>
                    <span className="text-txt">
                      {formatSize(Math.abs(position.quantity))}
                    </span>
                    <span className="text-txt">{formatPrice(position.avg_entry)}</span>
                    <span className="text-txt">{formatPrice(position.mark_price)}</span>
                    <span
                      className={
                        position.unrealized_pnl >= 0 ? "text-up" : "text-down"
                      }
                    >
                      {formatSigned(position.unrealized_pnl)}
                    </span>
                    <span
                      className={position.realized_pnl >= 0 ? "text-up" : "text-down"}
                    >
                      {formatSigned(position.realized_pnl)}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      ) : (
        <>
          <div
            className={`grid ${ORDER_COLUMNS} shrink-0 border-b border-line px-2.5 py-1`}
          >
            <span className="col-head">Time</span>
            <span className="col-head">Side</span>
            <span className="col-head">Market</span>
            <span className="col-head">Price</span>
            <span className="col-head">Amount</span>
            <span className="col-head">Filled</span>
            <span className="col-head">Status</span>
            <span className="col-head text-right">Action</span>
          </div>

          {(tab === "open" ? open : history).length === 0 ? (
            <p className="p-3 text-[10.5px] text-txt-faint">
              {tab === "open"
                ? "Nothing working. Orders you place appear here until they fill or you cancel them."
                : "No finished orders yet."}
            </p>
          ) : (
            <ul className="min-h-0 flex-1 overflow-y-auto">
              <AnimatePresence initial={false}>
                {(tab === "open" ? open : history).map((order) => (
                  <OrderRow
                    key={order.id}
                    order={order}
                    cancelling={pending.has(order.id)}
                    onCancel={tab === "open" ? cancel : undefined}
                  />
                ))}
              </AnimatePresence>
            </ul>
          )}
        </>
      )}
    </section>
  );
}
