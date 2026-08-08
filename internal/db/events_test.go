package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "state", "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestBootstrapEvent_OnlySeedsOnce(t *testing.T) {
	d := openTestDB(t)

	if err := d.BootstrapEvent("FIRST", []string{"CAM01"}); err != nil {
		t.Fatal(err)
	}
	if err := d.BootstrapEvent("SECOND", []string{"CAM02"}); err != nil {
		t.Fatal(err)
	}

	ev, err := d.GetActiveEvent()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != "FIRST" {
		t.Errorf("expected bootstrap to run only once, got active event %q", ev.Name)
	}
}

func TestCreateEvent_RejectsDuplicateName(t *testing.T) {
	d := openTestDB(t)

	if _, err := d.CreateEvent("FESTIVAL_X", []string{"CAM01"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateEvent("FESTIVAL_X", []string{"CAM02"}); !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("expected ErrDuplicateEvent, got %v", err)
	}
}

func TestActivateEvent_OnlyOneActiveAtOnce(t *testing.T) {
	d := openTestDB(t)

	a, err := d.CreateEvent("EVENT_A", []string{"CAM01"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateEvent("EVENT_B", []string{"CAM02"})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ActivateEvent(a.ID); err != nil {
		t.Fatal(err)
	}
	active, err := d.GetActiveEvent()
	if err != nil {
		t.Fatal(err)
	}
	if active.Name != "EVENT_A" {
		t.Fatalf("expected EVENT_A active, got %s", active.Name)
	}

	if err := d.ActivateEvent(b.ID); err != nil {
		t.Fatal(err)
	}
	active, err = d.GetActiveEvent()
	if err != nil {
		t.Fatal(err)
	}
	if active.Name != "EVENT_B" {
		t.Fatalf("expected EVENT_B active after switch, got %s", active.Name)
	}

	all, err := d.ListEventsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	activeCount := 0
	for _, ev := range all {
		if ev.IsActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly 1 active event, got %d", activeCount)
	}
}

func TestActivateEvent_UnknownIDFails(t *testing.T) {
	d := openTestDB(t)
	if err := d.ActivateEvent(999); err == nil {
		t.Fatal("expected error activating a nonexistent event")
	}
}

func TestGetActiveEvent_NoneConfigured(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.GetActiveEvent(); !errors.Is(err, ErrNoActiveEvent) {
		t.Fatalf("expected ErrNoActiveEvent, got %v", err)
	}
}

func TestListEventsWithStats_AggregatesIngests(t *testing.T) {
	d := openTestDB(t)

	if _, err := d.CreateEvent("STATS_EVENT", []string{"CAM01"}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateDetected("STATS_EVENT", "card-1", "/mock/card-1", 3, 1000, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateDetected("STATS_EVENT", "card-2", "/mock/card-2", 5, 2000, time.Now()); err != nil {
		t.Fatal(err)
	}

	summaries, err := d.ListEventsWithStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 event, got %d", len(summaries))
	}
	if summaries[0].CardCount != 2 {
		t.Errorf("expected card_count 2, got %d", summaries[0].CardCount)
	}
	if summaries[0].BytesTotal != 3000 {
		t.Errorf("expected bytes_total 3000, got %d", summaries[0].BytesTotal)
	}
}
