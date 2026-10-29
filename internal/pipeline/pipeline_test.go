package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/config"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/parser"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/sender"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/watcher"
)

func sampleFile(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "OUT_sample.dat"))
	if err != nil {
		t.Fatalf("baca testdata: %v", err)
	}
	return data
}

type primaMock struct {
	mu   sync.Mutex
	keys []string
	body []string
}

func (p *primaMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.keys = append(p.keys, r.Header.Get("Idempotency-Key"))
		p.body = append(p.body, string(body))
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func writeConfig(t *testing.T, root, primaURL string) string {
	t.Helper()
	path := filepath.Join(root, "agent.yaml")
	yaml := fmt.Sprintf(`
machine:
  serial: "059-000-1027"
timezone: Asia/Jakarta
inbox:
  dir: %q
  filename: "OUT.dat"
  poll_interval: 10ms
  claim: rename
  delete_deadline: 30s
encoding: utf-8
spool:
  dir: %q
  archive_dir: %q
  error_dir: %q
  db_path: %q
prima:
  url: %q
  token_env: TEST_PRIMA_TOKEN
  timeout: 5s
  idempotency_header: "Idempotency-Key"
  retry:
    max_attempts: 3
    backoff: "1ms"
    retry_on_status: [500, 503]
    treat_409_as_success: true
templates:
  "Erytra.pln":
    components: []
    interpretation_index: 6
api:
  addr: "127.0.0.1:0"
log:
  level: error
  format: text
`,
		filepath.Join(root, "share"),
		filepath.Join(root, "spool"),
		filepath.Join(root, "archive"),
		filepath.Join(root, "error"),
		filepath.Join(root, "agent.db"),
		primaURL,
	)
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("tulis config: %v", err)
	}
	return path
}

// TestEndToEnd menelusuri rantai penuh: OUT.dat di folder bersama → klaim →
// parse → antrean → POST ke Prima → arsip.
func TestEndToEnd(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"share", "spool", "archive", "error"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	mock := &primaMock{}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	t.Setenv("TEST_PRIMA_TOKEN", "token-uji")

	cfg, err := config.Load(writeConfig(t, root, srv.URL))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	st, err := store.Open(cfg.Spool.DBPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := parser.NewRegistry(cfg.Templates, cfg.Machine.Serial, cfg.Location())
	proc := New(cfg, st, reg, log)
	w := watcher.New(cfg.Inbox.Dir, cfg.Inbox.Filename, cfg.Inbox.Claim,
		cfg.Inbox.PollInterval.D(), cfg.Spool.Dir, cfg.Spool.ErrorDir, log)
	snd := sender.New(cfg, st, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handled := make(chan error, 1)
	go w.Run(ctx, func(c watcher.Claimed) error {
		err := proc.Handle(c)
		handled <- err
		return err
	})

	// Mesin mengirim hasil.
	if err := os.WriteFile(filepath.Join(cfg.Inbox.Dir, "OUT.dat"), sampleFile(t), 0o644); err != nil {
		t.Fatalf("tulis OUT.dat: %v", err)
	}

	select {
	case err := <-handled:
		if err != nil {
			t.Fatalf("Handle: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout menunggu berkas diproses")
	}
	cancel()

	// Folder bersama harus sudah bersih.
	if _, err := os.Stat(filepath.Join(cfg.Inbox.Dir, "OUT.dat")); !os.IsNotExist(err) {
		t.Error("OUT.dat masih ada di folder bersama")
	}

	// 4 hasil masuk antrean (order X tanpa hasil tidak dihitung).
	pending, err := st.NextPending(10, time.Now())
	if err != nil {
		t.Fatalf("NextPending: %v", err)
	}
	if got, want := len(pending), 4; got != want {
		t.Fatalf("antrean = %d, want %d", got, want)
	}

	// Kirim semuanya ke Prima.
	snd.Flush(context.Background())

	stats, err := st.Stats(8)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sent != 4 || stats.Pending != 0 {
		t.Errorf("stats = %+v, want sent=4 pending=0", stats)
	}

	mock.mu.Lock()
	keys := append([]string(nil), mock.keys...)
	bodies := append([]string(nil), mock.body...)
	mock.mu.Unlock()

	if len(keys) != 4 {
		t.Fatalf("Prima menerima %d request, want 4", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" {
			t.Error("ada request tanpa Idempotency-Key")
		}
		if seen[k] {
			t.Errorf("Idempotency-Key duplikat: %q", k)
		}
		seen[k] = true
	}
	if !strings.Contains(bodies[0], `"patient_id":"MRN0001"`) {
		t.Errorf("body pertama = %s, want berisi MRN0001", bodies[0])
	}

	// Arsip harus menyimpan salinan mentah.
	archived, err := filepath.Glob(filepath.Join(cfg.Spool.ArchiveDir, "*", "*.dat"))
	if err != nil {
		t.Fatalf("glob arsip: %v", err)
	}
	if len(archived) != 1 {
		t.Errorf("berkas arsip = %d, want 1", len(archived))
	}
}

// Berkas yang sama dua kali tidak boleh menghasilkan kiriman ganda.
func TestDuplicateFileIsSkipped(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"share", "spool", "archive", "error"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, err := config.Load(writeConfig(t, root, srv.URL))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	st, err := store.Open(cfg.Spool.DBPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	proc := New(cfg, st, parser.NewRegistry(cfg.Templates, "M", cfg.Location()), log)

	// Berkas identik dua kali, dengan hash sama.
	data := sampleFile(t)
	for i := 0; i < 2; i++ {
		p := filepath.Join(cfg.Spool.Dir, fmt.Sprintf("dup-%d.dat", i))
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("tulis: %v", err)
		}
		c := watcher.Claimed{
			OriginalName: "OUT.dat",
			SpoolPath:    p,
			Hash:         "hash-sama",
			Size:         int64(len(data)),
		}
		if err := proc.Handle(c); err != nil {
			t.Fatalf("Handle ke-%d: %v", i, err)
		}
	}

	items, _ := st.NextPending(100, time.Now())
	if got, want := len(items), 4; got != want {
		t.Errorf("antrean = %d, want %d (berkas kedua harus dilewati)", got, want)
	}
}
