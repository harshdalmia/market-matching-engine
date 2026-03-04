package main

import (
	"context"
	"flag"
	"matching-engine/internal/api"
	"matching-engine/internal/engine"
	"matching-engine/internal/orderbook"
	"matching-engine/internal/utils"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Flags
	port := flag.String("port", "8080", "HTTP server port")
	stress := flag.Bool("stress", false, "Run stress test with 10,000 orders then start server")
	stressCount := flag.Int("n", 10000, "Number of orders for stress test")
	flag.Parse()

	// -----------------------------------------------------------------------
	// Wire everything together
	// -----------------------------------------------------------------------
	ob := orderbook.New()
	eng := engine.New(ob)
	eng.Start()

	// Optional stress test before starting the server
	if *stress {
		utils.LogInfo("Running stress test...")
		eng.GenerateOrders(*stressCount)

		total, avgLatencyMs := eng.Metrics()
		utils.LogInfo("Stress test complete")
		_ = total
		_ = avgLatencyMs
	}

	// -----------------------------------------------------------------------
	// HTTP Server
	// -----------------------------------------------------------------------
	handler := api.NewHandler(eng, ob)
	router := handler.NewRouter()

	srv := &http.Server{
		Addr:         ":" + *port,
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in background
	go func() {
		utils.LogInfo("Server listening on :" + *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			utils.LogError("Server error", err)
			os.Exit(1)
		}
	}()

	// -----------------------------------------------------------------------
	// Graceful Shutdown
	// -----------------------------------------------------------------------
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	utils.LogInfo("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		utils.LogError("Forced shutdown", err)
	}

	eng.Stop()
	utils.LogInfo("Goodbye.")
}
