package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/db"
	"github.com/ugreen-ingest/ugreen-ingest/internal/device"
	"github.com/ugreen-ingest/ugreen-ingest/internal/ingest"
	"github.com/ugreen-ingest/ugreen-ingest/internal/model"
	"github.com/ugreen-ingest/ugreen-ingest/internal/verify"
)

func newTestServer(t *testing.T) (*Server, *ingest.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	mockRoot := filepath.Join(dir, "cards")
	if err := os.MkdirAll(mockRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.MockRoot = mockRoot
	cfg.DestinationRoot = filepath.Join(dir, "ingest")
	cfg.Cameras = []string{"CAM01"}
	if err := os.MkdirAll(cfg.DestinationRoot, 0o755); err != nil {
		t.Fatal(err)
	}

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
	mgr := ingest.NewManager(cfg, database, device.NewMockProvider(mockRoot), hasher)

	webFS := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	return NewServer(mgr, webFS), mgr, mockRoot
}

func createMockCard(t *testing.T, mockRoot, name string) {
	t.Helper()
	p := filepath.Join(mockRoot, name, "clip.mov")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("clip-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitForIngest waits until a card reaches WAITING_CAMERA specifically -
// not just "exists in the DB" - since detection briefly passes through
// DETECTED first (see Manager.scanOnce) and tests calling this want a card
// that's actually ready for camera assignment.
func waitForIngest(t *testing.T, mgr *ingest.Manager, identifier string) *model.Ingest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := mgr.List()
		if err == nil {
			for _, ing := range list {
				if ing.SourceIdentifier == identifier && ing.Status == model.StatusWaitingCamera {
					return ing
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ingest for %q did not reach WAITING_CAMERA in time", identifier)
	return nil
}

func doJSON(srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "GET", "/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestConfig_IncludesExtraCamera(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "GET", "/api/v1/config", nil)
	var body struct {
		Cameras []string `json:"cameras"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range body.Cameras {
		if c == config.ExtraCamera {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q in cameras list, got %v", config.ExtraCamera, body.Cameras)
	}
}

func TestAssignCamera_UnknownID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "POST", "/api/v1/ingests/999/assign-camera", map[string]string{"camera": "CAM01"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAssignCamera_InvalidCameraReturns400(t *testing.T) {
	srv, mgr, mockRoot := newTestServer(t)
	createMockCard(t, mockRoot, "CARD_A")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	ing := waitForIngest(t, mgr, "CARD_A")

	rec := doJSON(srv, "POST", "/api/v1/ingests/"+itoa(ing.ID)+"/assign-camera", map[string]string{"camera": "NOTACAMERA"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEject_BeforeSafeToEjectReturns409(t *testing.T) {
	srv, mgr, mockRoot := newTestServer(t)
	createMockCard(t, mockRoot, "CARD_B")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	ing := waitForIngest(t, mgr, "CARD_B")

	rec := doJSON(srv, "POST", "/api/v1/ingests/"+itoa(ing.ID)+"/eject", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCancel_BeforeCopyingReturns409(t *testing.T) {
	srv, mgr, mockRoot := newTestServer(t)
	createMockCard(t, mockRoot, "CARD_CANCEL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	ing := waitForIngest(t, mgr, "CARD_CANCEL")

	rec := doJSON(srv, "POST", "/api/v1/ingests/"+itoa(ing.ID)+"/cancel", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRetry_BeforeErrorReturns409(t *testing.T) {
	srv, mgr, mockRoot := newTestServer(t)
	createMockCard(t, mockRoot, "CARD_RETRY")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	ing := waitForIngest(t, mgr, "CARD_RETRY")

	rec := doJSON(srv, "POST", "/api/v1/ingests/"+itoa(ing.ID)+"/retry", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFormat_AlwaysDisabled(t *testing.T) {
	srv, mgr, mockRoot := newTestServer(t)
	createMockCard(t, mockRoot, "CARD_C")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)

	ing := waitForIngest(t, mgr, "CARD_C")

	rec := doJSON(srv, "POST", "/api/v1/ingests/"+itoa(ing.ID)+"/format", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d: %s", rec.Code, rec.Body.String())
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestStatus_ReturnsActiveEventAndStorage(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "GET", "/api/v1/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ActiveEvent string `json:"active_event"`
		Storage     struct {
			BytesTotal  uint64  `json:"bytes_total"`
			PercentUsed float64 `json:"percent_used"`
		} `json:"storage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ActiveEvent != "TEST_EVENT" {
		t.Errorf("expected active_event TEST_EVENT, got %q", body.ActiveEvent)
	}
	if body.Storage.BytesTotal == 0 {
		t.Error("expected a nonzero bytes_total for the destination filesystem")
	}
}

func TestListProjects_ReturnsBootstrapEvent(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "GET", "/api/v1/projects", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var events []struct {
		Name     string `json:"name"`
		IsActive bool   `json:"is_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Name != "TEST_EVENT" || !events[0].IsActive {
		t.Fatalf("expected exactly one active bootstrap event, got %+v", events)
	}
}

func TestCreateProject_InvalidNameReturns400(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(srv, "POST", "/api/v1/projects", map[string]any{"name": "bad/name", "cameras": []string{"CAM01"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAndActivateProject(t *testing.T) {
	srv, _, _ := newTestServer(t)

	createRec := doJSON(srv, "POST", "/api/v1/projects", map[string]any{
		"name":    "NEW_FESTIVAL",
		"cameras": []string{"CAM01", "DRONE"},
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	activateRec := doJSON(srv, "POST", "/api/v1/projects/"+itoa(created.ID)+"/activate", nil)
	if activateRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", activateRec.Code, activateRec.Body.String())
	}

	statusRec := doJSON(srv, "GET", "/api/v1/status", nil)
	var status struct {
		ActiveEvent string `json:"active_event"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ActiveEvent != "NEW_FESTIVAL" {
		t.Fatalf("expected NEW_FESTIVAL to be active, got %q", status.ActiveEvent)
	}
}
