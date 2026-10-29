// Package pipeline merangkai klaim berkas → parse → antrean.
package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/astm"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/config"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/model"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/parser"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/watcher"
)

// Processor mengubah berkas yang sudah diklaim menjadi baris antrean.
type Processor struct {
	cfg      *config.Config
	store    *store.Store
	registry *parser.Registry
	loc      *time.Location
	log      *slog.Logger
}

// New membuat processor.
func New(cfg *config.Config, st *store.Store, reg *parser.Registry, log *slog.Logger) *Processor {
	return &Processor{
		cfg:      cfg,
		store:    st,
		registry: reg,
		loc:      cfg.Location(),
		log:      log,
	}
}

// Handle dipanggil watcher untuk setiap berkas yang berhasil diklaim.
func (p *Processor) Handle(c watcher.Claimed) error {
	raw, err := os.ReadFile(c.SpoolPath)
	if err != nil {
		return p.fail(c, "baca spool", err)
	}

	decoded, err := parser.Decode(raw, p.cfg.Encoding)
	if err != nil {
		return p.fail(c, "decode", err)
	}

	msg, err := astm.Parse(decoded)
	if err != nil {
		return p.fail(c, "parse", err)
	}

	// Berkas tanpa record L bisa berarti ekspor terpotong. Jangan kirim.
	if !msg.Complete() {
		return p.fail(c, "validasi",
			fmt.Errorf("pesan tidak lengkap (terminated=%v, orders=%d, queries=%d)",
				msg.Terminated, len(msg.Orders), msg.Queries))
	}

	inserted, err := p.store.SaveFile(c.OriginalName, c.Hash, c.Size, countResults(msg))
	if err != nil {
		return p.fail(c, "simpan ingest_file", err)
	}
	if !inserted {
		p.log.Warn("berkas sudah pernah diproses, dilewati", "hash", c.Hash)
		return p.archive(c)
	}

	receivedAt := time.Now()
	envelopes := p.registry.Map(msg, filepath.Base(c.SpoolPath), receivedAt)
	if len(envelopes) == 0 {
		return p.fail(c, "validasi", fmt.Errorf("tidak ada hasil (record R) yang bisa dipetakan"))
	}

	// Hasil "Dis" tidak ditandai abnormal oleh mesin. Kalau tidak dilaporkan
	// di sini, satu-satunya jejaknya adalah field discrepancy di payload.
	if n := countDiscrepancy(envelopes); n > 0 {
		p.log.Warn("hasil discrepancy (Dis) terdeteksi — perlu diperiksa operator",
			"file", c.OriginalName, "jumlah", n, "dari", len(envelopes))
	}

	items := make([]store.EnqueueInput, 0, len(envelopes))
	for _, env := range envelopes {
		body, err := json.Marshal(env.Payload)
		if err != nil {
			return p.fail(c, "marshal payload", err)
		}
		items = append(items, store.EnqueueInput{
			IdemKey:  env.Result.IdemKey(),
			FileHash: c.Hash,
			Payload:  string(body),
		})
	}

	queued, err := p.store.Enqueue(items)
	if err != nil {
		return p.fail(c, "enqueue", err)
	}

	p.log.Info("berkas diproses",
		"file", c.OriginalName,
		"hasil", len(envelopes),
		"baru_diantrekan", queued,
		"duplikat", len(envelopes)-queued,
		"sender", msg.Sender,
		"serial", msg.Serial,
	)

	return p.archive(c)
}

func countResults(msg *astm.Message) int {
	n := 0
	for _, o := range msg.Orders {
		n += len(o.Results)
	}
	return n
}

func countDiscrepancy(envelopes []model.Envelope) int {
	n := 0
	for _, env := range envelopes {
		if env.Result.Discrepancy {
			n++
		}
	}
	return n
}

// archive memindahkan berkas spool ke arsip harian.
func (p *Processor) archive(c watcher.Claimed) error {
	dir := filepath.Join(p.cfg.Spool.ArchiveDir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return moveFile(c.SpoolPath, filepath.Join(dir, filepath.Base(c.SpoolPath)))
}

// fail memindahkan berkas ke folder error beserta alasannya, lalu mengembalikan error.
func (p *Processor) fail(c watcher.Claimed, stage string, cause error) error {
	_ = p.store.MarkFileError(c.Hash, stage+": "+cause.Error())

	dir := filepath.Join(p.cfg.Spool.ErrorDir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.log.Error("gagal membuat folder error", "err", err)
		return cause
	}

	dst := filepath.Join(dir, filepath.Base(c.SpoolPath))
	if err := moveFile(c.SpoolPath, dst); err != nil {
		p.log.Error("gagal memindahkan berkas gagal", "err", err)
		return cause
	}

	reason := fmt.Sprintf("stage: %s\nwaktu: %s\nasal: %s\nhash: %s\nerror: %v\n",
		stage, time.Now().Format(time.RFC3339), c.OriginalName, c.Hash, cause)
	_ = os.WriteFile(dst+".reason.txt", []byte(reason), 0o644)

	p.log.Error("berkas dipindahkan ke folder error", "stage", stage, "file", dst, "err", cause)
	return cause
}

// moveFile memindahkan berkas; bila beda volume, disalin lalu dihapus.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}
