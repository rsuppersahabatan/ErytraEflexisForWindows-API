// Package watcher memantau folder bersama Diana Server dan mengklaim OUT.dat.
//
// Dua keputusan penting di sini:
//  1. Yang dicari adalah nama persis "OUT.dat", bukan pola *.dat — Diana Server
//     juga menulis TX_TMP.dat (berisi setengah transaksi) di folder yang sama.
//  2. Berkas diklaim dengan rename atomik. Nama OUT.dat dipakai ulang, jadi
//     menghapus tanpa klaim bisa menghapus transaksi baru yang datang di
//     tengah proses.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claimed adalah berkas yang sudah diamankan di spool lokal.
type Claimed struct {
	// OriginalName adalah nama asli di folder bersama ("OUT.dat").
	OriginalName string
	// SpoolPath adalah lokasi salinan lokal yang sudah terverifikasi.
	SpoolPath string
	// Hash adalah sha256 isi berkas.
	Hash string
	// Size dalam byte.
	Size int64
	// ClaimedAt adalah waktu klaim selesai.
	ClaimedAt time.Time
	// ClaimLatency adalah lama dari deteksi sampai folder bersama bersih.
	// Inilah angka yang harus jauh di bawah timeout Diana Server.
	ClaimLatency time.Duration
	// Mode mencatat cara klaim yang dipakai: "rename" atau "copy_verify_delete".
	Mode string
}

// Watcher memantau satu folder bersama.
type Watcher struct {
	dir       string
	filename  string
	claimMode string
	poll      time.Duration
	spoolDir  string
	errorDir  string
	log       *slog.Logger
}

// New membuat watcher.
func New(dir, filename, claimMode string, poll time.Duration, spoolDir, errorDir string, log *slog.Logger) *Watcher {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	return &Watcher{
		dir:       dir,
		filename:  filename,
		claimMode: claimMode,
		poll:      poll,
		spoolDir:  spoolDir,
		errorDir:  errorDir,
		log:       log,
	}
}

// Run memantau folder sampai ctx dibatalkan.
func (w *Watcher) Run(ctx context.Context, handle func(Claimed) error) {
	if err := os.MkdirAll(w.spoolDir, 0o755); err != nil {
		w.log.Error("gagal membuat folder spool", "dir", w.spoolDir, "err", err)
	}
	if err := os.MkdirAll(w.errorDir, 0o755); err != nil {
		w.log.Error("gagal membuat folder error", "dir", w.errorDir, "err", err)
	}

	w.tick(handle) // langsung cek saat start: tangani sisa berkas sebelumnya

	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(handle)
		}
	}
}

func (w *Watcher) tick(handle func(Claimed) error) {
	start := time.Now()

	name, leftover, ok := w.candidate()
	if !ok {
		return
	}

	var claimed Claimed
	var err error
	if leftover {
		// Sisa klaim dari proses sebelumnya: folder sudah bersih, tinggal diambil.
		claimed, err = w.takeOver(name, start)
	} else if w.claimMode == "copy_verify_delete" {
		claimed, err = w.copyVerifyDelete(name, start)
	} else {
		claimed, err = w.renameClaim(name, start)
	}
	if err != nil {
		w.log.Warn("gagal mengklaim berkas", "file", name, "err", err)
		return
	}

	w.log.Info("berkas diklaim",
		"file", claimed.OriginalName,
		"hash", shortHash(claimed.Hash),
		"size", claimed.Size,
		"mode", claimed.Mode,
		"claim_ms", claimed.ClaimLatency.Milliseconds(),
	)

	if err := handle(claimed); err != nil {
		w.log.Error("pemrosesan gagal", "file", claimed.SpoolPath, "err", err)
	}
}

// candidate mencari berkas yang harus diproses: sisa klaim lebih dulu,
// lalu OUT.dat yang asli.
func (w *Watcher) candidate() (name string, leftover bool, ok bool) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		w.log.Warn("tidak bisa membaca folder bersama", "dir", w.dir, "err", err)
		return "", false, false
	}

	prefix := strings.ToLower(w.filename) + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".dat") {
			return e.Name(), true, true
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), w.filename) {
			return e.Name(), false, true
		}
	}
	return "", false, false
}

