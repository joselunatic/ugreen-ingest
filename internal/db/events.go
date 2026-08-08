package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
)

var (
	ErrNoActiveEvent  = errors.New("no active event configured")
	ErrDuplicateEvent = errors.New("an event with this name already exists")
)

// BootstrapEvent creates and activates a first event from the given
// defaults, but only if the events table is still empty. Safe to call on
// every startup: after the first run it's a no-op, so it never clobbers
// events created since via the admin panel.
func (d *DB) BootstrapEvent(name string, cameras []string) error {
	var count int
	if err := d.sql.QueryRow(`SELECT COUNT(1) FROM events`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := d.createEvent(name, cameras, true)
	return err
}

func (d *DB) CreateEvent(name string, cameras []string) (*model.Event, error) {
	return d.createEvent(name, cameras, false)
}

func (d *DB) createEvent(name string, cameras []string, active bool) (*model.Event, error) {
	camerasJSON, err := json.Marshal(cameras)
	if err != nil {
		return nil, err
	}

	activeInt := 0
	if active {
		activeInt = 1
	}

	res, err := d.sql.Exec(
		`INSERT INTO events (name, cameras, is_active, created_at) VALUES (?, ?, ?, ?)`,
		name, string(camerasJSON), activeInt, timeToStr(time.Now()),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return nil, ErrDuplicateEvent
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetEventByID(id)
}

func (d *DB) GetEventByID(id int64) (*model.Event, error) {
	row := d.sql.QueryRow(eventColumns+` FROM events WHERE id = ?`, id)
	return scanEvent(row)
}

// GetEventByName looks up an event by its exact name - used to validate a
// camera against the event an already-detected card belongs to, which may
// no longer be the currently active one.
func (d *DB) GetEventByName(name string) (*model.Event, error) {
	row := d.sql.QueryRow(eventColumns+` FROM events WHERE name = ?`, name)
	return scanEvent(row)
}

// GetActiveEvent returns the single active event. Callers can assume this
// always succeeds once BootstrapEvent has run at startup.
func (d *DB) GetActiveEvent() (*model.Event, error) {
	row := d.sql.QueryRow(eventColumns + ` FROM events WHERE is_active = 1`)
	ev, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoActiveEvent
	}
	return ev, err
}

// ActivateEvent makes id the single active event, deactivating whichever
// one was active before. It does not touch any ingest already recorded
// under the previous event.
func (d *DB) ActivateEvent(id int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE events SET is_active = 0 WHERE is_active = 1`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE events SET is_active = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ListEventsWithStats returns every event, newest first, with the number
// of cards and total bytes ingested under it so far.
func (d *DB) ListEventsWithStats() ([]*model.EventSummary, error) {
	rows, err := d.sql.Query(`
		SELECT e.id, e.name, e.cameras, e.is_active, e.created_at,
		       COUNT(i.id), COALESCE(SUM(i.bytes_total), 0)
		FROM events e
		LEFT JOIN ingests i ON i.project_name = e.name
		GROUP BY e.id
		ORDER BY e.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.EventSummary
	for rows.Next() {
		var s model.EventSummary
		var camerasJSON, createdAt string
		var isActive int
		if err := rows.Scan(&s.ID, &s.Name, &camerasJSON, &isActive, &createdAt, &s.CardCount, &s.BytesTotal); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(camerasJSON), &s.Cameras); err != nil {
			return nil, fmt.Errorf("parse cameras for event %d: %w", s.ID, err)
		}
		s.IsActive = isActive != 0
		t, err := time.Parse(timeLayout, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at for event %d: %w", s.ID, err)
		}
		s.CreatedAt = t
		out = append(out, &s)
	}
	return out, rows.Err()
}

const eventColumns = `SELECT id, name, cameras, is_active, created_at`

func scanEvent(r row) (*model.Event, error) {
	var ev model.Event
	var camerasJSON, createdAt string
	var isActive int

	if err := r.Scan(&ev.ID, &ev.Name, &camerasJSON, &isActive, &createdAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(camerasJSON), &ev.Cameras); err != nil {
		return nil, fmt.Errorf("parse cameras for event %d: %w", ev.ID, err)
	}
	ev.IsActive = isActive != 0
	t, err := time.Parse(timeLayout, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at for event %d: %w", ev.ID, err)
	}
	ev.CreatedAt = t
	return &ev, nil
}
