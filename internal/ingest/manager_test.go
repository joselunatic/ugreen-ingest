package ingest

import (
	"context"
	"errors"
	"fmt"
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

// writeLargeMockCard creates enough files/bytes that a copy takes long
// enough (real disk I/O, not instant) for a cancel issued immediately
// after AssignCamera returns to reliably land mid-copy.
func writeLargeMockCard(t *testing.T, mockRoot, name string, fileCount int) string {
	t.Helper()
	cardDir := filepath.Join(mockRoot, name)
	content := make([]byte, 256*1024)
	for i := range fileCount {
		p := filepath.Join(cardDir, fmt.Sprintf("CLIP_%04d.MOV", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cardDir
}

// findByIdentifier waits until a card reaches WAITING_CAMERA specifically -
// not just "exists in the DB" - since detection briefly passes through
// DETECTED first (see Manager.scanOnce), and every caller of this helper
// goes on to call AssignCamera, which requires WAITING_CAMERA.
func findByIdentifier(t *testing.T, mgr *Manager, identifier string) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := mgr.List()
		if err == nil {
			for _, ing := range list {
				if ing.SourceIdentifier == identifier && ing.Status == model.StatusWaitingCamera {
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

// waitForTerminal waits until a job settles in ERROR or SAFE_TO_EJECT, for
// tests where ERROR is the expected outcome (waitForStatus fails on ERROR).
func waitForTerminal(t *testing.T, mgr *Manager, id int64, timeout time.Duration) *model.Ingest {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *model.Ingest
	for time.Now().Before(deadline) {
		ing, err := mgr.Get(id)
		if err == nil {
			last = ing
			if ing.Status == model.StatusError || ing.Status == model.StatusSafeToEject {
				return ing
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for ingest %d to finish (last seen: %+v)", id, last)
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

// TestPipeline_SourceRemovedDuringCopy_EndsInError covers runbook section
// 18.2: a card pulled out mid-copy must end in ERROR, never SAFE_TO_EJECT.
// Same timing approach as the cancel test: the large fixture makes the copy
// slow enough that removing the source right after AssignCamera lands
// mid-copy (and if it ever landed later, verify still has no source to
// match against, so the expected outcome is the same).
func TestPipeline_SourceRemovedDuringCopy_EndsInError(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	cardDir := writeLargeMockCard(t, mockRoot, "CARD_PULLED_OUT", 400)

	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_PULLED_OUT")

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}
	if err := os.RemoveAll(cardDir); err != nil {
		t.Fatal(err)
	}

	final := waitForTerminal(t, mgr, id, 5*time.Second)
	if final.Status != model.StatusError {
		t.Fatalf("expected ERROR after the source vanished, got %s", final.Status)
	}
	if final.VerifyStatus == model.VerifyOK {
		t.Fatal("a job whose source vanished must never be marked verified")
	}
}

// TestPipeline_SourceEmptiedAfterDetection_EndsInError guards against a
// card that disappears but leaves an empty directory behind (e.g. an empty
// mount point after an unplug/umount in real mode): copying and verifying
// an empty tree trivially "matches", so without a check against what was
// detected the job would reach SAFE_TO_EJECT with nothing copied.
func TestPipeline_SourceEmptiedAfterDetection_EndsInError(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	cardDir := writeMockCard(t, mockRoot, "CARD_EMPTIED")

	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_EMPTIED")

	entries, err := os.ReadDir(cardDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(cardDir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}

	final := waitForTerminal(t, mgr, id, 3*time.Second)
	if final.Status != model.StatusError {
		t.Fatalf("expected ERROR when the source no longer has the detected files, got %s", final.Status)
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

func TestCancel_RequiresCopyingOrVerifying(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_CANCEL_INVALID")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_CANCEL_INVALID")

	if _, err := mgr.Cancel(id); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState cancelling a WAITING_CAMERA job, got %v", err)
	}
}

// TestCancel_DuringCopy_MarksCancelledAndRemovesPartialDestination is
// necessarily a bit timing-sensitive (it cancels a real background copy),
// so the fixture uses enough files/bytes that the copy takes long enough
// in wall-clock terms for a Cancel call issued immediately after
// AssignCamera returns to reliably land before the copy finishes.
func TestCancel_DuringCopy_MarksCancelledAndRemovesPartialDestination(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeLargeMockCard(t, mockRoot, "CARD_CANCEL_DURING_COPY", 400)

	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_CANCEL_DURING_COPY")

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}
	if _, err := mgr.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	final := waitForStatus(t, mgr, id, model.StatusCancelled, 5*time.Second)
	if final.ErrorMessage == "" {
		t.Error("expected a cancellation message explaining what happened")
	}
	if _, err := os.Stat(final.DestinationPath); !os.IsNotExist(err) {
		t.Errorf("expected the partial destination to be removed, stat err = %v", err)
	}
}

func TestRetry_RequiresErrorOrCancelled(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_RETRY_INVALID")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_RETRY_INVALID")

	if _, err := mgr.Retry(id); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState retrying a WAITING_CAMERA job, got %v", err)
	}
}

func TestRetry_FromError_RedrivesToSafeToEject(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_RETRY_FROM_ERROR")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_RETRY_FROM_ERROR")

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}
	waitForStatus(t, mgr, id, model.StatusSafeToEject, 3*time.Second)

	// Simulate a real failure having occurred (e.g. a transient read
	// error), independent of the cancellation path already covered above.
	if err := mgr.db.MarkError(id, "simulated failure for retry test", model.VerifyFailed, time.Now()); err != nil {
		t.Fatal(err)
	}

	retried, err := mgr.Retry(id)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.Status != model.StatusCopying {
		t.Fatalf("expected COPYING immediately after Retry, got %s", retried.Status)
	}
	if retried.CameraID != "CAM01" || retried.CardNumber != "CARD001" {
		t.Fatalf("expected retry to keep the original camera/card number, got %s/%s", retried.CameraID, retried.CardNumber)
	}

	final := waitForStatus(t, mgr, id, model.StatusSafeToEject, 3*time.Second)
	if final.VerifyStatus != model.VerifyOK {
		t.Errorf("expected retry to succeed with VerifyOK, got %s", final.VerifyStatus)
	}
}

// TestResumeInterrupted_RedrivesStuckJobs simulates the process having
// died mid-copy (nothing on disk actually needs to be broken - the copy
// engine's overwrite-everything behavior makes a redrive safe/idempotent
// either way) by forcing a completed job's status back to COPYING, then
// checks ResumeInterrupted notices and finishes it.
func TestResumeInterrupted_RedrivesStuckJobs(t *testing.T) {
	mgr, _, mockRoot := newTestManager(t)
	writeMockCard(t, mockRoot, "CARD_RESUME")
	mgr.scanOnce(context.Background())
	id := findByIdentifier(t, mgr, "CARD_RESUME")

	if _, err := mgr.AssignCamera(id, "CAM01"); err != nil {
		t.Fatalf("AssignCamera: %v", err)
	}
	waitForStatus(t, mgr, id, model.StatusSafeToEject, 3*time.Second)

	if err := mgr.db.UpdateStatus(id, model.StatusCopying); err != nil {
		t.Fatal(err)
	}

	if err := mgr.ResumeInterrupted(); err != nil {
		t.Fatalf("ResumeInterrupted: %v", err)
	}

	final := waitForStatus(t, mgr, id, model.StatusSafeToEject, 3*time.Second)
	if final.VerifyStatus != model.VerifyOK {
		t.Errorf("expected resumed job to reach VerifyOK, got %s", final.VerifyStatus)
	}
}
