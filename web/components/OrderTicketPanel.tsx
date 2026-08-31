"use client";

import { AnimatePresence, motion } from "motion/react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { formatPrice } from "@/lib/format";
import type {
  AccountSnapshot,
  DepthSnapshot,
  OrderSide,
  OrderType,
  TimeInForce,
} from "@/lib/types";

export interface TicketPrefill {
  price: number;
  side: OrderSide;
  /** Bumped on every ladder click so repeat clicks on one level still apply. */
  nonce: number;
}

type SubmitState = "idle" | "submitting" | "success" | "error";

/** The design's LIMIT / MARKET / STOP tabs. */
const ORDER_TYPES: OrderType[] = ["LIMIT", "MARKET", "STOP"];

const SIZE_PRESETS = [25, 50, 75, 100] as const;

const TIF_OPTIONS: TimeInForce[] = ["GTC", "IOC", "FOK"];

const TIF_HINT: Record<TimeInForce, string> = {
  GTC: "Rests until filled or cancelled",
  IOC: "Fills now, cancels the remainder",
  FOK: "Fills entirely or not at all",
};

/**
 * Mirrors the engine's server-side checks so the operator gets inline feedback
 * instead of a round-trip and a flat error string.
 */
function validate(
  traderId: string,
  orderType: OrderType,
  price: number,
  stopPrice: number,
  quantity: number,
): string | null {
  if (!traderId.trim()) return "Trader ID is required";
  if (orderType === "LIMIT" && (!Number.isFinite(price) || price <= 0)) {
    return "Price must be greater than 0";
  }
  if (orderType === "STOP" && (!Number.isFinite(stopPrice) || stopPrice <= 0)) {
    return "Stop price is required";
  }
  if (!Number.isFinite(quantity) || quantity <= 0) {
    return "Size must be greater than 0";
  }
  if (!Number.isInteger(quantity)) return "Size must be a whole number";
  return null;
}

interface OrderTicketPanelProps {
  symbol: string | null;
  traderId: string;
  ready: boolean;
  depth: DepthSnapshot | null;
  account: AccountSnapshot | undefined;
  prefill: TicketPrefill | null;
  onSubmitted: (orderId: string) => void;
}

