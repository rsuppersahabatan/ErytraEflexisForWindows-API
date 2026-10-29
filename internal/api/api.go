// Package api menyediakan endpoint admin lokal: kesehatan agent dan antrean.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
)

// State adalah kondisi runtime yang dilaporkan ke /healthz.
type State struct {
	mu             sync.RWMutex
	StartedAt      time.Time
	InboxDir       string
	Filename       string
	DeleteDeadline time.Duration
	MaxAttempts    int
	LastClaimAt    time.Time
	LastFile       string
	LastClaimMs    int64
}

// SetClaim mencatat klaim terakhir.
func (s *State) SetClaim(at time.Time, file string, ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastClaimAt = at
	s.LastFile = file
	s.LastClaimMs = ms
}

// Snapshot menyalin state agar aman dibaca.
func (s *State) Snapshot() (last time.Time, file string, ms int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastClaimAt, s.LastFile, s.LastClaimMs
}

// Server membungkus HTTP handler admin.
type Server struct {
	store *store.Store
	state *State
	log   *slog.Logger
}

// New membuat server admin.
func New(st *store.Store, state *State, log *slog.Logger) *Server {
	return &Server{store: st, state: state, log: log}
}

// Handler mengembalikan mux lengkap.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/outbox", s.outbox)
	mux.HandleFunc("POST /api/retry/{id}", s.retry)
	mux.HandleFunc("POST /api/requeue-file/{hash}", s.requeueFile)
	return mux
}

// Run menjalankan server sampai ctx dibatalkan.
func (s *Server) Run(ctx context.Context, addr string) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	s.log.Info("server admin aktif", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		s.log.Error("server admin berhenti", "err", err)
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(s.state.MaxAttempts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	last, file, ms := s.state.Snapshot()
	now := time.Now()

	var since any
	if !last.IsZero() {
		since = int64(now.Sub(last).Seconds())
	}

	status := "ok"
	if stats.Failed > 0 || stats.Stuck > 0 {
		status = "degraded"
	}
	// Klaim yang lebih lambat dari deadline berarti mesin berisiko memunculkan error.
	overDeadline := ms > 0 && time.Duration(ms)*time.Millisecond > s.state.DeleteDeadline
	if overDeadline {
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":              status,
		"uptime_s":            int64(now.Sub(s.state.StartedAt).Seconds()),
		"inbox":               s.state.InboxDir,
		"filename":            s.state.Filename,
		"delete_deadline_ms":  s.state.DeleteDeadline.Milliseconds(),
		"last_claim_at":       zeroSafe(last),
		"last_claim_file":     file,
		"last_claim_ms":       ms,
		"seconds_since_claim": since,
		"claim_over_deadline": overDeadline,
		"max_attempts":        s.state.MaxAttempts,
		"queue":               stats,
	})
}

type outboxItem struct {
	ID            int64  `json:"id"`
	IdemKey       string `json:"idem_key"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	SentAt        string `json:"sent_at,omitempty"`
	Payload       string `json:"payload"`
}

func (s *Server) outbox(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	items, err := s.store.Recent(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	want := r.URL.Query().Get("status")

	out := make([]outboxItem, 0, len(items))
	for _, it := range items {
		if want != "" && it.Status != want {
			continue
		}
		p := it.Payload
		if len(p) > 600 {
			p = p[:600] + "…"
		}
		out = append(out, outboxItem{
			ID: it.ID, IdemKey: it.IdemKey, Status: it.Status,
			Attempts: it.Attempts, NextAttemptAt: it.NextAttemptAt,
			HTTPStatus: it.HTTPStatus, LastError: it.LastError,
			SentAt: it.SentAt, Payload: p,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "items": out})
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id tidak valid"})
		return
	}
	if err := s.store.Requeue(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.log.Info("item dimasukkan ulang ke antrean", "id", id)
	writeJSON(w, http.StatusOK, map[string]any{"status": "requeued", "id": id})
}

func (s *Server) requeueFile(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	n, err := s.store.RequeueFile(hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "requeued", "file_hash": hash, "items": n})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func zeroSafe(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
