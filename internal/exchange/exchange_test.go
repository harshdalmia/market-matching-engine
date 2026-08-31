package exchange

import (
	"reflect"
	"testing"

	"matching-engine/internal/models"
)

func TestParseSymbols(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"single", "AAPL", []string{"AAPL"}},
		{"multiple sorted", "MSFT,AAPL", []string{"AAPL", "MSFT"}},
		{"uppercased", "aapl,msft", []string{"AAPL", "MSFT"}},
		{"whitespace trimmed", " AAPL , MSFT ", []string{"AAPL", "MSFT"}},
		{"duplicates collapsed", "AAPL,aapl,AAPL", []string{"AAPL"}},
		{"empty falls back", "", []string{FallbackSymbol}},
		{"only separators falls back", ",,,", []string{FallbackSymbol}},
		{"hyphenated preserved", "BTC-USD", []string{"BTC-USD"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSymbols(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestNewCreatesOneVenuePerSymbol(t *testing.T) {
	x := New([]string{"MSFT", "AAPL"})

	if got := x.Symbols(); !reflect.DeepEqual(got, []string{"AAPL", "MSFT"}) {
		t.Errorf("symbols should be sorted, got %v", got)
	}
	// The default is the first symbol in sorted order, so it is deterministic
	// regardless of how the operator ordered the configuration.
	if x.DefaultSymbol() != "AAPL" {
		t.Errorf("default: want AAPL, got %s", x.DefaultSymbol())
	}

	for _, symbol := range []string{"AAPL", "MSFT"} {
		v, ok := x.Venue(symbol)
		if !ok {
			t.Fatalf("venue %s missing", symbol)
		}
		if v.Book == nil || v.Engine == nil {
			t.Errorf("venue %s is not fully constructed", symbol)
		}
		if v.Engine.Symbol() != symbol {
			t.Errorf("engine symbol: want %s, got %s", symbol, v.Engine.Symbol())
		}
	}
}

func TestNewWithNoSymbolsUsesFallback(t *testing.T) {
	x := New(nil)

	if got := x.Symbols(); !reflect.DeepEqual(got, []string{FallbackSymbol}) {
		t.Errorf("want the fallback symbol, got %v", got)
	}
}

func TestResolve(t *testing.T) {
	x := New([]string{"AAPL", "MSFT"})

	// An omitted symbol must route to the default so single-instrument clients
	// keep working against the multi-symbol API.
	v, ok := x.Resolve("")
	if !ok || v.Symbol != "AAPL" {
		t.Errorf("empty symbol should resolve to the default, got %v %v", v, ok)
	}

	v, ok = x.Resolve("msft")
	if !ok || v.Symbol != "MSFT" {
		t.Errorf("lookup should be case-insensitive, got %v %v", v, ok)
	}

	if _, ok := x.Resolve("NOPE"); ok {
		t.Errorf("unknown symbol must not resolve")
	}
}

// Books must be genuinely independent: an order on one instrument must be
// invisible to every other.
func TestVenuesAreIsolated(t *testing.T) {
	x := New([]string{"AAA", "BBB"})

	aaa, _ := x.Venue("AAA")
	bbb, _ := x.Venue("BBB")

	aaa.Book.AddOrder(&models.Order{
		ID: "o1", Symbol: "AAA", TraderID: "t", Side: models.SideBuy,
		Price: 100, Quantity: 5, Remaining: 5, Status: models.StatusNew,
	})

	if aaa.Book.Len() != 1 {
		t.Errorf("AAA should hold 1 order, got %d", aaa.Book.Len())
	}
	if bbb.Book.Len() != 0 {
		t.Errorf("BBB must be unaffected, got %d orders", bbb.Book.Len())
	}
}

func TestCancelOrderFindsTheRightVenue(t *testing.T) {
	x := New([]string{"AAA", "BBB"})
	bbb, _ := x.Venue("BBB")

	bbb.Book.AddOrder(&models.Order{
		ID: "target", Symbol: "BBB", TraderID: "t", Side: models.SideBuy,
		Price: 100, Quantity: 5, Remaining: 5, Status: models.StatusNew,
	})

	// The client does not supply a symbol; IDs are UUIDs so the search is safe.
	symbol, ok := x.CancelOrder("target")
	if !ok {
		t.Fatalf("cancel should find the order")
	}
	if symbol != "BBB" {
		t.Errorf("want symbol BBB, got %s", symbol)
	}

	if _, ok := x.CancelOrder("missing"); ok {
		t.Errorf("cancelling an unknown ID must fail")
	}
}

func TestFindOrder(t *testing.T) {
	x := New([]string{"AAA", "BBB"})
	aaa, _ := x.Venue("AAA")

	aaa.Book.AddOrder(&models.Order{
		ID: "find-me", Symbol: "AAA", TraderID: "t", Side: models.SideSell,
		Price: 100, Quantity: 5, Remaining: 5, Status: models.StatusNew,
	})

	order, symbol, ok := x.FindOrder("find-me")
	if !ok || order == nil {
		t.Fatalf("order should be found")
	}
	if symbol != "AAA" {
		t.Errorf("want AAA, got %s", symbol)
	}

	if _, _, ok := x.FindOrder("nope"); ok {
		t.Errorf("unknown ID must not be found")
	}
}

func TestTotalsAggregateAcrossVenues(t *testing.T) {
	x := New([]string{"AAA", "BBB"})
	x.Start()
	defer x.Stop()

	aaa, _ := x.Venue("AAA")
	bbb, _ := x.Venue("BBB")

	aaa.Book.AddOrder(&models.Order{
		ID: "a1", Symbol: "AAA", TraderID: "t", Side: models.SideBuy,
		Price: 100, Quantity: 5, Remaining: 5, Status: models.StatusNew,
	})
	bbb.Book.AddOrder(&models.Order{
		ID: "b1", Symbol: "BBB", TraderID: "t", Side: models.SideBuy,
		Price: 100, Quantity: 5, Remaining: 5, Status: models.StatusNew,
	})

	_, _, _, resting, _ := x.Totals()
	if resting != 2 {
		t.Errorf("resting orders should sum across venues: want 2, got %d", resting)
	}
}

func TestAllStatsCoversEverySymbol(t *testing.T) {
	x := New([]string{"AAA", "BBB"})

	stats := x.AllStats()
	if len(stats) != 2 {
		t.Fatalf("want stats for 2 symbols, got %d", len(stats))
	}
	if stats[0].Symbol != "AAA" || stats[1].Symbol != "BBB" {
		t.Errorf("stats should be in symbol order, got %s then %s",
			stats[0].Symbol, stats[1].Symbol)
	}
	// An empty venue has no quote to report.
	if stats[0].BestBid != nil || stats[0].LastPrice != nil {
		t.Errorf("an untouched venue should report nil quote fields")
	}
}

func TestSymbolsReturnsACopy(t *testing.T) {
	x := New([]string{"AAA", "BBB"})

	got := x.Symbols()
	got[0] = "MUTATED"

	if x.Symbols()[0] != "AAA" {
		t.Errorf("Symbols must not expose internal state to mutation")
	}
}
