package engine

import (
	"fmt"
	"matching-engine/internal/models"
	"math/rand"
	"time"
)

// GenerateOrders creates n random orders and submits them to the engine.
// Used for benchmarking and stress testing.
func (e *Engine) GenerateOrders(n int) {
	start := time.Now()
	fmt.Printf("\n[GENERATOR] Starting stress test: %d orders...\n", n)

	sides := []string{models.SideBuy, models.SideSell}

	for i := 0; i < n; i++ {
		side := sides[rand.Intn(2)]

		// Price range: 95–105 so orders actually match each other
		price := 95.0 + rand.Float64()*10.0
		price = float64(int(price*100)) / 100 // round to 2 decimal places

		quantity := rand.Intn(100) + 1 // 1–100

		// Mostly resting limit orders, with a minority of immediate order types
		// so a stress run exercises every matching path rather than just GTC.
		orderType := models.TypeLimit
		tif := models.TIFGTC

		switch roll := rand.Float64(); {
		case roll < 0.06:
			orderType = models.TypeMarket
			tif = models.TIFIOC
			price = 0
		case roll < 0.12:
			tif = models.TIFIOC
		case roll < 0.15:
			tif = models.TIFFOK
		}

		order := &models.Order{
			ID:          fmt.Sprintf("GEN-%06d", i+1),
			TraderID:    fmt.Sprintf("TRADER-%03d", rand.Intn(50)+1),
			Side:        side,
			Type:        orderType,
			TimeInForce: tif,
			Price:       price,
			Quantity:    quantity,
			Remaining:   quantity,
			Timestamp:   time.Now().UnixNano(),
			Status:      models.StatusNew,
		}

		e.SubmitBlocking(order)
	}

	// Wait for engine to drain the channel
	for len(e.orderChan) > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	// A little extra buffer for the last orders being processed
	time.Sleep(100 * time.Millisecond)

	elapsed := time.Since(start).Milliseconds()

	fmt.Printf("[GENERATOR] Done.\n")
	fmt.Printf("[GENERATOR] Orders submitted : %d\n", n)
	fmt.Printf("[GENERATOR] Trades executed  : %d\n", e.TradeCount())
	fmt.Printf("[GENERATOR] Time elapsed     : %d ms\n", elapsed)
	if elapsed > 0 {
		fmt.Printf("[GENERATOR] Throughput       : %d orders/sec\n\n", int64(n)*1000/elapsed)
	}
}
