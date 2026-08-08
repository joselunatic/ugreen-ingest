// Package ingest is the state machine at the heart of the application: it
// turns detected sources into ingest jobs and drives them through
// DETECTED -> WAITING_CAMERA -> COPYING -> VERIFYING -> VERIFIED ->
// SAFE_TO_EJECT -> EJECTED (or ERROR), per
// RUNBOOK_MOY_MEDIA_INGEST.md section 4.2.
package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/copyengine"
	"github.com/ugreen-ingest/ugreen-ingest/internal/db"
	"github.com/ugreen-ingest/ugreen-ingest/internal/device"
	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
	"github.com/ugreen-ingest/ugreen-ingest/internal/verify"
)

var (
	ErrNotFound       = errors.New("ingest not found")
	ErrInvalidState   = errors.New("ingest is not in the expected state")
	ErrInvalidCamera  = errors.New("invalid camera")
	ErrCollision      = errors.New("destination already exists")
	ErrFormatDisabled = errors.New("formatting is not available in this phase")
)

const scanInterval = 2 * time.Second

type Manager struct {
	cfg      *config.Config
	db       *db.DB
	provider device.Provider
	hasher   verify.Hasher
	broker   *broker
}

func NewManager(cfg *config.Config, database *db.DB, provider device.Provider, hasher verify.Hasher) *Manager {
	return &Manager{
		cfg:      cfg,
		db:       database,
		provider: provider,
		hasher:   hasher,
		broker:   newBroker(),
	}
}

func (m *Manager) Subscribe() (int, <-chan Event) { return m.broker.subscribe() }
func (m *Manager) Unsubscribe(id int)             { m.broker.unsubscribe(id) }
func (m *Manager) Config() *config.Config         { return m.cfg }

// Run polls the configured device provider until ctx is cancelled, turning
// every newly seen source into a WAITING_CAMERA ingest job.
func (m *Manager) Run(ctx context.Context) {
	m.scanOnce(ctx)

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scanOnce(ctx)
		}
	}
}

func (m *Manager) scanOnce(ctx context.Context) {
	sources, err := m.provider.Scan(ctx)
	if err != nil {
		log.Printf("ingest: device scan error: %v", err)
		return
	}

	for _, src := range sources {
		known, err := m.db.SourceKnown(src.Identifier)
		if err != nil {
			log.Printf("ingest: check source known: %v", err)
			continue
		}
		if known {
			continue
		}

		ing, err := m.db.CreateDetected(m.cfg.Project.DefaultName, src.Identifier, src.Path, src.FileCount, src.SizeBytes, time.Now())
		if err != nil {
			log.Printf("ingest: create detected job for %s: %v", src.Identifier, err)
			continue
		}
		m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})

		if err := m.db.UpdateStatus(ing.ID, model.StatusWaitingCamera); err != nil {
			log.Printf("ingest: move %d to WAITING_CAMERA: %v", ing.ID, err)
			continue
		}
		ing.Status = model.StatusWaitingCamera
		m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})
	}
}

func (m *Manager) List() ([]*model.Ingest, error) {
	return m.db.ListAll()
}

func (m *Manager) Get(id int64) (*model.Ingest, error) {
	ing, err := m.db.GetByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return ing, nil
}

// AssignCamera records which camera a card came from, allocates the next
// card number for that camera, and kicks off copy+verify in the background.
func (m *Manager) AssignCamera(id int64, camera string) (*model.Ingest, error) {
	camera = strings.ToUpper(strings.TrimSpace(camera))
	if !m.allowedCamera(camera) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCamera, camera)
	}

	ing, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if ing.Status != model.StatusWaitingCamera {
		return nil, fmt.Errorf("%w: current status is %s", ErrInvalidState, ing.Status)
	}

	cardNumber, err := m.db.NextCardNumber(ing.ProjectName, camera)
	if err != nil {
		return nil, err
	}
	destPath := filepath.Join(m.cfg.DestinationRoot, ing.ProjectName, camera, cardNumber)
	if _, statErr := os.Stat(destPath); statErr == nil {
		return nil, fmt.Errorf("%w: %s", ErrCollision, destPath)
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if err := m.db.AssignCamera(id, camera, cardNumber, destPath, time.Now()); err != nil {
		return nil, err
	}
	ing, err = m.Get(id)
	if err != nil {
		return nil, err
	}
	m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})

	go m.runCopyAndVerify(ing)

	return ing, nil
}

func (m *Manager) allowedCamera(camera string) bool {
	if camera == config.ExtraCamera {
		return true
	}
	for _, c := range m.cfg.Cameras {
		if strings.EqualFold(c, camera) {
			return true
		}
	}
	return false
}

