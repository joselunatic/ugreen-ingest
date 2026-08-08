// Package device abstracts "what media is currently present" behind a
// single interface, so the mock directory-based provider used today can be
// swapped for a real Linux block-device provider later without touching the
// ingest state machine. See RUNBOOK_MOY_MEDIA_INGEST.md sections 9 and 13.
package device

import "context"

// Source is one piece of media a Provider currently sees, before any
// ingest job exists for it.
type Source struct {
	// Identifier is stable across scans for the same physical card, and is
	// what the ingest manager uses to avoid creating duplicate jobs.
	Identifier string
	Label      string
	// Path is a read-only filesystem location the copy engine can walk.
	Path      string
	SizeBytes int64
	FileCount int64
}

// Provider reports the media currently present. Implementations must be
// read-only: nothing in this package may write to, mount, or format a
// source.
type Provider interface {
	Name() string
	Scan(ctx context.Context) ([]Source, error)
}
