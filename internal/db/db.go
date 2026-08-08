// Package db is the SQLite persistence layer. It uses the pure-Go
// modernc.org/sqlite driver so the whole application builds with
// CGO_ENABLED=0 and stays trivial to cross-compile / containerize.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/model"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	sql *sql.DB
}

// Open creates the database file's parent directory if needed, opens the
// SQLite database, and applies the (idempotent) schema.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // SQLite + WAL: keep writes serialized, simplicity over throughput.

	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &DB{sql: sqlDB}, nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

const timeLayout = time.RFC3339Nano

func timeToStr(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func ptrToNullStr(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: timeToStr(*t), Valid: true}
}

func nullStrToPtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := time.Parse(timeLayout, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateDetected inserts a brand-new job for a just-scanned source. Status
// starts at DETECTED; callers (the ingest manager) are expected to move it
// to WAITING_CAMERA immediately after emitting the DETECTED event.
func (d *DB) CreateDetected(projectName, sourceIdentifier, sourcePath string, fileCount, bytesTotal int64, detectedAt time.Time) (*model.Ingest, error) {
	res, err := d.sql.Exec(
		`INSERT INTO ingests (project_name, source_identifier, source_path, file_count, bytes_total, status, verify_status, detected_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectName, sourceIdentifier, sourcePath, fileCount, bytesTotal, model.StatusDetected, model.VerifyPending, timeToStr(detectedAt), timeToStr(detectedAt),
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetByID(id)
}

// SourceKnown reports whether a job already exists for this source
// identifier, so the detector never creates a duplicate for the same card.
func (d *DB) SourceKnown(sourceIdentifier string) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(1) FROM ingests WHERE source_identifier = ?`, sourceIdentifier).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (d *DB) GetByID(id int64) (*model.Ingest, error) {
	row := d.sql.QueryRow(selectColumns+` WHERE id = ?`, id)
	return scanIngest(row)
}

func (d *DB) ListAll() ([]*model.Ingest, error) {
	rows, err := d.sql.Query(selectColumns + ` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Ingest
	for rows.Next() {
		ing, err := scanIngest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ing)
	}
	return out, rows.Err()
}

func (d *DB) UpdateStatus(id int64, status model.Status) error {
	_, err := d.sql.Exec(`UPDATE ingests SET status = ? WHERE id = ?`, status, id)
	return err
}

// AssignCamera transitions WAITING_CAMERA -> COPYING once an operator has
// picked a camera and a card number/destination were computed.
func (d *DB) AssignCamera(id int64, camera, cardNumber, destinationPath string, startedAt time.Time) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET camera_id = ?, card_number = ?, destination_path = ?, status = ?, started_at = ? WHERE id = ?`,
		camera, cardNumber, destinationPath, model.StatusCopying, timeToStr(startedAt), id,
	)
	return err
}

// UpdateProgress persists the running byte counter during a copy. Callers
// should throttle calls (not once per file) to avoid hammering SQLite.
func (d *DB) UpdateProgress(id int64, bytesCopied int64) error {
	_, err := d.sql.Exec(`UPDATE ingests SET bytes_copied = ? WHERE id = ?`, bytesCopied, id)
	return err
}

func (d *DB) MarkCopyCompleted(id int64, completedAt time.Time, bytesCopied int64) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET status = ?, copy_completed_at = ?, bytes_copied = ? WHERE id = ?`,
		model.StatusVerifying, timeToStr(completedAt), bytesCopied, id,
	)
	return err
}

func (d *DB) MarkVerified(id int64, completedAt time.Time) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET status = ?, verify_status = ?, verify_completed_at = ? WHERE id = ?`,
		model.StatusVerified, model.VerifyOK, timeToStr(completedAt), id,
	)
	return err
}

