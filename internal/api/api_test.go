package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
)

func newServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	state := &State{
		StartedAt:      time.Now().Add(-time.Minute),
		InboxDir:       `\\server\share`,
		Filename:       "OUT.dat",
		DeleteDeadline: 30 * time.Second,
	}
	return New(st, state, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func get(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestHealthzOK(t *testing.T) {
	s, _ := newServer(t)
	body := get(t, s, "/healthz")

	if got := body["status"]; got != "ok" {
		t.Errorf("status = %v, want ok", got)
	}
	if got := body["filename"]; got != "OUT.dat" {
		t.Errorf("filename = %v, want OUT.dat", got)
	}
	if got := body["claim_over_deadline"]; got != false {
		t.Errorf("claim_over_deadline = %v, want false", got)
	}
}

// Klaim yang lebih lambat dari timeout Diana Server harus terlihat sebagai degraded.
func TestHealthzFlagsSlowClaim(t *testing.T) {
	s, _ := newServer(t)
	s.state.SetClaim(time.Now(), "OUT.dat", 45_000) // 45 detik > deadline 30 detik

	body := get(t, s, "/healthz")
	if got := body["status"]; got != "degraded" {
		t.Errorf("status = %v, want degraded", got)
	}
	if got := body["claim_over_deadline"]; got != true {
		t.Errorf("claim_over_deadline = %v, want true", got)
	}
}

func TestOutboxListingAndRetry(t *testing.T) {
	s, st := newServer(t)
	if _, err := st.Enqueue([]store.EnqueueInput{
		{IdemKey: "k1", FileHash: "h1", Payload: `{"patient_id":"MRN1"}`},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	body := get(t, s, "/api/outbox?status=pending")
	if got := body["count"]; got != float64(1) {
		t.Fatalf("count = %v, want 1", got)
	}

	items, _ := st.NextPending(10, time.Now())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/retry/1", nil)
	req.SetPathValue("id", "1")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("retry status = %d, want 200", rec.Code)
	}

	again, _ := st.NextPending(10, time.Now())
	if len(again) != 1 || again[0].ID != items[0].ID {
		t.Error("item tidak kembali ke antrean setelah retry")
	}
}