// renameClaim mengklaim berkas dengan satu operasi rename atomik.
func (w *Watcher) renameClaim(name string, start time.Time) (Claimed, error) {
	src := filepath.Join(w.dir, name)
	dst := filepath.Join(w.dir, w.claimName())

	if err := os.Rename(src, dst); err != nil {
		// Rename ditolak (ACL share hanya mengizinkan hapus): pakai jalur cadangan.
		w.log.Warn("rename ditolak, memakai copy+verify+delete", "err", err)
		return w.copyVerifyDelete(name, start)
	}

	latency := time.Since(start)
	info, err := os.Stat(dst)
	if err != nil {
		return Claimed{}, err
	}

	// Folder bersama sudah bersih sejak rename. Sisanya boleh lambat.
	claimed, err := w.spool(dst, name, info.Size(), latency, "rename")
	if err != nil {
		return Claimed{}, err
	}
	_ = os.Remove(dst)
	return claimed, nil
}

// copyVerifyDelete dipakai bila rename tidak diizinkan.
func (w *Watcher) copyVerifyDelete(name string, start time.Time) (Claimed, error) {
	src := filepath.Join(w.dir, name)

	f, err := os.Open(src)
	if err != nil {
		return Claimed{}, err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return Claimed{}, err
	}

	spoolPath := w.spoolPath("", name)
	copyErr := copyTo(spoolPath, f)
	// Windows menolak menghapus berkas yang masih terbuka: tutup dulu.
	f.Close()
	if copyErr != nil {
		_ = os.Remove(spoolPath)
		return Claimed{}, copyErr
	}

	sum, size, err := hashFile(spoolPath)
	if err != nil {
		_ = os.Remove(spoolPath)
		return Claimed{}, err
	}
	if size != info.Size() {
		_ = os.Remove(spoolPath)
		return Claimed{}, fmt.Errorf("ukuran salinan %d != sumber %d, tidak menghapus sumber", size, info.Size())
	}

	// Verifikasi lolos: baru sumber dihapus.
	if err := os.Remove(src); err != nil {
		return Claimed{}, fmt.Errorf("salinan aman tapi gagal menghapus sumber: %w", err)
	}
	latency := time.Since(start)

	// Ganti nama spool agar memuat hash.
	final := w.spoolPath(sum, name)
	if err := os.Rename(spoolPath, final); err != nil {
		final = spoolPath
	}

	return Claimed{
		OriginalName: name,
		SpoolPath:    final,
		Hash:         sum,
		Size:         size,
		ClaimedAt:    time.Now(),
		ClaimLatency: latency,
		Mode:         "copy_verify_delete",
	}, nil
}

// takeOver mengambil alih sisa klaim (mis. proses sebelumnya mati setelah rename).
func (w *Watcher) takeOver(name string, start time.Time) (Claimed, error) {
	path := filepath.Join(w.dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return Claimed{}, err
	}
	return w.spool(path, name, info.Size(), time.Since(start), "leftover")
}

// spool menyalin berkas klaim ke folder spool lokal.
func (w *Watcher) spool(path, original string, size int64, latency time.Duration, mode string) (Claimed, error) {
	src, err := os.Open(path)
	if err != nil {
		return Claimed{}, err
	}
	defer src.Close()

	tmp := w.spoolPath("", original)
	if err := copyTo(tmp, src); err != nil {
		return Claimed{}, err
	}

	sum, got, err := hashFile(tmp)
	if err != nil {
		return Claimed{}, err
	}
	if size > 0 && got != size {
		_ = os.Remove(tmp)
		return Claimed{}, fmt.Errorf("salinan tidak utuh: %d dari %d byte", got, size)
	}

	final := w.spoolPath(sum, original)
	if err := os.Rename(tmp, final); err != nil {
		final = tmp
	}

	return Claimed{
		OriginalName: original,
		SpoolPath:    final,
		Hash:         sum,
		Size:         got,
		ClaimedAt:    time.Now(),
		ClaimLatency: latency,
		Mode:         mode,
	}, nil
}

func (w *Watcher) claimName() string {
	ext := filepath.Ext(w.filename)
	base := strings.TrimSuffix(w.filename, ext)
	return fmt.Sprintf("%s.%d.%s", base, time.Now().UnixNano(), ext)
}

func (w *Watcher) spoolPath(hash, original string) string {
	stamp := time.Now().Format("20060102-150405.000")
	short := "nohash"
	if len(hash) >= 8 {
		short = hash[:8]
	}
	ext := filepath.Ext(original)
	if ext == "" {
		ext = ".dat"
	}
	return filepath.Join(w.spoolDir, fmt.Sprintf("%s_%s%s", stamp, short, ext))
}

func copyTo(dst string, src io.Reader) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