export function OrderTicketPanel({
  symbol,
  traderId,
  ready,
  depth,
  account,
  prefill,
  onSubmitted,
}: OrderTicketPanelProps) {
  const [side, setSide] = useState<OrderSide>("BUY");
  const [orderType, setOrderType] = useState<OrderType>("LIMIT");
  const [tif, setTif] = useState<TimeInForce>("GTC");
  const [price, setPrice] = useState("");
  const [stopPrice, setStopPrice] = useState("");
  const [size, setSize] = useState("");
  const [state, setState] = useState<SubmitState>("idle");
  const [message, setMessage] = useState<string | null>(null);
  const [shaking, setShaking] = useState(false);

  const resetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const isMarket = orderType === "MARKET";
  const isStop = orderType === "STOP";
  const isBuy = side === "BUY";

  // Base asset from "BTC/USD"; falls back to the whole symbol for equities.
  const baseAsset = symbol ? (symbol.split("/")[0] ?? symbol) : "";

  useEffect(() => {
    if (!prefill) return;
    setPrice(prefill.price.toFixed(2));
    // Clicking an ask implies buying into it, and vice versa.
    setSide(prefill.side === "SELL" ? "BUY" : "SELL");
    setOrderType("LIMIT");
  }, [prefill]);

  // Seed the price once a quote exists so the ticket is not blank on load.
  useEffect(() => {
    if (price !== "" || depth?.mid == null) return;
    setPrice(depth.mid.toFixed(2));
  }, [depth?.mid, price]);

  // A market order cannot rest, so GTC is meaningless: move to IOC, which is
  // what the engine effectively does with it anyway.
  useEffect(() => {
    if (isMarket && tif === "GTC") setTif("IOC");
  }, [isMarket, tif]);

  useEffect(
    () => () => {
      if (resetTimer.current) clearTimeout(resetTimer.current);
    },
    [],
  );

  const fail = useCallback((text: string) => {
    setState("error");
    setMessage(text);
    setShaking(true);
    setTimeout(() => setShaking(false), 420);
  }, []);

  /**
   * Reference price for sizing. A market order has no price of its own, so the
   * percent buttons size against the touch instead.
   */
  const referencePrice = (() => {
    const parsed = Number.parseFloat(price);
    if (!isMarket && Number.isFinite(parsed) && parsed > 0) return parsed;
    if (isBuy && depth?.best_ask != null) return depth.best_ask;
    if (!isBuy && depth?.best_bid != null) return depth.best_bid;
    return depth?.mid ?? null;
  })();

  /**
   * Percent of what the account can actually put to work: cash for a buy, the
   * existing long for a sell. Without the ledger these buttons would be
   * percentages of nothing.
   */
  const applyPercent = useCallback(
    (percent: number) => {
      if (!account) return;

      if (isBuy) {
        if (referencePrice == null || referencePrice <= 0) return;
        const affordable = Math.floor(
          (account.cash * (percent / 100)) / referencePrice,
        );
        setSize(String(Math.max(0, affordable)));
        return;
      }

      const held = account.positions.find((p) => p.symbol === symbol)?.quantity ?? 0;
      if (held <= 0) {
        // Nothing long to sell down; fall back to sizing against cash so the
        // button still does something predictable rather than silently nothing.
        if (referencePrice == null || referencePrice <= 0) return;
        setSize(String(Math.max(0, Math.floor((account.cash * (percent / 100)) / referencePrice))));
        return;
      }
      setSize(String(Math.max(0, Math.floor(held * (percent / 100)))));
    },
    [account, isBuy, referencePrice, symbol],
  );

  const handleSubmit = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      if (state === "submitting") return;

      if (!symbol) {
        fail("No instrument selected");
        return;
      }

      const parsedPrice = isMarket ? 0 : Number.parseFloat(price);
      const parsedStop = isStop ? Number.parseFloat(stopPrice) : 0;
      const parsedSize = Number.parseInt(size, 10);

      const problem = validate(traderId, orderType, parsedPrice, parsedStop, parsedSize);
      if (problem) {
        fail(problem);
        return;
      }

      setState("submitting");
      setMessage(null);

      try {
        const res = await api.placeOrder({
          symbol,
          trader_id: traderId,
          side,
          type: orderType,
          time_in_force: tif,
          price: Number.isFinite(parsedPrice) ? parsedPrice : 0,
          stop_price: Number.isFinite(parsedStop) ? parsedStop : 0,
          quantity: parsedSize,
        });

        onSubmitted(res.order_id);
        setState("success");
        // The engine acknowledges before matching, so the response status is
        // always NEW and never reports the outcome. Word the confirmation from
        // what was asked for instead.
        setMessage(
          isStop
            ? `Stop armed · ${res.order_id.slice(0, 8)}`
            : `Accepted · ${res.order_id.slice(0, 8)}`,
        );

        resetTimer.current = setTimeout(() => {
          setState("idle");
          setMessage(null);
        }, 1800);
      } catch (err) {
        fail(err instanceof ApiError ? err.message : "Submission failed");
      }
    },
    [
      fail,
      isMarket,
      isStop,
      onSubmitted,
      orderType,
      price,
      side,
      size,
      state,
      stopPrice,
      symbol,
      tif,
      traderId,
    ],
  );

  const parsedSize = Number.parseInt(size, 10);
  const notional =
    referencePrice != null && Number.isFinite(parsedSize)
      ? referencePrice * parsedSize
      : null;

  return (
    <section className="panel flex min-h-0 flex-col overflow-hidden">
      <header className="panel-head justify-between gap-2">
        <span>Order Ticket</span>
        {account ? (
          <span className="text-[9px] normal-case tracking-normal text-txt-faint">
            ${formatPrice(account.cash)} free
          </span>
        ) : null}
      </header>

      <form
        onSubmit={handleSubmit}
        className={`flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto p-2.5 ${
          shaking ? "animate-shake" : ""
        }`}
      >
        {/* Side */}
        <div className="grid grid-cols-2 gap-px border border-line bg-line">
          {(["BUY", "SELL"] as const).map((option) => {
            const active = side === option;
            return (
              <button
                key={option}
                type="button"
                onClick={() => setSide(option)}
                aria-pressed={active}
                className={`relative py-[5px] text-[10.5px] font-semibold tracking-[0.08em] transition-colors ${
                  active ? "text-bg" : "bg-panel text-txt-dim hover:text-txt"
                }`}
              >
                {active ? (
                  <motion.span
                    layoutId="ticket-side"
                    className={`absolute inset-0 ${option === "BUY" ? "bg-up" : "bg-down"}`}
                    transition={{ type: "spring", stiffness: 420, damping: 34 }}
                  />
                ) : null}
                <span className="relative z-10">{option}</span>
              </button>
            );
          })}
        </div>

        {/* Order type tabs */}
        <div className="flex items-center gap-3 border-b border-line pb-1">
          {ORDER_TYPES.map((option) => {
            const active = orderType === option;
            return (
              <button
                key={option}
                type="button"
                onClick={() => setOrderType(option)}
                aria-pressed={active}
                className={`relative pb-1 text-[10px] tracking-[0.06em] transition-colors ${
                  active ? "text-txt" : "text-txt-faint hover:text-txt-dim"
                }`}
              >
                {active ? (
                  <motion.span
                    layoutId="ticket-type"
                    className="absolute inset-x-0 -bottom-[5px] h-[1.5px] bg-accent"
                    transition={{ type: "spring", stiffness: 400, damping: 32 }}
                  />
                ) : null}
                {option}
              </button>
            );
          })}
        </div>

        {/* Stop trigger, only meaningful for STOP */}
        {isStop ? (
          <motion.label
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: "auto" }}
            className="flex items-center justify-between gap-2 overflow-hidden border border-line bg-panel-head px-2 py-1.5"
          >
            <span className="col-head shrink-0">Stop</span>
            <input
              type="number"
              inputMode="decimal"
              step="0.01"
              min="0.01"
              value={stopPrice}
              onChange={(e) => setStopPrice(e.target.value)}
              placeholder="trigger"
              aria-label="Stop trigger price"
              className="w-full bg-transparent text-right text-[12px] text-txt outline-none placeholder:text-txt-faint"
            />
          </motion.label>
        ) : null}

        {/* Price */}
        <label className="flex items-center justify-between gap-2 border border-line bg-panel-head px-2 py-1.5">
          <span className="col-head shrink-0">Price</span>
          <input
            type="number"
            inputMode="decimal"
            step="0.01"
            min="0.01"
            disabled={isMarket}
            value={isMarket ? "" : price}
            onChange={(e) => setPrice(e.target.value)}
            placeholder={isMarket ? "market" : "0.00"}
            aria-label="Limit price"
            className="w-full bg-transparent text-right text-[12px] text-txt outline-none placeholder:text-txt-faint disabled:cursor-not-allowed"
          />
        </label>

        {/* Size */}
        <label className="flex items-center justify-between gap-2 border border-line bg-panel-head px-2 py-1.5">
          <span className="col-head shrink-0">Size</span>
          <input
            type="number"
            inputMode="numeric"
            step="1"
            min="1"
            value={size}
            onChange={(e) => setSize(e.target.value)}
            placeholder="0"
            aria-label="Order size"
            className="w-full bg-transparent text-right text-[12px] text-txt outline-none placeholder:text-txt-faint"
          />
        </label>

        {/* Percent of available balance */}
        <div className="grid grid-cols-4 gap-1">
          {SIZE_PRESETS.map((percent) => (
            <button
              key={percent}
              type="button"
              onClick={() => applyPercent(percent)}
              disabled={!account}
              title={
                isBuy
                  ? `${percent}% of free cash at the reference price`
                  : `${percent}% of the position held`
              }
              className="border border-line bg-panel py-[3px] text-[9.5px] text-txt-dim transition-colors hover:border-txt-faint hover:text-txt disabled:cursor-not-allowed disabled:opacity-40"
            >
              {percent}%
            </button>
          ))}
        </div>

        {/* Time in force */}
        <div className="flex items-center gap-1">
          {TIF_OPTIONS.map((option) => {
            const active = tif === option;
            // A market order has no price, so it can never rest.
            const disabled = isMarket && option === "GTC";
            return (
              <button
                key={option}
                type="button"
                disabled={disabled}
                onClick={() => setTif(option)}
                aria-pressed={active}
                title={disabled ? "A market order cannot rest" : TIF_HINT[option]}
                className={`flex-1 border py-[3px] text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-30 ${
                  active
                    ? "border-txt-faint bg-panel-raised text-txt"
                    : "border-line bg-panel text-txt-faint hover:text-txt-dim"
                }`}
              >
                {option}
              </button>
            );
          })}
        </div>

        {/* Total */}
        <div className="flex items-center justify-between border-t border-line pt-2">
          <span className="col-head">Total</span>
          <span className="text-[11px] text-txt">
            {notional != null ? `~ ${formatPrice(notional)} USD` : "~ 0.00 USD"}
          </span>
        </div>

        <button
          type="submit"
          disabled={!ready || !symbol || state === "submitting"}
          className={`relative mt-auto h-9 shrink-0 overflow-hidden text-[12px] font-semibold text-bg transition-opacity disabled:opacity-50 ${
            isBuy ? "bg-up hover:bg-up-solid" : "bg-down hover:bg-down-solid"
          }`}
        >
          <AnimatePresence mode="popLayout" initial={false}>
            <motion.span
              key={state === "submitting" ? "busy" : state === "success" ? "done" : "idle"}
              initial={{ y: 12, opacity: 0 }}
              animate={{ y: 0, opacity: 1 }}
              exit={{ y: -12, opacity: 0 }}
              transition={{ duration: 0.18 }}
              className="absolute inset-0 flex items-center justify-center gap-2"
            >
              {state === "submitting" ? (
                <>
                  <span className="h-3 w-3 animate-spin rounded-full border-2 border-bg/40 border-t-bg" />
                  Working
                </>
              ) : state === "success" ? (
                <>✓ Accepted</>
              ) : (
                <>
                  {isBuy ? "Buy" : "Sell"} {baseAsset || "—"}
                </>
              )}
            </motion.span>
          </AnimatePresence>
        </button>

        <AnimatePresence>
          {message ? (
            <motion.p
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              className={`shrink-0 overflow-hidden text-[9.5px] ${
                state === "error" ? "text-down" : "text-up"
              }`}
            >
              {message}
            </motion.p>
          ) : null}
        </AnimatePresence>
      </form>
    </section>
  );
}
