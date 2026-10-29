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
	root := t.TempDir()
	shared := filepath.Join(root, "share")
	spool := filepath.Join(root, "spool")
	errDir := filepath.Join(root, "error")

	for _, d := range []string{shared, spool, errDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	w := New(shared, "OUT.dat", claim, time.Second, spool, errDir,
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