// runCopyAndVerify drives one job through COPYING -> VERIFYING -> VERIFIED
// -> SAFE_TO_EJECT, or into ERROR. It must never mark SAFE_TO_EJECT without
// a preceding successful verify.
func (m *Manager) runCopyAndVerify(ing *model.Ingest) {
	ctx := context.Background()
	start := time.Now()
	var lastPublish time.Time

	onProgress := func(p copyengine.Progress) {
		now := time.Now()
		final := p.FilesCopied >= p.FilesTotal
		if !final && now.Sub(lastPublish) < 200*time.Millisecond {
			return
		}
		lastPublish = now

		if err := m.db.UpdateProgress(ing.ID, p.BytesCopied); err != nil {
			log.Printf("ingest: update progress for %d: %v", ing.ID, err)
		}

		var speed float64
		if elapsed := now.Sub(start).Seconds(); elapsed > 0 {
			speed = float64(p.BytesCopied) / elapsed
		}
		var percent float64
		switch {
		case p.BytesTotal > 0:
			percent = float64(p.BytesCopied) / float64(p.BytesTotal) * 100
		case p.FilesTotal > 0:
			percent = float64(p.FilesCopied) / float64(p.FilesTotal) * 100
		default:
			percent = 100
		}

		m.broker.publish(Event{Type: EventProgress, Progress: &model.Progress{
			IngestID:       ing.ID,
			FilesCopied:    p.FilesCopied,
			FilesTotal:     p.FilesTotal,
			BytesCopied:    p.BytesCopied,
			BytesTotal:     p.BytesTotal,
			Percent:        percent,
			SpeedBytesPerS: speed,
		}})
	}

	result, err := copyengine.Copy(ctx, ing.SourcePath, ing.DestinationPath, onProgress)
	if err != nil {
		m.fail(ing.ID, fmt.Sprintf("copy failed: %v", err), model.VerifyPending)
		return
	}

	if err := m.db.MarkCopyCompleted(ing.ID, time.Now(), result.BytesTotal); err != nil {
		log.Printf("ingest: mark copy completed for %d: %v", ing.ID, err)
		return
	}
	m.publishReload(ing.ID)

	vres, err := verify.Verify(ctx, ing.SourcePath, ing.DestinationPath, m.hasher)
	if err != nil {
		m.fail(ing.ID, fmt.Sprintf("verification error: %v", err), model.VerifyFailed)
		return
	}
	if !vres.OK() {
		m.fail(ing.ID, summarizeMismatches(vres.Mismatches), model.VerifyFailed)
		return
	}

	if err := m.db.MarkVerified(ing.ID, time.Now()); err != nil {
		log.Printf("ingest: mark verified for %d: %v", ing.ID, err)
		return
	}
	m.publishReload(ing.ID)

	if err := m.db.MarkSafeToEject(ing.ID, time.Now()); err != nil {
		log.Printf("ingest: mark safe to eject for %d: %v", ing.ID, err)
		return
	}
	m.publishReload(ing.ID)
}

func (m *Manager) fail(id int64, msg string, vs model.VerifyResult) {
	if err := m.db.MarkError(id, msg, vs, time.Now()); err != nil {
		log.Printf("ingest: mark error for %d: %v", id, err)
		return
	}
	m.publishReload(id)
}

func (m *Manager) publishReload(id int64) {
	ing, err := m.db.GetByID(id)
	if err != nil {
		log.Printf("ingest: reload %d: %v", id, err)
		return
	}
	m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})
}

func summarizeMismatches(mismatches []verify.Mismatch) string {
	const max = 5
	parts := make([]string, 0, max)
	for i, mm := range mismatches {
		if i >= max {
			break
		}
		parts = append(parts, mm.String())
	}
	msg := fmt.Sprintf("verification failed: %d file(s) mismatched: %s", len(mismatches), strings.Join(parts, "; "))
	if len(mismatches) > max {
		msg += fmt.Sprintf(" ... and %d more", len(mismatches)-max)
	}
	return msg
}

// Eject marks a card as physically removable. It only succeeds from
// SAFE_TO_EJECT, so a card can never be "ejected" mid-copy or mid-verify.
func (m *Manager) Eject(id int64) (*model.Ingest, error) {
	ing, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if ing.Status != model.StatusSafeToEject {
		return nil, fmt.Errorf("%w: current status is %s", ErrInvalidState, ing.Status)
	}
	if err := m.db.MarkEjected(id, time.Now()); err != nil {
		return nil, err
	}
	ing, err = m.Get(id)
	if err != nil {
		return nil, err
	}
	m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})
	return ing, nil
}

// Format is intentionally not implemented in this phase; see
// RUNBOOK_MOY_MEDIA_INGEST.md section 4.2 and section 22.
func (m *Manager) Format(id int64) error {
	return ErrFormatDisabled
}
