package marketdata

import (
	"math"
	"testing"
	"time"

	"matching-engine/internal/models"
)

// base is aligned to a minute boundary so bucket edges are predictable.
var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixNano()

// at builds a trade offset from base.
func at(offset time.Duration, price float64, qty int) *models.Trade {
	return &models.Trade{
		Symbol:    "AAA",
		Price:     price,
		Quantity:  qty,
		Timestamp: base + offset.Nanoseconds(),
	}
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// -----------------------------------------------------------------------
// Intervals
// -----------------------------------------------------------------------

func TestParseInterval(t *testing.T) {
	tests := []struct {
		label string
		want  time.Duration
		ok    bool
	}{
		{"1m", time.Minute, true},
		{"5m", 5 * time.Minute, true},
		{"15m", 15 * time.Minute, true},
		{"1h", time.Hour, true},
		{"4h", 4 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"", time.Minute, true}, // empty falls back to the default
		{"7s", 0, false},
		{"nonsense", 0, false},
	}

	for _, tc := range tests {
		got, ok := ParseInterval(tc.label)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.label, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("%q: want %v, got %v", tc.label, tc.want, got)
		}
	}
}

func TestIntervalsAreSortedShortestFirst(t *testing.T) {
	labels := Intervals()
	if len(labels) == 0 {
		t.Fatal("no intervals reported")
	}

	previous := time.Duration(0)
	for _, label := range labels {
		d, ok := ParseInterval(label)
		if !ok {
			t.Fatalf("reported interval %q does not parse", label)
		}
		if d <= previous {
			t.Errorf("intervals out of order at %q", label)
		}
		previous = d
	}
}

// -----------------------------------------------------------------------
// Candles
// -----------------------------------------------------------------------

func TestCandlesBucketByInterval(t *testing.T) {
	trades := []*models.Trade{
		at(0, 100, 1),
		at(30*time.Second, 105, 2),
		// Next minute.
		at(70*time.Second, 110, 3),
	}

	candles := Candles(trades, time.Minute, 0)
	if len(candles) != 2 {
		t.Fatalf("want 2 buckets, got %d", len(candles))
	}
	if candles[0].Trades != 2 || candles[1].Trades != 1 {
		t.Errorf("trade counts: got %d and %d", candles[0].Trades, candles[1].Trades)
	}
}

func TestCandleOHLC(t *testing.T) {
	trades := []*models.Trade{
		at(0, 100, 1),
		at(10*time.Second, 120, 2),
		at(20*time.Second, 90, 3),
		at(30*time.Second, 110, 4),
	}

	candles := Candles(trades, time.Minute, 0)
	if len(candles) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(candles))
	}

	c := candles[0]
	if !closeTo(c.Open, 100) {
		t.Errorf("open: want 100, got %.2f", c.Open)
	}
	if !closeTo(c.High, 120) {
		t.Errorf("high: want 120, got %.2f", c.High)
	}
	if !closeTo(c.Low, 90) {
		t.Errorf("low: want 90, got %.2f", c.Low)
	}
	if !closeTo(c.Close, 110) {
		t.Errorf("close: want 110, got %.2f", c.Close)
	}
	if c.Volume != 10 {
		t.Errorf("volume: want 10, got %d", c.Volume)
	}
}

// Buckets align to absolute time, not to the first trade, so the same print
// always lands in the same bucket whatever window was requested.
func TestCandleBucketsAlignToAbsoluteTime(t *testing.T) {
	step := time.Minute.Nanoseconds()

	candles := Candles([]*models.Trade{at(0, 100, 1)}, time.Minute, 0)
	if len(candles) != 1 {
		t.Fatalf("want 1 candle, got %d", len(candles))
	}
	if candles[0].OpenTime%step != 0 {
		t.Errorf("bucket start should be a multiple of the interval, got %d", candles[0].OpenTime)
	}
	if candles[0].CloseTime-candles[0].OpenTime != step {
		t.Errorf("bucket should span exactly one interval")
	}
}

func TestCandleLimitKeepsMostRecent(t *testing.T) {
	trades := make([]*models.Trade, 0, 10)
	for i := 0; i < 10; i++ {
		trades = append(trades, at(time.Duration(i)*time.Minute, float64(100+i), 1))
	}

	candles := Candles(trades, time.Minute, 3)
	if len(candles) != 3 {
		t.Fatalf("want 3 candles, got %d", len(candles))
	}
	// The last three prints were 107, 108, 109.
	if !closeTo(candles[len(candles)-1].Close, 109) {
		t.Errorf("last candle should close at 109, got %.2f", candles[len(candles)-1].Close)
	}
}

// A gap in activity is genuinely an absence of data, not a flat candle: the
// engine has no session concept to interpolate across.
func TestCandlesOmitEmptyBuckets(t *testing.T) {
	trades := []*models.Trade{
		at(0, 100, 1),
		at(10*time.Minute, 110, 1), // nine minutes of nothing in between
	}

	candles := Candles(trades, time.Minute, 0)
	if len(candles) != 2 {
		t.Errorf("want 2 candles with the gap omitted, got %d", len(candles))
	}
}

func TestCandlesEmptyInputs(t *testing.T) {
	if got := Candles(nil, time.Minute, 0); len(got) != 0 {
		t.Errorf("no trades should produce no candles, got %d", len(got))
	}
	if got := Candles([]*models.Trade{at(0, 100, 1)}, 0, 0); len(got) != 0 {
		t.Errorf("a zero interval should produce no candles, got %d", len(got))
	}
}

