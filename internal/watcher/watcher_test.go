package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const payload = "H|\\^&|||REPORT^000^ERY|||||||P|1|20261029120000\nL|1|N\n"

func newTestWatcher(t *testing.T, claim string) (*Watcher, string, string) {
	t.Helper()
	return newTestWatcherOpts(t, claim, 0, 30*time.Second)
}

func newTestWatcherOpts(t *testing.T, claim string, stability, deadline time.Duration) (*Watcher, string, string) {
	t.Helper()
	root := t.TempDir()
	shared := filepath.Join(root, "share")
	spool := filepath.Join(root, "spool")
	errDir := filepath.Join(root, "error")

	for _, d := range []string{shared, spool, errDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	w := New(shared, "OUT.dat", claim, time.Second, stability, deadline, spool, errDir,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return w, shared, spool
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("tulis %s: %v", p, err)
	}
	return p
}

func TestClaimsOnlyExactFilename(t *testing.T) {
	w, shared, spool := newTestWatcher(t, "rename")

	write(t, shared, "OUT.dat", payload)
	write(t, shared, "TX_TMP.dat", "setengah transaksi")

	var got []Claimed
	w.tick(func(c Claimed) error {
		got = append(got, c)
		return nil
	})

	if len(got) != 1 {
		t.Fatalf("klaim = %d berkas, want 1", len(got))
	}
	if got[0].OriginalName != "OUT.dat" {
		t.Errorf("OriginalName = %q, want OUT.dat", got[0].OriginalName)
	}

	// Folder bersama harus bersih dari OUT.dat, tapi TX_TMP.dat tidak boleh disentuh.
	if _, err := os.Stat(filepath.Join(shared, "OUT.dat")); !os.IsNotExist(err) {
		t.Error("OUT.dat masih ada di folder bersama, want terklaim")
	}
	if _, err := os.Stat(filepath.Join(shared, "TX_TMP.dat")); err != nil {
		t.Error("TX_TMP.dat ikut terhapus — pola *.dat akan merusak transaksi setengah jadi")
	}

	// Isi spool harus utuh dan hash-nya benar.
	data, err := os.ReadFile(got[0].SpoolPath)
	if err != nil {
		t.Fatalf("baca spool: %v", err)
	}
	if string(data) != payload {
		t.Errorf("isi spool = %q, want %q", data, payload)
	}
	sum := sha256.Sum256([]byte(payload))
	if got[0].Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("hash = %q, want %q", got[0].Hash, hex.EncodeToString(sum[:]))
	}
	if filepath.Dir(got[0].SpoolPath) != spool {
		t.Errorf("spool path = %q, want di %q", got[0].SpoolPath, spool)
	}
}

// Nama OUT.dat dipakai ulang: transaksi baru yang datang setelah klaim
// tidak boleh ikut terhapus.
func TestNewFileAfterClaimSurvives(t *testing.T) {
	w, shared, _ := newTestWatcher(t, "rename")
	write(t, shared, "OUT.dat", payload)

	w.tick(func(c Claimed) error { return nil })

	// Diana mengirim transaksi berikutnya.
	second := payload + "P|2|MRN9999|||Baru^Pasien||19900101|M|\n"
	write(t, shared, "OUT.dat", second)

	// tick berikutnya harus mengambil berkas BARU, bukan menghapusnya.
	var got []Claimed
	w.tick(func(c Claimed) error {
		got = append(got, c)
		return nil
	})
	if len(got) != 1 {
		t.Fatalf("klaim kedua = %d, want 1", len(got))
	}
	data, _ := os.ReadFile(got[0].SpoolPath)
	if string(data) != second {
		t.Error("berkas baru tidak terklaim utuh")
	}
}

// Sisa klaim dari proses yang mati harus diambil alih, bukan diabaikan.
func TestLeftoverClaimIsRecovered(t *testing.T) {
	w, shared, _ := newTestWatcher(t, "rename")
	write(t, shared, "OUT.dat.1700000000000000000.dat", payload)

	var got []Claimed
	w.tick(func(c Claimed) error {
		got = append(got, c)
		return nil
	})

	if len(got) != 1 {
		t.Fatalf("klaim leftover = %d, want 1", len(got))
	}
	if got[0].Mode != "leftover" {
		t.Errorf("Mode = %q, want leftover", got[0].Mode)
	}
}

