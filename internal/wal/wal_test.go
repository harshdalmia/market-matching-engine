package wal

import (
	"os"
	"path/filepath"
	"testing"

	"matching-engine/internal/models"
)

func tempPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "engine.wal")
}

func sampleOrder(id string, qty int) *models.Order {
	return &models.Order{
		ID:          id,
		Symbol:      "AAA",
		TraderID:    "alice",
		Side:        models.SideBuy,
		Type:        models.TypeLimit,
		TimeInForce: models.TIFGTC,
		Price:       100.5,
		Quantity:    qty,
		Remaining:   qty,
		Timestamp:   1234,
		Status:      models.StatusNew,
	}
}

func TestAppendAndReplayRoundTrip(t *testing.T) {
	path := tempPath(t)

	log, err := Open(path)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}

	if err := log.AppendOrder(sampleOrder("o1", 10)); err != nil {
		t.Fatalf("appending order: %v", err)
	}
	if err := log.AppendCancel("AAA", "o1"); err != nil {
		t.Fatalf("appending cancel: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("closing log: %v", err)
	}

	var records []Record
	applied, err := Replay(path, func(r Record) error {
		records = append(records, r)
		return nil
	})
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}

	if applied != 2 {
		t.Fatalf("want 2 records applied, got %d", applied)
	}

	if records[0].Kind != KindOrder {
		t.Errorf("first record: want %s, got %s", KindOrder, records[0].Kind)
	}
	if records[0].Order == nil || records[0].Order.ID != "o1" {
		t.Errorf("order record did not survive the round trip: %+v", records[0].Order)
	}
	// Every field matters for replay fidelity: a changed timestamp would change
	// time priority, and a changed type would change matching behaviour.
	if got := records[0].Order; got != nil {
		if got.Timestamp != 1234 || got.Price != 100.5 || got.Quantity != 10 {
			t.Errorf("order fields altered: %+v", got)
		}
		if got.Type != models.TypeLimit || got.TimeInForce != models.TIFGTC {
			t.Errorf("type/TIF not preserved: %s/%s", got.Type, got.TimeInForce)
		}
	}

	if records[1].Kind != KindCancel {
		t.Errorf("second record: want %s, got %s", KindCancel, records[1].Kind)
	}
	if records[1].OrderID != "o1" {
		t.Errorf("cancel record lost its order ID: %q", records[1].OrderID)
	}
}

// The logged copy must not change when the live order is filled afterwards,
// otherwise replay would reconstruct a partially filled order as if it had
// arrived that way.
func TestAppendSnapshotsTheOrder(t *testing.T) {
	path := tempPath(t)

	log, err := Open(path)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	defer log.Close()

	order := sampleOrder("o1", 10)
	if err := log.AppendOrder(order); err != nil {
		t.Fatalf("appending: %v", err)
	}

	// Simulate the matching loop filling it after the append.
	order.Remaining = 3
	order.Status = models.StatusPartial

	var logged *models.Order
	if _, err := Replay(path, func(r Record) error {
		logged = r.Order
		return nil
	}); err != nil {
		t.Fatalf("replaying: %v", err)
	}

	if logged == nil {
		t.Fatal("no order recovered")
	}
	if logged.Remaining != 10 || logged.Status != models.StatusNew {
		t.Errorf("logged order followed later mutations: remaining %d status %s",
			logged.Remaining, logged.Status)
	}
}

func TestReplayMissingFileIsNotAnError(t *testing.T) {
	applied, err := Replay(filepath.Join(t.TempDir(), "absent.wal"), func(Record) error {
		t.Fatalf("callback should not run")
		return nil
	})

	if err != nil {
		t.Errorf("a missing log means nothing to recover, got error: %v", err)
	}
	if applied != 0 {
		t.Errorf("want 0 records, got %d", applied)
	}
}

