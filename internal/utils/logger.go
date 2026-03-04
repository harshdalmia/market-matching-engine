package utils

import (
	"fmt"
	"log"
	"os"
	"time"
)

var (
	infoLogger  = log.New(os.Stdout, "[INFO]  ", 0)
	tradeLogger = log.New(os.Stdout, "[TRADE] ", 0)
	errLogger   = log.New(os.Stderr, "[ERROR] ", 0)
)

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000")
}

func LogOrderReceived(orderID, side string, price float64, qty int) {
	infoLogger.Printf("%s | Order received — ID: %s | Side: %s | Price: %.2f | Qty: %d",
		timestamp(), orderID, side, price, qty)
}

func LogOrderStateChange(orderID, from, to string) {
	infoLogger.Printf("%s | Order state change — ID: %s | %s → %s",
		timestamp(), orderID, from, to)
}

func LogTradeExecuted(tradeID, buyID, sellID string, price float64, qty int) {
	tradeLogger.Printf("%s | Trade executed — ID: %s | Buy: %s | Sell: %s | Price: %.2f | Qty: %d",
		timestamp(), tradeID, buyID, sellID, price, qty)
}

func LogOrderCancelled(orderID string) {
	infoLogger.Printf("%s | Order cancelled — ID: %s", timestamp(), orderID)
}

func LogLatency(orderID string, latencyNs int64) {
	infoLogger.Printf("%s | Latency — ID: %s | %.3f ms",
		timestamp(), orderID, float64(latencyNs)/1e6)
}

func LogError(context string, err error) {
	errLogger.Printf("%s | %s — %v", timestamp(), context, err)
}

func LogInfo(msg string) {
	infoLogger.Printf("%s | %s", timestamp(), msg)
}

func LogBenchmark(ordersProcessed int, durationMs float64) {
	throughput := float64(ordersProcessed) / (durationMs / 1000.0)
	fmt.Printf("\n====== BENCHMARK RESULTS ======\n")
	fmt.Printf("Orders Processed : %d\n", ordersProcessed)
	fmt.Printf("Total Time       : %.2f ms\n", durationMs)
	fmt.Printf("Throughput       : %.0f orders/sec\n", throughput)
	fmt.Printf("================================\n\n")
}
