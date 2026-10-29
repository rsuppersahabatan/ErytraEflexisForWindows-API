package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSaveFileIsIdempotentByHash(t *testing.T) {
	st := openTemp(t)

	inserted, err := st.SaveFile("OUT.dat", "hash-a", 100, 4)
	if err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if !inserted {
		t.Fatal("SaveFile pertama inserted = false, want true")
	}

	inserted, err = st.SaveFile("OUT.dat", "hash-a", 100, 4)
	if err != nil {
		t.Fatalf("SaveFile kedua: %v", err)
	}
	if inserted {
		t.Fatal("SaveFile kedua inserted = true, want false (hash sama)")
	}
}

func TestEnqueueDedupesByIdemKey(t *testing.T) {
	st := openTemp(t)

	items := []EnqueueInput{
		{IdemKey: "k1", FileHash: "h1", Payload: `{"a":1}`},
		{IdemKey: "k2", FileHash: "h1", Payload: `{"a":2}`},
		{IdemKey: "k1", FileHash: "h2", Payload: `{"a":3}`}, // duplikat
	}
	inserted, err := st.Enqueue(items)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got, want := inserted, 2; got != want {
		t.Fatalf("inserted = %d, want %d", got, want)
	}

	pending, err := st.NextPending(10, time.Now())
	if err != nil {
		t.Fatalf("NextPending: %v", err)
	}
	if got, want := len(pending), 2; got != want {
		t.Fatalf("pending = %d, want %d", got, want)
	}
}

func TestRetryScheduleHidesItemUntilDue(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Enqueue([]EnqueueInput{{IdemKey: "k1", FileHash: "h1", Payload: "{}"}}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, _ := st.NextPending(10, time.Now())
	id := items[0].ID

	if err := st.MarkSending(id); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if err := st.MarkRetry(id, 503, "prima down", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}

	now, _ := st.NextPending(10, time.Now())
	if len(now) != 0 {
		t.Errorf("item masih muncul sebelum jadwal: %d", len(now))
	}
	later, _ := st.NextPending(10, time.Now().Add(2*time.Minute))
	if len(later) != 1 {
		t.Errorf("item tidak muncul setelah jadwal: %d", len(later))
	}
	if got, want := later[0].Attempts, 1; got != want {
		t.Errorf("Attempts = %d, want %d", got, want)
	}
}

func TestStatsAndRequeue(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Enqueue([]EnqueueInput{
		{IdemKey: "k1", FileHash: "h1", Payload: "{}"},
		{IdemKey: "k2", FileHash: "h1", Payload: "{}"},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, _ := st.NextPending(10, time.Now())
	if err := st.MarkSent(items[0].ID, 200); err != nil {
		t.Fatalf("MarkSent: %v", err)
	}
	if err := st.MarkPermanent(items[1].ID, 422, "field wajib kosong"); err != nil {
		t.Fatalf("MarkPermanent: %v", err)
	}

	stats, err := st.Stats(8)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Sent != 1 || stats.Failed != 1 || stats.Pending != 0 {
		t.Errorf("stats = %+v, want sent=1 failed=1 pending=0", stats)
	}
	if stats.LastError == "" {
		t.Error("LastError kosong, want terisi dari kegagalan permanen")
	}

	if err := st.Requeue(items[1].ID); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	again, _ := st.NextPending(10, time.Now())
	if len(again) != 1 {
		t.Errorf("setelah Requeue, pending = %d, want 1", len(again))
	}
}

// Item yang terus gagal harus tetap diulang (data tidak boleh hilang), tetapi
// harus terlihat sebagai "stuck" begitu melewati max_attempts.
func TestStatsMarksStuckAfterMaxAttempts(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Enqueue([]EnqueueInput{
		{IdemKey: "k1", FileHash: "h1", Payload: "{}"},
		{IdemKey: "k2", FileHash: "h1", Payload: "{}"},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, _ := st.NextPending(10, time.Now())
	if len(items) != 2 {
		t.Fatalf("pending = %d, want 2", len(items))
	}

	// k1 gagal 3 kali, k2 hanya sekali. Pola ini meniru sender sungguhan:
	// attempts bertambah di MarkSending, lalu MarkRetry menjadwalkan ulang.
	for i := 0; i < 3; i++ {
		if err := st.MarkSending(items[0].ID); err != nil {
			t.Fatalf("MarkSending: %v", err)
		}
		if err := st.MarkRetry(items[0].ID, 503, "prima down", time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("MarkRetry: %v", err)
		}
	}
	if err := st.MarkSending(items[1].ID); err != nil {
		t.Fatalf("MarkSending: %v", err)
	}
	if err := st.MarkRetry(items[1].ID, 503, "prima down", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}

	// Batas 3: hanya k1 yang sudah menyentuh ambang.
	stats, err := st.Stats(3)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got, want := stats.Stuck, 1; got != want {
		t.Errorf("Stuck = %d, want %d", got, want)
	}
	if got, want := stats.Pending, 2; got != want {
		t.Errorf("Pending = %d, want %d (stuck tetap diantre, bukan dibuang)", got, want)
	}

	// Batas lebih tinggi: tidak ada yang stuck.
	stats, err = st.Stats(10)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Stuck != 0 {
		t.Errorf("Stuck = %d, want 0 untuk max_attempts=10", stats.Stuck)
	}

	// Batas 0 mematikan penandaan.
	stats, err = st.Stats(0)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Stuck != 0 {
		t.Errorf("Stuck = %d, want 0 untuk max_attempts=0", stats.Stuck)
	}
}
