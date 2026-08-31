"use client";

import { AnimatePresence, motion } from "motion/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BottomTabs } from "@/components/BottomTabs";
import { CandleChart } from "@/components/CandleChart";
import { MarketDepthPanel } from "@/components/MarketDepthPanel";
import { OrderBookPanel } from "@/components/OrderBookPanel";
import { OrderTicketPanel, type TicketPrefill } from "@/components/OrderTicketPanel";
import { RecentTradesPanel } from "@/components/RecentTradesPanel";
import { StatusBar } from "@/components/StatusBar";
import { TopBar } from "@/components/TopBar";
import { ENGINE_URL } from "@/lib/api";
import { annotateTrades } from "@/lib/book";
import {
  useAccount,
  useCandles,
  useMetrics,
  useStats,
  useSymbols,
  useTraderOrders,
} from "@/lib/hooks";
import { useFlowSimulator } from "@/lib/simulator";
import { useEngineStream } from "@/lib/stream";
import { useTrader } from "@/lib/trader";
import type { CandleInterval, OrderSide } from "@/lib/types";

function OfflineBanner({ engineUrl }: { engineUrl: string }) {
  return (
    <motion.div
      initial={{ opacity: 0, y: -6 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: -6 }}
      role="alert"
      className="shrink-0 border-b border-down/40 bg-down/[0.07] px-3 py-1.5"
    >
      <p className="text-[10.5px] leading-relaxed text-txt-dim">
        <span className="text-down">Cannot reach the engine</span> at{" "}
        <code className="text-txt">{engineUrl}</code>. Start it with{" "}
        <code className="text-txt">go run cmd/main.go</code> and allow this origin
        via <code className="text-txt">ALLOWED_ORIGINS</code>.
      </p>
    </motion.div>
  );
}

export default function TerminalPage() {
  const { traderId, ready, myOrderIds, regenerate, trackOrder } = useTrader();

  const symbolsQuery = useSymbols();
  const metricsQuery = useMetrics();

  const [symbol, setSymbol] = useState<string | null>(null);
  const [interval, setInterval] = useState<CandleInterval>("1m");
  const [simEnabled, setSimEnabled] = useState(false);
  const [prefill, setPrefill] = useState<TicketPrefill | null>(null);
  const nonce = useRef(0);

  // Adopt the engine's default instrument once the symbol list arrives.
  useEffect(() => {
    if (symbol !== null || !symbolsQuery.data) return;
    setSymbol(symbolsQuery.data.default ?? symbolsQuery.data.symbols[0] ?? null);
  }, [symbol, symbolsQuery.data]);

  // Book depth, prints and order updates are pushed over SSE.
  const stream = useEngineStream(symbol);

  // Aggregations the engine has no event for.
  const statsQuery = useStats(symbol);
  const candlesQuery = useCandles(symbol, interval);
  const accountQuery = useAccount(traderId);
  const ordersQuery = useTraderOrders(traderId);

  useFlowSimulator({
    enabled: simEnabled,
    symbol,
    mid: stream.depth?.mid ?? null,
    ordersPerSecond: 4,
  });

  const prints = useMemo(
    () => [...annotateTrades(stream.trades)].reverse(),
    [stream.trades],
  );

  const ownOrderIds = useMemo(() => new Set(myOrderIds), [myOrderIds]);

  const livePrice =
    stream.trades.length > 0 ? stream.trades[stream.trades.length - 1].price : null;

  const offline = stream.status === "offline" || Boolean(symbolsQuery.error);

  const refreshTraderState = useCallback(() => {
    void accountQuery.mutate();
    void ordersQuery.mutate();
    void metricsQuery.mutate();
  }, [accountQuery, ordersQuery, metricsQuery]);

  const handleSubmitted = useCallback(
    (orderId: string) => {
      trackOrder(orderId);
      // Depth and prints arrive on the stream; account state is polled, so nudge it.
      refreshTraderState();
    },
    [refreshTraderState, trackOrder],
  );

  const handleSelectPrice = useCallback((price: number, side: OrderSide) => {
    nonce.current += 1;
    setPrefill({ price, side, nonce: nonce.current });
  }, []);

  return (
    <div className="flex h-dvh flex-col overflow-hidden">
      <TopBar
        symbols={symbolsQuery.data?.symbols ?? []}
        activeSymbol={symbol}
        symbolStats={metricsQuery.data?.symbols ?? symbolsQuery.data?.stats ?? []}
        onSelectSymbol={setSymbol}
        stats={statsQuery.data}
        livePrice={livePrice}
        status={stream.status}
        simEnabled={simEnabled}
        onToggleSim={() => setSimEnabled((v) => !v)}
        traderId={traderId}
        onCycleTrader={regenerate}
      />

      <AnimatePresence>
        {offline ? <OfflineBanner engineUrl={ENGINE_URL} /> : null}
      </AnimatePresence>

      {/* Upper region: book | chart + depth | trades + ticket */}
      <div className="grid min-h-0 flex-1 gap-px bg-line lg:grid-cols-[minmax(0,1fr)_260px] xl:grid-cols-[260px_minmax(0,1fr)_262px]">
        <div className="hidden min-h-0 xl:block">
          <OrderBookPanel
            depth={stream.depth}
            lastPrice={livePrice}
            onSelectPrice={handleSelectPrice}
          />
        </div>

        <div className="grid min-h-0 grid-rows-[minmax(0,1fr)_150px] gap-px">
          <CandleChart
            candles={candlesQuery.data?.candles ?? []}
            interval={interval}
            onIntervalChange={setInterval}
            livePrice={livePrice}
            loading={candlesQuery.isLoading}
          />
          <MarketDepthPanel depth={stream.depth} />
        </div>

        <div className="grid min-h-0 grid-rows-[minmax(120px,1fr)_minmax(0,320px)] gap-px">
          <RecentTradesPanel
            prints={prints}
            total={stream.trades.length}
            ownOrderIds={ownOrderIds}
          />
          <OrderTicketPanel
            symbol={symbol}
            traderId={traderId}
            ready={ready}
            depth={stream.depth}
            account={accountQuery.data}
            prefill={prefill}
            onSubmitted={handleSubmitted}
          />
        </div>
      </div>

      {/* Lower region: working orders, positions, history */}
      <div className="h-[168px] shrink-0 border-t border-line">
        <BottomTabs
          orders={ordersQuery.data}
          account={accountQuery.data}
          onChanged={refreshTraderState}
        />
      </div>

      <StatusBar
        account={accountQuery.data}
        metrics={metricsQuery.data}
        openOrderCount={ordersQuery.data?.open.length ?? 0}
        traderId={traderId}
      />
    </div>
  );
}
