// Command ingest runs the ugreen-ingest appliance: device detection,
// ingest state machine, HTTP API, SSE, and the embedded tablet UI, all in
// one process. See RUNBOOK_MOY_MEDIA_INGEST.md section 7.
package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/api"
	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/db"
	"github.com/ugreen-ingest/ugreen-ingest/internal/device"
	"github.com/ugreen-ingest/ugreen-ingest/internal/ingest"
	"github.com/ugreen-ingest/ugreen-ingest/internal/verify"
	"github.com/ugreen-ingest/ugreen-ingest/web"
)

func main() {
	configPath := os.Getenv("INGEST_CONFIG")
	if configPath == "" {
		configPath = "config/config.yaml"
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	database, err := db.Open(cfg.Database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	// Seeds the first event from config on a brand-new database only; once
	// any event exists (created via the admin panel or a prior run) this is
	// a no-op. See internal/db/events.go.
	if err := database.BootstrapEvent(cfg.Project.DefaultName, cfg.Cameras); err != nil {
		log.Fatalf("bootstrap event: %v", err)
	}

	hasher, err := verify.NewHasher(cfg.Verification.Algorithm)
	if err != nil {
		log.Fatalf("verification config: %v", err)
	}

	var provider device.Provider
	switch cfg.Mode {
	case config.ModeMock:
		if err := os.MkdirAll(cfg.MockRoot, 0o755); err != nil {
			log.Fatalf("create mock root %s: %v", cfg.MockRoot, err)
		}
		provider = device.NewMockProvider(cfg.MockRoot)
	case config.ModeReal:
		provider = device.NewLinuxProvider()
	default:
		log.Fatalf("unknown INGEST_MODE %q", cfg.Mode)
	}

	if err := os.MkdirAll(cfg.DestinationRoot, 0o755); err != nil {
		log.Fatalf("create destination root %s: %v", cfg.DestinationRoot, err)
	}

	mgr := ingest.NewManager(cfg, database, provider, hasher)

	// If the process was killed/restarted mid-copy, redrive those jobs
	// from scratch now rather than leaving them stuck showing COPYING or
	// VERIFYING forever with no goroutine actually driving them.
	if err := mgr.ResumeInterrupted(); err != nil {
		log.Fatalf("resume interrupted jobs: %v", err)
	}

	staticFS, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		log.Fatalf("web assets: %v", err)
	}
	srv := api.NewServer(mgr, staticFS)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go mgr.Run(ctx)

	httpServer := &http.Server{Addr: cfg.Listen, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	activeEvent, err := mgr.ActiveEvent()
	if err != nil {
		log.Fatalf("read active event: %v", err)
	}
	log.Printf("ugreen-ingest listening on %s (mode=%s, active event=%s)", cfg.Listen, cfg.Mode, activeEvent.Name)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
