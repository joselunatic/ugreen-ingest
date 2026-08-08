package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/copyengine"
	"github.com/ugreen-ingest/ugreen-ingest/internal/db"
	"github.com/ugreen-ingest/ugreen-ingest/internal/device"
	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
	"github.com/ugreen-ingest/ugreen-ingest/internal/verify"
)

func newTestManager(t *testing.T) (*Manager, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	mockRoot := filepath.Join(dir, "cards")
	destRoot := filepath.Join(dir, "ingest")
	if err := os.MkdirAll(mockRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MockRoot = mockRoot
	cfg.DestinationRoot = destRoot
	cfg.Project.DefaultName = "TEST_EVENT"
	cfg.Cameras = []string{"CAM01", "CAM02"}

	database, err := db.Open(filepath.Join(dir, "state", "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.BootstrapEvent(cfg.Project.DefaultName, cfg.Cameras); err != nil {
		t.Fatal(err)
	}

	hasher, err := verify.NewHasher(cfg.Verification.Algorithm)
	if err != nil {
		t.Fatal(err)
	}
	provider := device.NewMockProvider(mockRoot)
	mgr := NewManager(cfg, database, provider, hasher)
	return mgr, cfg, mockRoot
}

func writeMockCard(t *testing.T, mockRoot, name string) string {
	t.Helper()
	cardDir := filepath.Join(mockRoot, name)
	files := map[string]string{
		"DCIM/100CANON/IMG_0001.JPG": "photo-data-one",
		"DCIM/100CANON/IMG_0002.JPG": "photo-data-two",
		"AUDIO/take1.wav":            "audio-data",
	}
	for rel, content := range files {
		p := filepath.Join(cardDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cardDir
}

func findByIdentifier(t *testing.T, mgr *Manager, identifier string) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := mgr.List()
		if err == nil {
			for _, ing := range list {
				if ing.SourceIdentifier == identifier {
					return ing.ID
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ingest for source %q was not detected in time", identifier)
	return 0
}

func waitForStatus(t *testing.T, mgr *Manager, id int64, want model.Status, timeout time.Duration) *model.Ingest {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *model.Ingest
	for time.Now().Before(deadline) {
		ing, err := mgr.Get(id)
		if err == nil {
			last = ing
			if ing.Status == want {
				return ing
			}
			if ing.Status == model.StatusError {
				t.Fatalf("ingest %d went to ERROR while waiting for %s: %s", id, want, ing.ErrorMessage)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for status %s on ingest %d (last seen: %+v)", want, id, last)
	return nil
}

// TestFullPipeline_DetectAssignCopyVerifySafeToEject exercises the entire
// happy path end to end: detection, camera assignment, copy, verify,
// SAFE_TO_EJECT, and eject - using the real detector/copy/verify/db code.
func TestFullPipeline_DetectAssignCopyVerifySafeToEject(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_TEST_001")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	id := findByIdentifier(t, mgr, "CARD_TEST_001")
	waitForStatus(t, mgr, id, model.StatusWaitingCamera, 3*time.Second)

	updated, err := mgr.AssignCamera(id, "CAM01")
	if err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}
	if updated.CardNumber != "CARD001" {
		t.Errorf("expected CARD001, got %s", updated.CardNumber)
	}

	final := waitForStatus(t, mgr, id, model.StatusSafeToEject, 5*time.Second)
	if final.VerifyStatus != model.VerifyOK {
		t.Errorf("expected verify_status OK, got %s", final.VerifyStatus)
	}
	if final.BytesCopied != final.BytesTotal {
		t.Errorf("expected bytes_copied == bytes_total, got %d/%d", final.BytesCopied, final.BytesTotal)
	}

	if _, err := os.Stat(filepath.Join(mockRoot, "CARD_TEST_001", "DCIM", "100CANON", "IMG_0001.JPG")); err != nil {
		t.Fatalf("source file missing/modified after ingest: %v", err)
	}

	ejected, err := mgr.Eject(final.ID)
	if err != nil {
		t.Fatalf("Eject: %v", err)
	}
	if ejected.Status != model.StatusEjected {
		t.Errorf("expected EJECTED, got %s", ejected.Status)
	}
}

// TestPipeline_CorruptedDestination_EndsInErrorNeverSafeToEject is the
// negative test required by RUNBOOK_MOY_MEDIA_INGEST.md section 18.1: copy a
// mock card, deliberately corrupt a destination file, run verification, and
// confirm the job ends in ERROR - never SAFE_TO_EJECT. It drives the same
// db/copyengine/verify calls the manager's background goroutine makes, just
// synchronously, so the corruption is deterministic instead of racing a
// background pipeline.
func TestPipeline_CorruptedDestination_EndsInErrorNeverSafeToEject(t *testing.T) {
	mgr, cfg, mockRoot := newTestManager(t)
	cardDir := writeMockCard(t, mockRoot, "CARD_TEST_002")

	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_TEST_002")

	ing, err := mgr.db.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if ing.Status != model.StatusWaitingCamera {
		t.Fatalf("expected WAITING_CAMERA after detection, got %s", ing.Status)
	}

	cardNumber, err := mgr.db.NextCardNumber(ing.ProjectName, "CAM02")
	if err != nil {
		t.Fatal(err)
	}
	destPath := filepath.Join(cfg.DestinationRoot, ing.ProjectName, "CAM02", cardNumber)
	if err := mgr.db.AssignCamera(id, "CAM02", cardNumber, destPath, time.Now()); err != nil {
		t.Fatal(err)
	}

	result, err := copyengine.Copy(context.Background(), cardDir, destPath, nil)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if err := mgr.db.MarkCopyCompleted(id, time.Now(), result.BytesTotal); err != nil {
		t.Fatal(err)
	}

	corrupted := filepath.Join(destPath, "AUDIO", "take1.wav")
	if err := os.WriteFile(corrupted, []byte("CORRUPTED-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	vres, err := verify.Verify(context.Background(), cardDir, destPath, mgr.hasher)
	if err != nil {
		t.Fatal(err)
	}
	if vres.OK() {
		t.Fatal("expected verification to detect the deliberate corruption")
	}
	if err := mgr.db.MarkError(id, "verification failed: deliberate test corruption", model.VerifyFailed, time.Now()); err != nil {
		t.Fatal(err)
	}

	final, err := mgr.db.GetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != model.StatusError {
		t.Fatalf("expected ERROR, got %s", final.Status)
	}
	if final.Status == model.StatusSafeToEject {
		t.Fatal("must never reach SAFE_TO_EJECT after a detected corruption")
	}
}

func TestAssignCamera_RejectsUnknownCamera(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_TEST_003")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_TEST_003")

	if _, err := mgr.AssignCamera(id, "NOTACAMERA"); !errors.Is(err, ErrInvalidCamera) {
		t.Fatalf("expected ErrInvalidCamera, got %v", err)
	}
}

func TestAssignCamera_RejectsDoubleAssignment(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_TEST_004")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_TEST_004")

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("first assignment should succeed: %v", err)
	}
	if _, err := mgr.AssignCamera(id, "CAM02"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState on double-assign, got %v", err)
	}
}

func TestAssignCamera_DetectsDestinationCollision(t *testing.T) {
	mgr, cfg, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_TEST_005")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_TEST_005")

	collidePath := filepath.Join(cfg.DestinationRoot, cfg.Project.DefaultName, "CAM01", "CARD001")
	if err := os.MkdirAll(collidePath, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.AssignCamera(id, "CAM01"); !errors.Is(err, ErrCollision) {
		t.Fatalf("expected ErrCollision, got %v", err)
	}
}

func TestEject_RequiresSafeToEject(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_TEST_006")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_TEST_006")

	if _, err := mgr.Eject(id); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState ejecting a WAITING_CAMERA job, got %v", err)
	}
}

func TestActivateEvent_NewDetectionsAttachToNewlyActiveEvent(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)

	writeMockCard(t, mockRoot, "CARD_UNDER_BOOTSTRAP_EVENT")
	mgr.scanOnce(context.Background())
	first := findByIdentifier(t, mgr, "CARD_UNDER_BOOTSTRAP_EVENT")
	firstIng, err := mgr.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if firstIng.ProjectName != "TEST_EVENT" {
		t.Fatalf("expected first card under bootstrap event TEST_EVENT, got %s", firstIng.ProjectName)
	}

	newEvent, err := mgr.CreateEvent("SECOND_FESTIVAL", []string{"CAM09"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ActivateEvent(newEvent.ID); err != nil {
		t.Fatal(err)
	}

	writeMockCard(t, mockRoot, "CARD_UNDER_SECOND_EVENT")
	mgr.scanOnce(context.Background())
	second := findByIdentifier(t, mgr, "CARD_UNDER_SECOND_EVENT")
	secondIng, err := mgr.Get(second)
	if err != nil {
		t.Fatal(err)
	}
	if secondIng.ProjectName != "SECOND_FESTIVAL" {
		t.Fatalf("expected second card under SECOND_FESTIVAL, got %s", secondIng.ProjectName)
	}

	// Switching the active event must never rewrite already-detected cards.
	firstIng, err = mgr.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if firstIng.ProjectName != "TEST_EVENT" {
		t.Fatalf("activating a new event retroactively changed an existing ingest's project to %s", firstIng.ProjectName)
	}
}

func TestAssignCamera_ValidatesAgainstCardsOwnEventNotWhicheverIsActiveNow(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)

	// Card detected while TEST_EVENT (cameras CAM01, CAM02) is active.
	writeMockCard(t, mockRoot, "CARD_ON_FIRST_EVENT")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_ON_FIRST_EVENT")

	// Switch active event to one with a completely different camera list.
	other, err := mgr.CreateEvent("OTHER_FESTIVAL", []string{"CAM09"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ActivateEvent(other.ID); err != nil {
		t.Fatal(err)
	}

	// The card still belongs to TEST_EVENT, so its own cameras (CAM01/CAM02)
	// must still be accepted...
	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("expected CAM01 (valid for the card's own event) to be accepted, got %v", err)
	}
}

func TestCreateEvent_RejectsInvalidNameAndEmptyCameraList(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	if _, err := mgr.CreateEvent("bad/name", []string{"CAM01"}); !errors.Is(err, ErrInvalidEventName) {
		t.Fatalf("expected ErrInvalidEventName, got %v", err)
	}
	if _, err := mgr.CreateEvent("VALID_NAME", nil); !errors.Is(err, ErrInvalidCameraList) {
		t.Fatalf("expected ErrInvalidCameraList for empty camera list, got %v", err)
	}
	if _, err := mgr.CreateEvent("VALID_NAME", []string{"OTHER"}); !errors.Is(err, ErrInvalidCameraList) {
		t.Fatalf("expected ErrInvalidCameraList when only the implicit OTHER camera is given, got %v", err)
	}
}
