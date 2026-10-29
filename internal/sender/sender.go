// Package sender mengirim hasil ke service Prima lewat HTTP POST.
//
// Aturan klasifikasi yang dipakai:
//   - 2xx, atau 409 bila treat_409_as_success → sukses
//   - 429 / 5xx / gangguan jaringan          → ulangi dengan backoff
//   - 4xx lainnya                            → permanen, jangan diulang
//
// Mengulang 400 Bad Request selamanya adalah kesalahan paling umum di
// integrator seperti ini, jadi status dibedakan secara eksplisit.
package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/config"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
)

// Outcome adalah hasil satu percobaan pengiriman.
type Outcome int

const (
	// OutcomeSent berarti Prima menerima.
	OutcomeSent Outcome = iota
	// OutcomeRetry berarti layak diulang.
	OutcomeRetry
	// OutcomePermanent berarti tidak akan berhasil walau diulang.
	OutcomePermanent
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSent:
		return "sent"
	case OutcomeRetry:
		return "retry"
	default:
		return "permanent"
	}
}

// Sender mengirim antrean ke Prima.
type Sender struct {
	url        string
	method     string
	authHeader string
	token      string
	idemHeader string
	maxAttempt int
	backoff    []time.Duration
	retryOn    map[int]bool
	treat409   bool
	client     *http.Client
	store      *store.Store
	log        *slog.Logger
}

// New membuat sender dari konfigurasi.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Sender {
	retryOn := make(map[int]bool, len(cfg.Prima.Retry.RetryOnStatus))
	for _, code := range cfg.Prima.Retry.RetryOnStatus {
		retryOn[code] = true
	}
	return &Sender{
		url:        cfg.Prima.URL,
		method:     strings.ToUpper(cfg.Prima.Method),
		authHeader: cfg.Prima.AuthHeader,
		token:      cfg.Token(),
		idemHeader: cfg.Prima.IdempotencyHeader,
		maxAttempt: cfg.Prima.Retry.MaxAttempts,
		backoff:    cfg.BackoffSchedule(),
		retryOn:    retryOn,
		treat409:   cfg.Prima.Retry.Treat409AsSuccess,
		client: &http.Client{
			Timeout: cfg.Prima.Timeout.D(),
		},
		store: st,
		log:   log,
	}
}

// Run mengirim antrean terus-menerus sampai ctx dibatalkan.
func (s *Sender) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.drain(ctx)
		}
	}
}

// Flush mengirim antrean yang siap sekarang juga, tanpa menunggu ticker.
// Dipakai tombol retry manual dan pengujian ujung-ke-ujung.
func (s *Sender) Flush(ctx context.Context) { s.drain(ctx) }

func (s *Sender) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		items, err := s.store.NextPending(20, time.Now())
		if err != nil {
			s.log.Error("gagal membaca antrean", "err", err)
			return
		}
		if len(items) == 0 {
			return
		}

		for _, item := range items {
			s.process(ctx, item)
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func (s *Sender) process(ctx context.Context, item store.Item) {
	if err := s.store.MarkSending(item.ID); err != nil {
		s.log.Error("gagal menandai sending", "id", item.ID, "err", err)
		return
	}

	outcome, status, msg, err := s.Send(ctx, item)
	attempts := item.Attempts + 1

	switch outcome {
	case OutcomeSent:
		if e := s.store.MarkSent(item.ID, status); e != nil {
			s.log.Error("gagal menandai sent", "id", item.ID, "err", e)
		}
		s.log.Info("terkirim", "id", item.ID, "key", item.IdemKey, "status", status)

	case OutcomePermanent:
		if e := s.store.MarkPermanent(item.ID, status, msg); e != nil {
			s.log.Error("gagal menandai permanent", "id", item.ID, "err", e)
		}
		s.log.Error("ditolak permanen, tidak diulang",
			"id", item.ID, "key", item.IdemKey, "status", status, "pesan", msg)

	case OutcomeRetry:
		next := time.Now().Add(s.delay(attempts))
		if e := s.store.MarkRetry(item.ID, status, msg, next); e != nil {
			s.log.Error("gagal menjadwalkan ulang", "id", item.ID, "err", e)
		}
		level := slog.LevelWarn
		if attempts >= s.maxAttempt {
			level = slog.LevelError // tetap diulang, tapi perlu perhatian operator
		}
		s.log.Log(ctx, level, "gagal, akan diulang",
			"id", item.ID, "key", item.IdemKey, "status", status,
			"attempts", attempts, "next", next.Format(time.RFC3339), "err", err)
	}
}

// delay menghitung jeda backoff; setelah jadwal habis, dipakai nilai terakhir.
func (s *Sender) delay(attempts int) time.Duration {
	if len(s.backoff) == 0 {
		return time.Second
	}
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s.backoff) {
		i = len(s.backoff) - 1
	}
	return s.backoff[i]
}

// Send melakukan satu percobaan POST.
func (s *Sender) Send(ctx context.Context, item store.Item) (Outcome, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, s.method, s.url, bytes.NewReader([]byte(item.Payload)))
	if err != nil {
		return OutcomePermanent, 0, err.Error(), err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set(s.authHeader, s.token)
	}
	if s.idemHeader != "" {
		req.Header.Set(s.idemHeader, item.IdemKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		// Timeout / jaringan: belum pasti gagal di sisi Prima, jadi ulangi.
		// Idempotency-Key mencegah duplikasi bila sebenarnya sudah tersimpan.
		return OutcomeRetry, 0, err.Error(), err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := summarize(body, resp.Status)

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return OutcomeSent, resp.StatusCode, msg, nil
	case resp.StatusCode == http.StatusConflict && s.treat409:
		return OutcomeSent, resp.StatusCode, "sudah ada di Prima (409)", nil
	case s.retryOn[resp.StatusCode]:
		return OutcomeRetry, resp.StatusCode, msg, fmt.Errorf("prima membalas %d", resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return OutcomePermanent, resp.StatusCode, msg, fmt.Errorf("prima menolak %d", resp.StatusCode)
	default:
		// 3xx tak terduga atau kode aneh: perlakukan sebagai perlu diulang.
		return OutcomeRetry, resp.StatusCode, msg, fmt.Errorf("status tak terduga %d", resp.StatusCode)
	}
}

func summarize(body []byte, status string) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return status
	}
	var v any
	if err := json.Unmarshal(body, &v); err == nil {
		compact, _ := json.Marshal(v)
		return truncate(string(compact), 400)
	}
	return truncate(string(body), 400)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
