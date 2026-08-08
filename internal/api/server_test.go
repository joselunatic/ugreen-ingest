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

	database, err := db.Open(filepath.Join(dir, "state", "ingest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

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

func waitForIngest(t *testing.T, mgr *ingest.Manager, identifier string) *model.Ingest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := mgr.List()
		if err == nil {
			for _, ing := range list {
				if ing.SourceIdentifier == identifier {
					return ing
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ingest for %q was not detected in time", identifier)
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