func TestCopyVerifyDeleteMode(t *testing.T) {
	w, shared, _ := newTestWatcher(t, "copy_verify_delete")
	write(t, shared, "OUT.dat", payload)

	var got []Claimed
	w.tick(func(c Claimed) error {
		got = append(got, c)
		return nil
	})

	if len(got) != 1 {
		t.Fatalf("klaim = %d, want 1", len(got))
	}
	if got[0].Mode != "copy_verify_delete" {
		t.Errorf("Mode = %q, want copy_verify_delete", got[0].Mode)
	}
	if _, err := os.Stat(filepath.Join(shared, "OUT.dat")); !os.IsNotExist(err) {
		t.Error("OUT.dat masih ada setelah copy+verify+delete")
	}
}

func TestNoFileNoClaim(t *testing.T) {
	w, shared, _ := newTestWatcher(t, "rename")
	write(t, shared, "TX_TMP.dat", "setengah")

	called := false
	w.tick(func(c Claimed) error {
		called = true
		return nil
	})
	if called {
		t.Error("handler dipanggil padahal hanya ada TX_TMP.dat")
	}
}

// Berkas 0 byte berarti Diana masih menulis. Mengklaimnya lewat rename berarti
// memindahkan transaksi yang sedang berjalan, dan hasilnya tidak akan pernah
// sampai ke LIS. Tetapi berkas itu juga tidak boleh dibiarkan selamanya:
// folder bersama wajib bersih sebelum timeout mesin.
func TestEmptyFileWaitsUntilDeadline(t *testing.T) {
	w, shared, _ := newTestWatcherOpts(t, "rename", 0, 80*time.Millisecond)
	write(t, shared, "OUT.dat", "")

	claims := 0
	handle := func(c Claimed) error { claims++; return nil }

	w.tick(handle)
	if claims != 0 {
		t.Fatalf("berkas kosong langsung diklaim (%d klaim), want 0", claims)
	}
	if _, err := os.Stat(filepath.Join(shared, "OUT.dat")); err != nil {
		t.Fatal("berkas kosong hilang dari folder bersama padahal belum lewat deadline")
	}

	time.Sleep(100 * time.Millisecond)
	w.tick(handle)
	if claims != 1 {
		t.Fatalf("klaim setelah deadline = %d, want 1 (folder bersama harus bersih)", claims)
	}
	if _, err := os.Stat(filepath.Join(shared, "OUT.dat")); !os.IsNotExist(err) {
		t.Error("berkas kosong masih di folder bersama setelah lewat deadline")
	}
}

// Gate kestabilan: berkas yang ukurannya masih berubah tidak boleh diklaim.
func TestStabilityGateWaitsForSettledFile(t *testing.T) {
	w, shared, _ := newTestWatcherOpts(t, "rename", 120*time.Millisecond, 30*time.Second)
	p := filepath.Join(shared, "OUT.dat")

	claims := 0
	var last Claimed
	handle := func(c Claimed) error { claims++; last = c; return nil }

	const head = "H|\\^&|||REPORT^000^ERY|||||||P|1|20261029120000\n"
	write(t, shared, "OUT.dat", head)

	w.tick(handle)
	if claims != 0 {
		t.Fatalf("klaim sebelum berkas diam (%d), want 0", claims)
	}

	// Berkas masih bertambah: penghitung kestabilan harus mulai dari nol lagi.
	time.Sleep(40 * time.Millisecond)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("buka berkas: %v", err)
	}
	if _, err := f.WriteString("L|1|N\n"); err != nil {
		t.Fatalf("tulis berkas: %v", err)
	}
	f.Close()
	w.tick(handle)
	if claims != 0 {
		t.Fatalf("klaim padahal ukuran baru berubah (%d), want 0", claims)
	}

	time.Sleep(150 * time.Millisecond)
	w.tick(handle)
	if claims != 1 {
		t.Fatalf("klaim setelah berkas diam = %d, want 1", claims)
	}

	// Yang diklaim harus versi lengkap, bukan versi setengah jadi.
	data, err := os.ReadFile(last.SpoolPath)
	if err != nil {
		t.Fatalf("baca spool: %v", err)
	}
	if string(data) != head+"L|1|N\n" {
		t.Errorf("isi spool = %q, want %q", data, head+"L|1|N\n")
	}
}
