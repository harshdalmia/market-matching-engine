package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"matching-engine/internal/accounts"
	"matching-engine/internal/api"
	"matching-engine/internal/exchange"
	"matching-engine/internal/models"
	"matching-engine/internal/stream"
	"matching-engine/internal/utils"
	"matching-engine/internal/wal"
)

// envOr returns the environment variable value, or def when unset/empty.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envFloat reads a float environment variable, falling back to def when unset or
// unparseable.
func envFloat(key string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return parsed
}

// parseOrigins splits a comma-separated CORS allow-list. An empty value means
// "allow any origin", which is the sensible default for an unauthenticated
// public demo API but should be locked down once deployed.
func parseOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"*"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

func main() {
	// Flags. Defaults come from the environment so the same binary works
	// locally and on hosts like Fly.io or Railway that inject PORT.
	port := flag.String("port", envOr("PORT", "8080"), "HTTP server port")
	origins := flag.String("origins", envOr("ALLOWED_ORIGINS", ""), "Comma-separated CORS allow-list (empty = any origin)")
	symbolList := flag.String("symbols", envOr("SYMBOLS", "BTC/USD,ETH/USD,AAPL"), "Comma-separated instruments to host")
	walPath := flag.String("wal", envOr("WAL_PATH", ""), "Write-ahead log path for crash recovery (empty = disabled)")
	startingCash := flag.Float64("cash", envFloat("STARTING_CASH", accounts.DefaultStartingCash), "Opening cash balance for every trader account")
	stress := flag.Bool("stress", false, "Run stress test with 10,000 orders then start server")
	stressCount := flag.Int("n", 10000, "Number of orders for stress test")
	flag.Parse()

	allowedOrigins := parseOrigins(*origins)
	symbols := exchange.ParseSymbols(*symbolList)

	// -----------------------------------------------------------------------
	// Wire everything together
	// -----------------------------------------------------------------------

	// One book and one matching goroutine per instrument.
	venue := exchange.New(symbols)

	// The broker fans engine events out to SSE subscribers, and the registry
	// derives positions, cash and order history from the same activity. Both are
	// attached before Start so nothing is missed once matching begins.
	broker := stream.NewBroker()
	venue.SetPublisher(broker)

	registry := accounts.New(*startingCash, 0, 0)
	venue.SetObserver(registry)

	venue.Start()

	// -----------------------------------------------------------------------
	// Recovery
	//
	// Replay happens after the engines are running but before the server accepts
	// traffic, so recovered state is complete before the first client sees it.
	// Records are submitted straight to the engines rather than through the HTTP
	// path, which would append them to the log a second time.
	// -----------------------------------------------------------------------
	var recorder *wal.Log

	if *walPath != "" {
		applied, err := wal.Replay(*walPath, func(record wal.Record) error {
			v, ok := venue.Resolve(record.Symbol)
			if !ok {
				// A symbol that is no longer hosted cannot be rebuilt. Skip it
				// rather than refusing to start.
				utils.LogInfo("Skipping replay for unknown symbol: " + record.Symbol)
				return nil
			}

			switch record.Kind {
			case wal.KindOrder:
				if record.Order == nil {
					return nil
				}
				// Reset the runtime fields so the order is matched from scratch
				// rather than from whatever state it had reached when logged.
				replayed := *record.Order
				replayed.Remaining = replayed.Quantity
				replayed.Status = models.StatusNew
				v.Engine.SubmitBlocking(&replayed)

			case wal.KindCancel:
				v.Book.CancelOrder(record.OrderID)
			}
			return nil
		})
		if err != nil {
			utils.LogError("Replaying write-ahead log failed", err)
			os.Exit(1)
		}

		if applied > 0 {
			// Let the matching loops finish before reporting recovered state.
			venue.Drain(10 * time.Second)
			utils.LogInfo(fmt.Sprintf("Recovered %d records from %s", applied, *walPath))
		}

		recorder, err = wal.Open(*walPath)
		if err != nil {
			utils.LogError("Opening write-ahead log failed", err)
			os.Exit(1)
		}
		defer recorder.Close()
	}

	// Optional stress test before starting the server.
	if *stress {
		utils.LogInfo("Running stress test...")
		perSymbol := *stressCount / len(symbols)
		if perSymbol < 1 {
			perSymbol = 1
		}
		for _, symbol := range venue.Symbols() {
			v, _ := venue.Venue(symbol)
			utils.LogInfo("Stress test: " + symbol)
			v.Engine.GenerateOrders(perSymbol)
		}
		utils.LogInfo("Stress test complete")
	}

	// -----------------------------------------------------------------------
	// HTTP Server
	// -----------------------------------------------------------------------
	handler := api.NewHandler(venue, broker, registry, allowedOrigins)
	if recorder != nil {
		handler.SetRecorder(recorder)
	}
	router := handler.NewRouter()

	srv := &http.Server{
		Addr:        ":" + *port,
		Handler:     router,
		ReadTimeout: 5 * time.Second,
		// No WriteTimeout: /stream holds a response open indefinitely, and a
		// write deadline would sever every SSE connection on a fixed schedule.
		// Read and idle timeouts still bound abusive clients.
		IdleTimeout: 60 * time.Second,
	}

	// Start server in background
	go func() {
		utils.LogInfo("Symbols: " + strings.Join(venue.Symbols(), ", "))
		utils.LogInfo("CORS allowed origins: " + strings.Join(allowedOrigins, ", "))
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

	venue.Stop()
	utils.LogInfo("Goodbye.")
}
