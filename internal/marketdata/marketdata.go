// Package marketdata derives time-series views from the engine's trade tape.
//
// Everything here is a pure function over a slice of trades. The engine records
// prints but has no notion of time buckets, sessions or windows, so candles and
// rolling statistics are computed on demand rather than maintained incrementally.
//
// The important caveat: the tape is capped, so history reaches back only as far
// as the retained trades. Responses report the interval and window they actually
// cover so a client can tell the difference between "no activity" and "no data".
package marketdata

import (
	"sort"
	"time"

	"matching-engine/internal/models"
)

// Supported candle intervals, keyed by the label a client sends.
var intervals = map[string]time.Duration{
	"1m":  time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"1d":  24 * time.Hour,
}

// DefaultInterval is used when a client sends no interval.
const DefaultInterval = "1m"

// ParseInterval resolves an interval label. Reports false for anything unknown,
// so the API can reject it rather than silently substituting a different chart.
func ParseInterval(label string) (time.Duration, bool) {
	if label == "" {
		label = DefaultInterval
	}
	d, ok := intervals[label]
	return d, ok
}

// Intervals lists the supported labels, shortest first.
func Intervals() []string {
	out := make([]string, 0, len(intervals))
	for label := range intervals {
		out = append(out, label)
	}
	sort.Slice(out, func(i, j int) bool {
		return intervals[out[i]] < intervals[out[j]]
	})
	return out
}

// Candle is one OHLC bucket. OpenTime is the bucket's start in Unix nanoseconds.
type Candle struct {
	OpenTime  int64   `json:"open_time"`
	CloseTime int64   `json:"close_time"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    int     `json:"volume"`
	Trades    int     `json:"trades"`
}

// CandleSeries is the response shape for a candle request.
type CandleSeries struct {
	Symbol   string   `json:"symbol"`
	Interval string   `json:"interval"`
	Candles  []Candle `json:"candles"`
}

// Candles buckets trades into OHLC candles.
//
// Trades must be oldest-first, which is the order the engine returns them.
// Buckets are aligned to absolute time rather than to the first trade, so the
// same trade always lands in the same bucket regardless of the window requested.
// Empty buckets are omitted: the engine has no concept of a session, so a gap in
// activity is genuinely an absence of data rather than a flat candle.
//
// limit greater than zero keeps only the most recent buckets.
func Candles(trades []*models.Trade, interval time.Duration, limit int) []Candle {
	if len(trades) == 0 || interval <= 0 {
		return []Candle{}
	}

	step := interval.Nanoseconds()
	out := make([]Candle, 0, 64)

	for _, trade := range trades {
		bucket := trade.Timestamp - (trade.Timestamp % step)

		last := len(out) - 1
		if last >= 0 && out[last].OpenTime == bucket {
			candle := &out[last]
			if trade.Price > candle.High {
				candle.High = trade.Price
			}
			if trade.Price < candle.Low {
				candle.Low = trade.Price
			}
			candle.Close = trade.Price
			candle.Volume += trade.Quantity
			candle.Trades++
			continue
		}

		out = append(out, Candle{
			OpenTime:  bucket,
			CloseTime: bucket + step,
			Open:      trade.Price,
			High:      trade.Price,
			Low:       trade.Price,
			Close:     trade.Price,
			Volume:    trade.Quantity,
			Trades:    1,
		})
	}

	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}

	return out
}

// Stats is a rolling-window summary of trading activity.
type Stats struct {
	Symbol        string  `json:"symbol"`
	Last          float64 `json:"last"`
	Open          float64 `json:"open"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Change        float64 `json:"change"`
	ChangePercent float64 `json:"change_percent"`
	// Volume is quantity traded; QuoteVolume is quantity times price, i.e. the
	// notional turnover the design's "VOLUME 1.2B" figure refers to.
	Volume        int     `json:"volume"`
	QuoteVolume   float64 `json:"quote_volume"`
	TradeCount    int     `json:"trade_count"`
	WindowSeconds float64 `json:"window_seconds"`
	// Covered reports how much of the window the retained tape actually spans,
	// so a client can tell a quiet market from a short history.
	CoveredSeconds float64 `json:"covered_seconds"`
}

// Summarise computes statistics over the trades falling inside window, counting
// back from now (Unix nanoseconds).
//
// Trades must be oldest-first.
func Summarise(symbol string, trades []*models.Trade, window time.Duration, now int64) Stats {
	stats := Stats{
		Symbol:        symbol,
		WindowSeconds: window.Seconds(),
	}

	if len(trades) == 0 {
		return stats
	}

	cutoff := now - window.Nanoseconds()

	// The tape is sorted, so find the first in-window trade by binary search
	// rather than walking up to 10,000 prints on every request.
	start := sort.Search(len(trades), func(i int) bool {
		return trades[i].Timestamp >= cutoff
	})

	// A window with no trades in it still has a meaningful last price: the most
	// recent print, however old. Reporting zero would look like a broken feed.
	if start >= len(trades) {
		stats.Last = trades[len(trades)-1].Price
		stats.Open = stats.Last
		stats.High = stats.Last
		stats.Low = stats.Last
		return stats
	}

	inWindow := trades[start:]

	first := inWindow[0]
	stats.Open = first.Price
	stats.High = first.Price
	stats.Low = first.Price

	for _, trade := range inWindow {
		if trade.Price > stats.High {
			stats.High = trade.Price
		}
		if trade.Price < stats.Low {
			stats.Low = trade.Price
		}
		stats.Volume += trade.Quantity
		stats.QuoteVolume += trade.Price * float64(trade.Quantity)
	}

	last := inWindow[len(inWindow)-1]
	stats.Last = last.Price
	stats.TradeCount = len(inWindow)
	stats.Change = stats.Last - stats.Open
	if stats.Open != 0 {
		stats.ChangePercent = (stats.Change / stats.Open) * 100
	}
	stats.CoveredSeconds = float64(last.Timestamp-first.Timestamp) / float64(time.Second)

	return stats
}
