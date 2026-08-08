package model

import "time"

// Event is a festival/production ("proyecto/evento" in the runbook) that
// groups ingests under one destination folder and one set of cameras.
// Exactly one Event is active at a time; newly detected cards attach to
// whichever one that is (see RUNBOOK_MOY_MEDIA_INGEST.md section 5).
type Event struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Cameras   []string  `json:"cameras"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

// EventSummary adds the aggregate stats the admin panel shows per event.
// Ingests are linked to an event by name, not by ID (see internal/db), so
// these numbers are computed on read rather than maintained incrementally.
type EventSummary struct {
	Event
	CardCount  int64 `json:"card_count"`
	BytesTotal int64 `json:"bytes_total"`
}
