package sender

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/config"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
)

func testSender(url string) *Sender {
	cfg := &config.Config{}
	cfg.Prima.URL = url
	cfg.Prima.Method = "POST"
	cfg.Prima.AuthHeader = "token"
	cfg.Prima.TokenEnv = "TEST_PRIMA_TOKEN"
	cfg.Prima.Timeout = config.Duration(2 * time.Second)
	cfg.Prima.IdempotencyHeader = "Idempotency-Key"
	cfg.Prima.Retry.MaxAttempts = 3
	cfg.Prima.Retry.Backoff = "1ms,2ms"
	cfg.Prima.Retry.RetryOnStatus = []int{429, 500, 502, 503, 504}
	cfg.Prima.Retry.Treat409AsSuccess = true

	return New(cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func item() store.Item {
	return store.Item{
		ID:      1,
		IdemKey: "MRN0001|abo.gru|2026-10-29T03:15:00Z",
		Payload: `{"patient_id":"MRN0001"}`,
	}
}

func TestSendSuccess(t *testing.T) {
	var gotKey, gotToken, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotToken = r.Header.Get("token")
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42}`))
	}))
	defer srv.Close()

	t.Setenv("TEST_PRIMA_TOKEN", "rahasia")

	outcome, status, _, err := testSender(srv.URL).Send(context.Background(), item())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if outcome != OutcomeSent {
		t.Errorf("outcome = %v, want sent", outcome)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if gotKey != item().IdemKey {
		t.Errorf("Idempotency-Key = %q, want %q", gotKey, item().IdemKey)
	}
	if gotToken != "rahasia" {
		t.Errorf("token = %q, want %q", gotToken, "rahasia")
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
}

// 409 berarti sudah ada di Prima: diperlakukan sukses agar tidak dikirim ulang.
func TestSendConflictTreatedAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	outcome, status, _, _ := testSender(srv.URL).Send(context.Background(), item())
	if outcome != OutcomeSent {
		t.Errorf("outcome = %v, want sent untuk 409", outcome)
	}
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
}

// 400 adalah kesalahan permanen: jangan diulang selamanya.
func TestSendBadRequestIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"patient_id wajib"}`))
	}))
	defer srv.Close()

	outcome, status, msg, _ := testSender(srv.URL).Send(context.Background(), item())
	if outcome != OutcomePermanent {
		t.Errorf("outcome = %v, want permanent", outcome)
	}
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if msg == "" {
		t.Error("pesan error kosong, want berisi body respons")
	}
}

func TestSendServerErrorIsRetryable(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504, 429} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))

		outcome, status, _, err := testSender(srv.URL).Send(context.Background(), item())
		srv.Close()

		if outcome != OutcomeRetry {
			t.Errorf("status %d: outcome = %v, want retry", code, outcome)
		}
		if status != code {
			t.Errorf("status = %d, want %d", status, code)
		}
		if err == nil {
			t.Errorf("status %d: err = nil, want error", code)
		}
	}
}

// Gangguan jaringan belum tentu berarti Prima gagal menyimpan, jadi diulang.
func TestNetworkErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // server mati

	outcome, _, _, err := testSender(url).Send(context.Background(), item())
	if outcome != OutcomeRetry {
		t.Errorf("outcome = %v, want retry", outcome)
	}
	if err == nil {
		t.Error("err = nil, want error koneksi")
	}
}

func TestBackoffCaps(t *testing.T) {
	s := testSender("http://example.invalid")
	if got, want := s.delay(1), time.Millisecond; got != want {
		t.Errorf("delay(1) = %v, want %v", got, want)
	}
	if got, want := s.delay(2), 2*time.Millisecond; got != want {
		t.Errorf("delay(2) = %v, want %v", got, want)
	}
	// Melewati panjang jadwal: pakai nilai terakhir, jangan panik.
	if got, want := s.delay(99), 2*time.Millisecond; got != want {
		t.Errorf("delay(99) = %v, want %v (nilai terakhir)", got, want)
	}
}