// -----------------------------------------------------------------------
// Statistics
// -----------------------------------------------------------------------

func TestSummariseWithinWindow(t *testing.T) {
	trades := []*models.Trade{
		at(0, 100, 10),
		at(time.Minute, 120, 5),
		at(2*time.Minute, 90, 5),
		at(3*time.Minute, 110, 10),
	}
	now := base + (4 * time.Minute).Nanoseconds()

	stats := Summarise("AAA", trades, time.Hour, now)

	if !closeTo(stats.Open, 100) {
		t.Errorf("open: want 100, got %.2f", stats.Open)
	}
	if !closeTo(stats.High, 120) {
		t.Errorf("high: want 120, got %.2f", stats.High)
	}
	if !closeTo(stats.Low, 90) {
		t.Errorf("low: want 90, got %.2f", stats.Low)
	}
	if !closeTo(stats.Last, 110) {
		t.Errorf("last: want 110, got %.2f", stats.Last)
	}
	if !closeTo(stats.Change, 10) {
		t.Errorf("change: want 10, got %.2f", stats.Change)
	}
	if !closeTo(stats.ChangePercent, 10) {
		t.Errorf("change pct: want 10, got %.4f", stats.ChangePercent)
	}
	if stats.Volume != 30 {
		t.Errorf("volume: want 30, got %d", stats.Volume)
	}
	// 100*10 + 120*5 + 90*5 + 110*10 = 1000 + 600 + 450 + 1100
	if !closeTo(stats.QuoteVolume, 3150) {
		t.Errorf("quote volume: want 3150, got %.2f", stats.QuoteVolume)
	}
	if stats.TradeCount != 4 {
		t.Errorf("trade count: want 4, got %d", stats.TradeCount)
	}
}

// Trades older than the window must not contribute to the open price, the
// extremes, or the volume.
func TestSummariseExcludesTradesOutsideWindow(t *testing.T) {
	trades := []*models.Trade{
		at(0, 500, 100),                           // ancient, far outside
		at(59*time.Minute, 100, 1),                // inside
		at(59*time.Minute+30*time.Second, 110, 1), // inside
	}
	now := base + time.Hour.Nanoseconds()

	stats := Summarise("AAA", trades, 5*time.Minute, now)

	if !closeTo(stats.Open, 100) {
		t.Errorf("open should come from inside the window, got %.2f", stats.Open)
	}
	if !closeTo(stats.High, 110) {
		t.Errorf("the out-of-window 500 print must not set the high, got %.2f", stats.High)
	}
	if stats.Volume != 2 {
		t.Errorf("volume should exclude the old print, got %d", stats.Volume)
	}
	if stats.TradeCount != 2 {
		t.Errorf("trade count: want 2, got %d", stats.TradeCount)
	}
}

// A quiet window still has a meaningful last price — the most recent print,
// however old. Reporting zero would look like a broken feed.
func TestSummariseFallsBackToLastKnownPrice(t *testing.T) {
	trades := []*models.Trade{at(0, 250, 5)}
	now := base + (48 * time.Hour).Nanoseconds()

	stats := Summarise("AAA", trades, time.Hour, now)

	if !closeTo(stats.Last, 250) {
		t.Errorf("last: want the most recent print 250, got %.2f", stats.Last)
	}
	if stats.TradeCount != 0 {
		t.Errorf("no trades fall inside the window, got count %d", stats.TradeCount)
	}
	if stats.Volume != 0 {
		t.Errorf("volume should be zero for an empty window, got %d", stats.Volume)
	}
	if !closeTo(stats.Change, 0) {
		t.Errorf("change should be zero with nothing to compare, got %.2f", stats.Change)
	}
}

func TestSummariseEmptyTape(t *testing.T) {
	stats := Summarise("AAA", nil, time.Hour, base)

	if stats.Last != 0 || stats.TradeCount != 0 || stats.Volume != 0 {
		t.Errorf("an empty tape should produce zeroed stats, got %+v", stats)
	}
	if stats.Symbol != "AAA" {
		t.Errorf("symbol should still be reported, got %q", stats.Symbol)
	}
	if !closeTo(stats.WindowSeconds, time.Hour.Seconds()) {
		t.Errorf("window should be reported, got %.0f", stats.WindowSeconds)
	}
}

func TestSummariseReportsCoveredSpan(t *testing.T) {
	trades := []*models.Trade{
		at(0, 100, 1),
		at(2*time.Minute, 110, 1),
	}
	now := base + (3 * time.Minute).Nanoseconds()

	stats := Summarise("AAA", trades, time.Hour, now)

	// The tape spans two minutes even though the window is an hour, which is how
	// a client tells a short history from a quiet market.
	if !closeTo(stats.CoveredSeconds, 120) {
		t.Errorf("covered span: want 120s, got %.2f", stats.CoveredSeconds)
	}
}

func TestSummariseSingleTrade(t *testing.T) {
	stats := Summarise("AAA", []*models.Trade{at(0, 100, 7)}, time.Hour, base+1)

	if !closeTo(stats.Open, 100) || !closeTo(stats.Last, 100) {
		t.Errorf("a single print is both open and last, got open %.2f last %.2f", stats.Open, stats.Last)
	}
	if !closeTo(stats.Change, 0) || !closeTo(stats.ChangePercent, 0) {
		t.Errorf("a single print has no change, got %.4f", stats.Change)
	}
	if stats.Volume != 7 {
		t.Errorf("volume: want 7, got %d", stats.Volume)
	}
}
