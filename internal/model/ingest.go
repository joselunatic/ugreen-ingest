// Package model holds the data types shared by the persistence, ingest
// state machine, and API layers, so none of them need to import each other
// just to see the shape of an ingest job.
package model

import "time"

// Status is the ingest job lifecycle, per RUNBOOK_MOY_MEDIA_INGEST.md
// section 4.2. SAFE_TO_EJECT must never be reached without a prior
// successful VERIFIED transition.
type Status string

const (
	StatusDetected      Status = "DETECTED"
	StatusWaitingCamera Status = "WAITING_CAMERA"
	StatusCopying       Status = "COPYING"
	StatusVerifying     Status = "VERIFYING"
	StatusVerified      Status = "VERIFIED"
	StatusSafeToEject   Status = "SAFE_TO_EJECT"
	StatusEjected       Status = "EJECTED"
	StatusError         Status = "ERROR"
)

// VerifyResult is the outcome of the verification engine, stored
// independently from Status so history keeps a clear audit trail.
type VerifyResult string

const (
	VerifyPending VerifyResult = "PENDING"
	VerifyOK      VerifyResult = "OK"
	VerifyFailed  VerifyResult = "FAILED"
)

// Ingest represents one card's journey from detection to ejection.
// Field names/semantics follow RUNBOOK_MOY_MEDIA_INGEST.md section 6.
type Ingest struct {
	ID                int64        `json:"id"`
	ProjectName       string       `json:"project_name"`
	CameraID          string       `json:"camera_id"`
	CardNumber        string       `json:"card_number"`
	SourceIdentifier  string       `json:"source_identifier"`
	SourcePath        string       `json:"source_path"`
	DestinationPath   string       `json:"destination_path"`
	DetectedAt        time.Time    `json:"detected_at"`
	StartedAt         *time.Time   `json:"started_at,omitempty"`
	CopyCompletedAt   *time.Time   `json:"copy_completed_at,omitempty"`
	VerifyCompletedAt *time.Time   `json:"verify_completed_at,omitempty"`
	FinishedAt        *time.Time   `json:"finished_at,omitempty"`
	EjectedAt         *time.Time   `json:"ejected_at,omitempty"`
	FormattedAt       *time.Time   `json:"formatted_at,omitempty"`
	FileCount         int64        `json:"file_count"`
	BytesTotal        int64        `json:"bytes_total"`
	BytesCopied       int64        `json:"bytes_copied"`
	Status            Status       `json:"status"`
	VerifyStatus      VerifyResult `json:"verify_status"`
	ErrorMessage      string       `json:"error_message"`
	CreatedAt         time.Time    `json:"created_at"`
}

// Progress is a lightweight snapshot broadcast over SSE while copying.
type Progress struct {
	IngestID       int64   `json:"ingest_id"`
	FilesCopied    int64   `json:"files_copied"`
	FilesTotal     int64   `json:"files_total"`
	BytesCopied    int64   `json:"bytes_copied"`
	BytesTotal     int64   `json:"bytes_total"`
	Percent        float64 `json:"percent"`
	SpeedBytesPerS float64 `json:"speed_bytes_per_sec"`
}
