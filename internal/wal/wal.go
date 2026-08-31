// Package wal provides an append-only write-ahead log of accepted commands.
//
// The engine keeps everything in memory, so a restart normally starts from an
// empty book. Recording every accepted order and cancel makes recovery possible:
// matching is deterministic given the same command sequence, so replaying the
// log in order reconstructs the book — and the trades it produced — exactly.
//
// The format is newline-delimited JSON. It is not the most compact option, but
// it is append-only, trivially tailable, and readable without tooling, which
// matters more for a log you reach for when something has gone wrong.
//
// Known limitation: the log grows without bound. A production system would
// periodically snapshot state and truncate; that is not implemented here.
package wal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"matching-engine/internal/models"
)

// Record kinds.
const (
	KindOrder  = "order"
	KindCancel = "cancel"
)

// Record is a single logged command.
type Record struct {
	Kind    string        `json:"kind"`
	Time    int64         `json:"time"`
	Symbol  string        `json:"symbol,omitempty"`
	Order   *models.Order `json:"order,omitempty"`
	OrderID string        `json:"order_id,omitempty"`
}

// Log is an append-only command log.
//
// Writes are serialised by a mutex and flushed with an explicit Sync, so a
// record that has been acknowledged has actually reached the disk rather than
// sitting in a buffer.
type Log struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// Open creates or opens the log for appending, creating parent directories as
// needed.
func Open(path string) (*Log, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating wal directory: %w", err)
		}
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening wal: %w", err)
	}

	return &Log{file: file, path: path}, nil
}

// Path returns the log's location on disk.
func (l *Log) Path() string { return l.path }

func (l *Log) append(record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encoding wal record: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("writing wal record: %w", err)
	}
	// Durability is the entire point of a write-ahead log; an unsynced write
	// would be lost by exactly the crash it is meant to survive.
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("syncing wal: %w", err)
	}
	return nil
}

// AppendOrder records an accepted order.
func (l *Log) AppendOrder(order *models.Order) error {
	// Copy so a later fill mutating the live order cannot change what was logged.
	stored := *order
	return l.append(Record{
		Kind:   KindOrder,
		Time:   order.Timestamp,
		Symbol: order.Symbol,
		Order:  &stored,
	})
}

// AppendCancel records an accepted cancellation.
func (l *Log) AppendCancel(symbol, orderID string) error {
	return l.append(Record{
		Kind:    KindCancel,
		Symbol:  symbol,
		OrderID: orderID,
	})
}

// Close flushes and closes the log.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Replay reads the log in order and hands each record to fn.
//
// A missing file is not an error — it just means there is nothing to recover.
// A truncated final line is tolerated and skipped, because a crash mid-write is
// precisely the situation this log exists for.
func Replay(path string, fn func(Record) error) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("opening wal for replay: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	applied := 0

	for lineNo := 1; ; lineNo++ {
		line, err := reader.ReadBytes('\n')

		// A final line with no newline means the process died mid-append.
		partial := err == io.EOF && len(line) > 0

		if len(line) > 0 {
			var record Record
			if jsonErr := json.Unmarshal(trimNewline(line), &record); jsonErr != nil {
				if partial {
					break // torn tail; everything before it is still valid
				}
				return applied, fmt.Errorf("wal line %d is corrupt: %w", lineNo, jsonErr)
			}
			if applyErr := fn(record); applyErr != nil {
				return applied, fmt.Errorf("applying wal line %d: %w", lineNo, applyErr)
			}
			applied++
		}

		if err != nil {
			break
		}
	}

	return applied, nil
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