// MarkSafeToEject is only ever called right after MarkVerified succeeds.
func (d *DB) MarkSafeToEject(id int64, finishedAt time.Time) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET status = ?, finished_at = ? WHERE id = ?`,
		model.StatusSafeToEject, timeToStr(finishedAt), id,
	)
	return err
}

// MarkError puts the job in ERROR. It never touches SAFE_TO_EJECT.
func (d *DB) MarkError(id int64, errMsg string, verifyStatus model.VerifyResult, finishedAt time.Time) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET status = ?, error_message = ?, verify_status = ?, finished_at = ? WHERE id = ?`,
		model.StatusError, errMsg, verifyStatus, timeToStr(finishedAt), id,
	)
	return err
}

func (d *DB) MarkEjected(id int64, ejectedAt time.Time) error {
	_, err := d.sql.Exec(
		`UPDATE ingests SET status = ?, ejected_at = ? WHERE id = ?`,
		model.StatusEjected, timeToStr(ejectedAt), id,
	)
	return err
}

// NextCardNumber returns the next sequential card number (e.g. "CARD003")
// for a given project+camera pair, based on how many cards that pair has
// already been assigned.
func (d *DB) NextCardNumber(projectName, cameraID string) (string, error) {
	var count int
	err := d.sql.QueryRow(
		`SELECT COUNT(1) FROM ingests WHERE project_name = ? AND camera_id = ?`,
		projectName, cameraID,
	).Scan(&count)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CARD%03d", count+1), nil
}

const selectColumns = `SELECT
	id, project_name, camera_id, card_number, source_identifier, source_path, destination_path,
	detected_at, started_at, copy_completed_at, verify_completed_at, finished_at, ejected_at, formatted_at,
	file_count, bytes_total, bytes_copied, status, verify_status, error_message, created_at
	FROM ingests`

type row interface {
	Scan(dest ...any) error
}

func scanIngest(r row) (*model.Ingest, error) {
	var ing model.Ingest
	var detectedAt, createdAt string
	var startedAt, copyCompletedAt, verifyCompletedAt, finishedAt, ejectedAt, formattedAt sql.NullString

	err := r.Scan(
		&ing.ID, &ing.ProjectName, &ing.CameraID, &ing.CardNumber, &ing.SourceIdentifier, &ing.SourcePath, &ing.DestinationPath,
		&detectedAt, &startedAt, &copyCompletedAt, &verifyCompletedAt, &finishedAt, &ejectedAt, &formattedAt,
		&ing.FileCount, &ing.BytesTotal, &ing.BytesCopied, &ing.Status, &ing.VerifyStatus, &ing.ErrorMessage, &createdAt,
	)
	if err != nil {
		return nil, err
	}

	if ing.DetectedAt, err = time.Parse(timeLayout, detectedAt); err != nil {
		return nil, fmt.Errorf("parse detected_at: %w", err)
	}
	if ing.CreatedAt, err = time.Parse(timeLayout, createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if ing.StartedAt, err = nullStrToPtr(startedAt); err != nil {
		return nil, fmt.Errorf("parse started_at: %w", err)
	}
	if ing.CopyCompletedAt, err = nullStrToPtr(copyCompletedAt); err != nil {
		return nil, fmt.Errorf("parse copy_completed_at: %w", err)
	}
	if ing.VerifyCompletedAt, err = nullStrToPtr(verifyCompletedAt); err != nil {
		return nil, fmt.Errorf("parse verify_completed_at: %w", err)
	}
	if ing.FinishedAt, err = nullStrToPtr(finishedAt); err != nil {
		return nil, fmt.Errorf("parse finished_at: %w", err)
	}
	if ing.EjectedAt, err = nullStrToPtr(ejectedAt); err != nil {
		return nil, fmt.Errorf("parse ejected_at: %w", err)
	}
	if ing.FormattedAt, err = nullStrToPtr(formattedAt); err != nil {
		return nil, fmt.Errorf("parse formatted_at: %w", err)
	}

	return &ing, nil
}