func TestReplayEmptyFile(t *testing.T) {
	path := tempPath(t)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("writing empty file: %v", err)
	}

	applied, err := Replay(path, func(Record) error { return nil })
	if err != nil {
		t.Errorf("empty log should replay cleanly, got: %v", err)
	}
	if applied != 0 {
		t.Errorf("want 0 records, got %d", applied)
	}
}

// A crash mid-append leaves a torn final line. Everything before it is still
// valid and must be recovered rather than discarded.
func TestReplayToleratesTornFinalLine(t *testing.T) {
	path := tempPath(t)

	log, err := Open(path)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	if err := log.AppendOrder(sampleOrder("good-1", 5)); err != nil {
		t.Fatalf("appending: %v", err)
	}
	if err := log.AppendOrder(sampleOrder("good-2", 5)); err != nil {
		t.Fatalf("appending: %v", err)
	}
	log.Close()

	// Append a truncated record with no trailing newline.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if _, err := file.WriteString(`{"kind":"order","order":{"id":"tor`); err != nil {
		t.Fatalf("writing torn line: %v", err)
	}
	file.Close()

	var ids []string
	applied, err := Replay(path, func(r Record) error {
		if r.Order != nil {
			ids = append(ids, r.Order.ID)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("a torn tail should not fail replay: %v", err)
	}
	if applied != 2 {
		t.Errorf("want the 2 intact records, got %d", applied)
	}
	if len(ids) != 2 || ids[0] != "good-1" || ids[1] != "good-2" {
		t.Errorf("recovered the wrong records: %v", ids)
	}
}

// Corruption in the middle of the log is different from a torn tail: it means
// something is genuinely wrong, and silently skipping it would recover a
// different market than the one that existed.
func TestReplayFailsOnCorruptionMidFile(t *testing.T) {
	path := tempPath(t)

	contents := `{"kind":"order","order":{"id":"o1"}}` + "\n" +
		`this is not json` + "\n" +
		`{"kind":"order","order":{"id":"o2"}}` + "\n"

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	if _, err := Replay(path, func(Record) error { return nil }); err == nil {
		t.Errorf("corruption mid-log should fail loudly")
	}
}

func TestReplayPreservesOrdering(t *testing.T) {
	path := tempPath(t)

	log, err := Open(path)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}

	want := []string{"a", "b", "c", "d"}
	for _, id := range want {
		if err := log.AppendOrder(sampleOrder(id, 1)); err != nil {
			t.Fatalf("appending %s: %v", id, err)
		}
	}
	log.Close()

	var got []string
	if _, err := Replay(path, func(r Record) error {
		got = append(got, r.Order.ID)
		return nil
	}); err != nil {
		t.Fatalf("replaying: %v", err)
	}

	// Order is the whole basis of deterministic recovery.
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("replay order wrong: want %v, got %v", want, got)
		}
	}
}

func TestOpenCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "engine.wal")

	log, err := Open(path)
	if err != nil {
		t.Fatalf("Open should create missing directories: %v", err)
	}
	defer log.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("log file was not created: %v", err)
	}
}

func TestAppendsSurviveReopen(t *testing.T) {
	path := tempPath(t)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	first.AppendOrder(sampleOrder("before", 1))
	first.Close()

	// Reopening must append, not truncate.
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	second.AppendOrder(sampleOrder("after", 1))
	second.Close()

	applied, err := Replay(path, func(Record) error { return nil })
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if applied != 2 {
		t.Errorf("reopening should append: want 2 records, got %d", applied)
	}
}

func TestReplayPropagatesCallbackError(t *testing.T) {
	path := tempPath(t)

	log, _ := Open(path)
	log.AppendOrder(sampleOrder("o1", 1))
	log.Close()

	if _, err := Replay(path, func(Record) error {
		return os.ErrInvalid
	}); err == nil {
		t.Errorf("a failing callback should abort replay")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	log, err := Open(tempPath(t))
	if err != nil {
		t.Fatalf("opening: %v", err)
	}

	if err := log.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Errorf("second close should be a no-op, got: %v", err)
	}
}
