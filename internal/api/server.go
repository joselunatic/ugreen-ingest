// Package api exposes the ingest manager over HTTP: a small REST API for
// the tablet UI plus a Server-Sent Events stream for live status/progress.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/ugreen-ingest/ugreen-ingest/internal/config"
	"github.com/ugreen-ingest/ugreen-ingest/internal/ingest"
)

type Server struct {
	mgr *ingest.Manager
	mux *http.ServeMux
}

func NewServer(mgr *ingest.Manager, webFS fs.FS) *Server {
	s := &Server{mgr: mgr, mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/config", s.handleConfig)
	s.mux.HandleFunc("GET /api/v1/ingests", s.handleListIngests)
	s.mux.HandleFunc("GET /api/v1/ingests/{id}", s.handleGetIngest)
	s.mux.HandleFunc("POST /api/v1/ingests/{id}/assign-camera", s.handleAssignCamera)
	s.mux.HandleFunc("POST /api/v1/ingests/{id}/eject", s.handleEject)
	s.mux.HandleFunc("POST /api/v1/ingests/{id}/format", s.handleFormat)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.Handle("GET /", http.FileServer(http.FS(webFS)))

	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.mgr.Config()
	cameras := append([]string{}, cfg.Cameras...)
	cameras = append(cameras, config.ExtraCamera)
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         cfg.Mode,
		"project_name": cfg.Project.DefaultName,
		"cameras":      cameras,
	})
}

func (s *Server) handleListIngests(w http.ResponseWriter, r *http.Request) {
	ings, err := s.mgr.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ings)
}

func (s *Server) handleGetIngest(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ing, err := s.mgr.Get(id)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ing)
}

func (s *Server) handleAssignCamera(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var body struct {
		Camera string `json:"camera"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	ing, err := s.mgr.AssignCamera(id, body.Camera)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, ing)
}

func (s *Server) handleEject(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ing, err := s.mgr.Eject(id)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ing)
}

func (s *Server) handleFormat(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.mgr.Format(id); err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	subID, events := s.mgr.Subscribe()
	defer s.mgr.Unsubscribe(subID)

	// Prime the freshly opened stream with the current state, so a tablet
	// that just loaded the page doesn't wait for the next status change.
	if snapshot, err := s.mgr.List(); err == nil {
		for _, ing := range snapshot {
			writeSSE(w, ingest.Event{Type: ingest.EventIngestUpdated, Ingest: ing})
		}
		flusher.Flush()
	}

	ctx := r.Context()
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, ev ingest.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ingest.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, ingest.ErrInvalidCamera):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, ingest.ErrInvalidState), errors.Is(err, ingest.ErrCollision):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func parseID(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %w", err)
	}
	return v, nil
}
