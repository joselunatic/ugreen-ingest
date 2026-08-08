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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/copyengine"
	"github.com/ugreen-ingest/ugreen-ingest/internal/db"
	"github.com/ugreen-ingest/ugreen-ingest/internal/device"
	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
	"github.com/ugreen-ingest/ugreen-ingest/internal/verify"
)

var (
	ErrNotFound          = errors.New("ingest not found")
	ErrInvalidState      = errors.New("ingest is not in the expected state")
	ErrInvalidCamera     = errors.New("invalid camera")
	ErrCollision         = errors.New("destination already exists")
	ErrFormatDisabled    = errors.New("formatting is not available in this phase")
	ErrEventNotFound     = errors.New("event not found")
	ErrInvalidEventName  = errors.New("invalid event name")
	ErrInvalidCameraList = errors.New("invalid camera list")
)

// eventNamePattern / cameraCodePattern restrict the characters allowed in
// event names and camera codes, since both become literal path components
// under destination_root.
var (
	eventNamePattern  = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,100}$`)
	cameraCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

const scanInterval = 2 * time.Second

type Manager struct {
	cfg      *config.Config
	db       *db.DB
	provider device.Provider
	hasher   verify.Hasher
	broker   *broker

	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func NewManager(cfg *config.Config, database *db.DB, provider device.Provider, hasher verify.Hasher) *Manager {
	return &Manager{
		cfg:      cfg,
		db:       database,
		provider: provider,
		hasher:   hasher,
		broker:   newBroker(),
		cancels:  make(map[int64]context.CancelFunc),
	}
}

func (m *Manager) Subscribe() (int, <-chan Event) { return m.broker.subscribe() }
func (m *Manager) Unsubscribe(id int)             { m.broker.unsubscribe(id) }
func (m *Manager) Config() *config.Config         { return m.cfg }

// ActiveEvent returns the event new detections currently attach to.
func (m *Manager) ActiveEvent() (*model.Event, error) {
	return m.db.GetActiveEvent()
}

// ListEvents returns every event, newest first, with per-event stats.
func (m *Manager) ListEvents() ([]*model.EventSummary, error) {
	return m.db.ListEventsWithStats()
}

// CreateEvent validates and stores a new event. It does not activate it -
// call ActivateEvent separately once you're ready to switch to it.
func (m *Manager) CreateEvent(name string, cameras []string) (*model.Event, error) {
	name = strings.TrimSpace(name)
	if !eventNamePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: must be 1-100 characters, letters/numbers/spaces/-/_ only", ErrInvalidEventName)
	}

	cleaned, err := cleanCameraList(cameras)
	if err != nil {
		return nil, err
	}

	ev, err := m.db.CreateEvent(name, cleaned)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateEvent) {
			return nil, fmt.Errorf("%w: %q", db.ErrDuplicateEvent, name)
		}
		return nil, err
	}
	return ev, nil
}

// ActivateEvent switches which event newly detected cards attach to. It
// never touches ingests already recorded under any event.
func (m *Manager) ActivateEvent(id int64) (*model.Event, error) {
	if err := m.db.ActivateEvent(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEventNotFound
		}
		return nil, err
	}
	return m.db.GetEventByID(id)
}

func cleanCameraList(cameras []string) ([]string, error) {
	seen := make(map[string]bool)
	var cleaned []string
	for _, c := range cameras {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || c == config.ExtraCamera || seen[c] {
			continue
		}
		if !cameraCodePattern.MatchString(c) {
			return nil, fmt.Errorf("%w: %q must be 1-40 characters, letters/numbers/-/_ only", ErrInvalidCameraList, c)
		}
		seen[c] = true
		cleaned = append(cleaned, c)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: at least one camera is required", ErrInvalidCameraList)
	}
	return cleaned, nil
}

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
	if len(sources) == 0 {
		return
	}

	activeEvent, err := m.db.GetActiveEvent()
	if err != nil {
		log.Printf("ingest: no active event, cannot register detected cards: %v", err)
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

		ing, err := m.db.CreateDetected(activeEvent.Name, src.Identifier, src.Path, src.FileCount, src.SizeBytes, time.Now())
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

	ing, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if ing.Status != model.StatusWaitingCamera {
		return nil, fmt.Errorf("%w: current status is %s", ErrInvalidState, ing.Status)
	}

	// Validate against the event the card was detected under, not
	// necessarily whichever event is active right now.
	ev, err := m.db.GetEventByName(ing.ProjectName)
	if err != nil {
		return nil, fmt.Errorf("look up event %q: %w", ing.ProjectName, err)
	}
	if !allowedCamera(camera, ev.Cameras) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCamera, camera)
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

	m.launchCopyAndVerify(ing)

	return ing, nil
}

// launchCopyAndVerify starts (or restarts, for Retry/ResumeInterrupted) the
// copy+verify goroutine for a job, wiring up a cancellable context that
// Cancel can trigger. The cancel func is registered synchronously, before
// the goroutine is spawned, so a Cancel call made right after this returns
// is guaranteed to find it.
func (m *Manager) launchCopyAndVerify(ing *model.Ingest) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[ing.ID] = cancel
	m.mu.Unlock()

	go m.runCopyAndVerify(ctx, ing)
}

func (m *Manager) unregisterCancel(id int64) {
	m.mu.Lock()
	delete(m.cancels, id)
	m.mu.Unlock()
}

func allowedCamera(camera string, eventCameras []string) bool {
	if camera == config.ExtraCamera {
		return true
	}
	for _, c := range eventCameras {
		if strings.EqualFold(c, camera) {
			return true
		}
	}
	return false
}

// runCopyAndVerify drives one job through COPYING -> VERIFYING -> VERIFIED
// -> SAFE_TO_EJECT, or into ERROR/CANCELLED. It must never mark
// SAFE_TO_EJECT without a preceding successful verify.
func (m *Manager) runCopyAndVerify(ctx context.Context, ing *model.Ingest) {
	defer m.unregisterCancel(ing.ID)
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
		if errors.Is(err, context.Canceled) {
			m.cancelDuringCopy(ing)
			return
		}
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
		if errors.Is(err, context.Canceled) {
			m.cancelDuringVerify(ing)
			return
		}
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

// cancelDuringCopy handles a cancellation observed while COPYING: the
// destination is by definition incomplete, so it's removed rather than
// left around as something that looks like a card folder but isn't one.
func (m *Manager) cancelDuringCopy(ing *model.Ingest) {
	if err := os.RemoveAll(ing.DestinationPath); err != nil {
		log.Printf("ingest: remove partial destination for cancelled job %d: %v", ing.ID, err)
	}
	if err := m.db.MarkCancelled(ing.ID, "Copia cancelada por el operador.", time.Now()); err != nil {
		log.Printf("ingest: mark cancelled for %d: %v", ing.ID, err)
		return
	}
	m.publishReload(ing.ID)
}

// cancelDuringVerify handles a cancellation observed while VERIFYING: the
// copy itself had already finished without error, so the files are left in
// place - they just haven't been double-checked, which the CANCELLED
// status and message make explicit rather than implying SAFE_TO_EJECT.
func (m *Manager) cancelDuringVerify(ing *model.Ingest) {
	msg := "Verificación cancelada por el operador: los archivos se copiaron pero no se verificaron."
	if err := m.db.MarkCancelled(ing.ID, msg, time.Now()); err != nil {
		log.Printf("ingest: mark cancelled for %d: %v", ing.ID, err)
		return
	}
	m.publishReload(ing.ID)
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

// Cancel stops an in-progress copy or verification. Only valid from
// COPYING or VERIFYING - there is nothing to cancel in any other state.
// The actual CANCELLED transition happens asynchronously in
// runCopyAndVerify once it observes the cancelled context, so callers
// should rely on the SSE ingest_updated event (or poll Get) rather than
// this call's return value for the final state.
func (m *Manager) Cancel(id int64) (*model.Ingest, error) {
	ing, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if ing.Status != model.StatusCopying && ing.Status != model.StatusVerifying {
		return nil, fmt.Errorf("%w: current status is %s", ErrInvalidState, ing.Status)
	}

	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if !ok {
		// Shouldn't happen: COPYING/VERIFYING implies a registered cancel
		// func. Defensive rather than a panic.
		return nil, fmt.Errorf("%w: no running operation found for ingest %d", ErrInvalidState, id)
	}
	cancel()

	return ing, nil
}

// Retry re-attempts a job that ended in ERROR or CANCELLED, reusing the
// same camera/card number/destination it already had (no new camera
// assignment, so no new card number is allocated). It always redoes the
// full copy+verify from scratch: the copy engine overwrites destination
// files idempotently, so this is safe even if the previous attempt got
// partway through.
func (m *Manager) Retry(id int64) (*model.Ingest, error) {
	ing, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if ing.Status != model.StatusError && ing.Status != model.StatusCancelled {
		return nil, fmt.Errorf("%w: current status is %s", ErrInvalidState, ing.Status)
	}

	if err := m.db.ResetForRetry(id, time.Now()); err != nil {
		return nil, err
	}
	ing, err = m.Get(id)
	if err != nil {
		return nil, err
	}
	m.broker.publish(Event{Type: EventIngestUpdated, Ingest: ing})

	m.launchCopyAndVerify(ing)

	return ing, nil
}

// ResumeInterrupted finds jobs an ungraceful shutdown left stuck in
// COPYING or VERIFYING (no goroutine is driving them after a restart -
// the process that owned them is gone) and redrives each one from
// scratch, the same way Retry does. Call once at startup, before Run.
func (m *Manager) ResumeInterrupted() error {
	stuck, err := m.db.ListByStatus(model.StatusCopying, model.StatusVerifying)
	if err != nil {
		return fmt.Errorf("list interrupted jobs: %w", err)
	}

	for _, ing := range stuck {
		log.Printf("ingest: resuming job %d (was %s when the process last stopped)", ing.ID, ing.Status)
		if err := m.db.ResetForRetry(ing.ID, time.Now()); err != nil {
			log.Printf("ingest: resume %d: %v", ing.ID, err)
			continue
		}
		reloaded, err := m.Get(ing.ID)
		if err != nil {
			log.Printf("ingest: resume %d: reload: %v", ing.ID, err)
			continue
		}
		m.broker.publish(Event{Type: EventIngestUpdated, Ingest: reloaded})
		m.launchCopyAndVerify(reloaded)
	}
	return nil
}
