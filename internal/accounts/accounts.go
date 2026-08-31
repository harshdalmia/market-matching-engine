// Package accounts tracks per-trader positions, cash and order history.
//
// The matching engine itself has no notion of an account: an order carries a
// trader ID string and nothing more, and the engine forgets an order the moment
// it leaves the book. This package sits alongside it as an observer, deriving
// account state from the fills and order transitions the engine reports.
//
// Two deliberate limitations:
//
// The ledger is observational, not enforcing. Buying power is never checked, so
// a trader can go arbitrarily long or short and cash may go negative. Adding
// enforcement would mean rejecting orders on a balance check, which is a
// different feature with different failure modes.
//
// Positions use average-cost accounting. Realised profit is booked when a
// position is reduced or closed; the average entry is untouched by reductions
// and reset when a position flips.
package accounts

import (
	"sort"
	"sync"

	"matching-engine/internal/models"
)

// Defaults for a fresh registry.
const (
	DefaultStartingCash = 100_000.0
	DefaultMaxHistory   = 200
	DefaultMaxAccounts  = 10_000
)

// Position is a trader's net exposure in one instrument.
//
// Quantity is signed: positive is long, negative is short. UnrealizedPnL,
// MarkPrice and Value are derived at read time from a supplied mark, not stored.
type Position struct {
	Symbol        string  `json:"symbol"`
	Quantity      int     `json:"quantity"`
	AvgEntry      float64 `json:"avg_entry"`
	RealizedPnL   float64 `json:"realized_pnl"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	MarkPrice     float64 `json:"mark_price"`
	Value         float64 `json:"value"`
}

// Snapshot is a trader's whole account at a point in time.
type Snapshot struct {
	TraderID      string     `json:"trader_id"`
	Cash          float64    `json:"cash"`
	StartingCash  float64    `json:"starting_cash"`
	RealizedPnL   float64    `json:"realized_pnl"`
	UnrealizedPnL float64    `json:"unrealized_pnl"`
	PositionValue float64    `json:"position_value"`
	Equity        float64    `json:"equity"`
	TotalPnL      float64    `json:"total_pnl"`
	Positions     []Position `json:"positions"`
	OpenOrders    int        `json:"open_orders"`
}

// OrdersSnapshot separates a trader's working orders from their finished ones.
type OrdersSnapshot struct {
	TraderID string         `json:"trader_id"`
	Open     []models.Order `json:"open"`
	History  []models.Order `json:"history"`
}

type account struct {
	cash      float64
	positions map[string]*Position
	open      map[string]models.Order
	// Oldest-first, trimmed from the front. Reversed on read so callers get
	// newest-first without paying to prepend on every event.
	history []models.Order
}

// Registry holds account state for every trader the engine has seen.
type Registry struct {
	mu           sync.RWMutex
	startingCash float64
	maxHistory   int
	maxAccounts  int
	accounts     map[string]*account
}

// New builds a registry. Zero or negative values fall back to the defaults.
func New(startingCash float64, maxHistory, maxAccounts int) *Registry {
	if startingCash <= 0 {
		startingCash = DefaultStartingCash
	}
	if maxHistory <= 0 {
		maxHistory = DefaultMaxHistory
	}
	if maxAccounts <= 0 {
		maxAccounts = DefaultMaxAccounts
	}

	return &Registry{
		startingCash: startingCash,
		maxHistory:   maxHistory,
		maxAccounts:  maxAccounts,
		accounts:     make(map[string]*account),
	}
}

// StartingCash is the balance every account opens with.
func (r *Registry) StartingCash() float64 { return r.startingCash }

// TraderCount reports how many accounts are being tracked.
func (r *Registry) TraderCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.accounts)
}

// ensure returns the account for a trader, creating it if there is room.
//
// Caller must hold the write lock. Returns nil once maxAccounts is reached:
// the API is unauthenticated, so an unbounded map keyed by a client-supplied
// string is a memory-growth vector. Beyond the cap, new traders are simply not
// tracked rather than being allowed to consume the process.
func (r *Registry) ensure(traderID string) *account {
	if traderID == "" {
		return nil
	}
	if existing, ok := r.accounts[traderID]; ok {
		return existing
	}
	if len(r.accounts) >= r.maxAccounts {
		return nil
	}

	created := &account{
		cash:      r.startingCash,
		positions: make(map[string]*Position),
		open:      make(map[string]models.Order),
	}
	r.accounts[traderID] = created
	return created
}

// -----------------------------------------------------------------------
// engine.Observer
// -----------------------------------------------------------------------

func isTerminal(status string) bool {
	return status == models.StatusFilled ||
		status == models.StatusCancelled ||
		status == models.StatusRejected
}

// OnOrderState moves an order between the working set and the history.
func (r *Registry) OnOrderState(order models.Order) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct := r.ensure(order.TraderID)
	if acct == nil {
		return
	}

	if !isTerminal(order.Status) {
		acct.open[order.ID] = order
		return
	}

	delete(acct.open, order.ID)

	// A fully filled order can be reported once per fill, so replace an existing
	// history entry for the same ID rather than accumulating duplicates.
	for i := range acct.history {
		if acct.history[i].ID == order.ID {
			acct.history[i] = order
			return
		}
	}

	acct.history = append(acct.history, order)
	if excess := len(acct.history) - r.maxHistory; excess > 0 {
		acct.history = append(acct.history[:0], acct.history[excess:]...)
	}
}

// OnFill books an execution against both counterparties.
func (r *Registry) OnFill(trade models.Trade, buy, sell models.Order) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.applyFill(buy.TraderID, trade.Symbol, models.SideBuy, trade.Price, trade.Quantity)
	r.applyFill(sell.TraderID, trade.Symbol, models.SideSell, trade.Price, trade.Quantity)
}

// applyFill updates one trader's cash and position. Caller holds the write lock.
func (r *Registry) applyFill(traderID, symbol, side string, price float64, quantity int) {
	if quantity <= 0 {
		return
	}

	acct := r.ensure(traderID)
	if acct == nil {
		return
	}

	notional := price * float64(quantity)
	signed := quantity

	if side == models.SideSell {
		signed = -quantity
		acct.cash += notional
	} else {
		acct.cash -= notional
	}

	position, ok := acct.positions[symbol]
	if !ok {
		position = &Position{Symbol: symbol}
		acct.positions[symbol] = position
	}

	applyToPosition(position, signed, price)
}

// applyToPosition folds a signed fill into a position using average-cost
// accounting, booking realised profit whenever exposure is reduced.
func applyToPosition(position *Position, signedQty int, price float64) {
	if signedQty == 0 {
		return
	}

	// Opening or adding to the same side: blend the entry price by quantity.
	if position.Quantity == 0 || sameSign(position.Quantity, signedQty) {
		existing := abs(position.Quantity)
		incoming := abs(signedQty)

		total := float64(existing)*position.AvgEntry + float64(incoming)*price
		position.Quantity += signedQty

		if position.Quantity != 0 {
			position.AvgEntry = total / float64(abs(position.Quantity))
		}
		return
	}

	// Reducing, closing, or flipping. Only the overlapping quantity realises a
	// profit; anything beyond that opens a fresh position on the other side.
	closing := abs(position.Quantity)
	if abs(signedQty) < closing {
		closing = abs(signedQty)
	}

	if position.Quantity > 0 {
		// Long being sold: profit is the rise above the average entry.
		position.RealizedPnL += (price - position.AvgEntry) * float64(closing)
	} else {
		// Short being bought back: profit is the fall below the average entry.
		position.RealizedPnL += (position.AvgEntry - price) * float64(closing)
	}

	flipped := abs(signedQty) > abs(position.Quantity)
	position.Quantity += signedQty

	switch {
	case position.Quantity == 0:
		position.AvgEntry = 0
	case flipped:
		// The residual is a brand new position taken on at this fill's price.
		position.AvgEntry = price
	}
}

// -----------------------------------------------------------------------
// Reads
// -----------------------------------------------------------------------

// Snapshot returns a trader's account, valuing open positions with marks.
//
// A symbol with no usable mark is valued at its own average entry, which makes
// its unrealised profit exactly zero rather than inventing a number.
func (r *Registry) Snapshot(traderID string, marks map[string]float64) Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snapshot := Snapshot{
		TraderID:     traderID,
		Cash:         r.startingCash,
		StartingCash: r.startingCash,
		Positions:    []Position{},
	}

	acct, ok := r.accounts[traderID]
	if !ok {
		snapshot.Equity = r.startingCash
		return snapshot
	}

	snapshot.Cash = acct.cash
	snapshot.OpenOrders = len(acct.open)

	for _, position := range acct.positions {
		view := *position

		mark := marks[view.Symbol]
		if mark <= 0 {
			mark = view.AvgEntry
		}
		view.MarkPrice = mark
		view.Value = mark * float64(view.Quantity)
		view.UnrealizedPnL = (mark - view.AvgEntry) * float64(view.Quantity)

		snapshot.RealizedPnL += view.RealizedPnL

		// A flat position still carries realised profit worth reporting, but it
		// contributes no exposure or valuation.
		if view.Quantity != 0 {
			snapshot.UnrealizedPnL += view.UnrealizedPnL
			snapshot.PositionValue += view.Value
		}

		snapshot.Positions = append(snapshot.Positions, view)
	}

	sort.Slice(snapshot.Positions, func(i, j int) bool {
		return snapshot.Positions[i].Symbol < snapshot.Positions[j].Symbol
	})

	snapshot.Equity = snapshot.Cash + snapshot.PositionValue
	snapshot.TotalPnL = snapshot.Equity - snapshot.StartingCash

	return snapshot
}

// Orders returns a trader's working orders and finished orders, newest first.
// An empty symbol returns every instrument.
func (r *Registry) Orders(traderID, symbol string) OrdersSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := OrdersSnapshot{
		TraderID: traderID,
		Open:     []models.Order{},
		History:  []models.Order{},
	}

	acct, ok := r.accounts[traderID]
	if !ok {
		return result
	}

	for _, order := range acct.open {
		if symbol == "" || order.Symbol == symbol {
			result.Open = append(result.Open, order)
		}
	}
	sort.Slice(result.Open, func(i, j int) bool {
		return result.Open[i].Timestamp > result.Open[j].Timestamp
	})

	// Stored oldest-first, so walk backwards to hand back newest-first.
	for i := len(acct.history) - 1; i >= 0; i-- {
		if symbol == "" || acct.history[i].Symbol == symbol {
			result.History = append(result.History, acct.history[i])
		}
	}

	return result
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sameSign(a, b int) bool {
	return (a > 0 && b > 0) || (a < 0 && b < 0)
}
