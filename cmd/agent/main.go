// Command agent membaca OUT.dat dari folder bersama Diana Server, mem-parse
// ASTM E1394, lalu mengirim setiap hasil ke service Prima lewat HTTP POST.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	// Zona waktu di-embed ke dalam binary. Tanpa ini time.LoadLocation hanya
	// berhasil bila GOROOT atau ZONEINFO tersedia, dan gagal saat agent berjalan
	// sebagai Windows service di PC lab yang tidak punya Go terpasang.
	_ "time/tzdata"

	"github.com/kardianos/service"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/api"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/astm"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/config"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/parser"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/pipeline"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/sender"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/store"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/watcher"
)

const version = "0.1.0"

func main() {
	var (
		configPath = flag.String("config", filepath.Join("configs", "agent.yaml"), "path berkas konfigurasi YAML")
		parsePath  = flag.String("parse", "", "parse satu berkas ASTM lalu cetak payload (tidak menyentuh folder bersama)")
		install    = flag.Bool("install", false, "pasang sebagai Windows Service lalu jalankan")
		uninstall  = flag.Bool("uninstall", false, "hentikan dan hapus Windows Service")
		showVer    = flag.Bool("version", false, "cetak versi")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("erytra-agent %s\n", version)
		return
	}

	if *parsePath != "" {
		if err := runParse(*configPath, *parsePath); err != nil {
			fmt.Fprintf(os.Stderr, "gagal: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "konfigurasi tidak valid: %v\n", err)
		os.Exit(1)
	}

	logger, closer, err := newLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}
	defer closer()

	prg := &program{cfg: cfg, log: logger}
	svc, err := service.New(prg, &service.Config{
		Name:        "ErytraAgent",
		DisplayName: "Erytra Eflexis Agent",
		Description: "Membaca OUT.dat dari folder bersama Diana Server dan mengirim hasilnya ke service Prima.",
	})
	if err != nil {
		logger.Error("gagal menyiapkan service", "err", err)
		os.Exit(1)
	}

	switch {
	case *install:
		if err := svc.Install(); err != nil {
			logger.Error("gagal memasang service", "err", err)
			os.Exit(1)
		}
		if err := svc.Start(); err != nil {
			logger.Error("service terpasang tapi gagal dijalankan", "err", err)
			os.Exit(1)
		}
		logger.Info("service terpasang dan berjalan", "name", "ErytraAgent")
		return
	case *uninstall:
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			logger.Error("gagal menghapus service", "err", err)
			os.Exit(1)
		}
		logger.Info("service dihapus")
		return
	}

	// svc.Run juga bekerja di mode konsol, jadi satu jalur untuk dua mode.
	if err := svc.Run(); err != nil {
		logger.Error("service berhenti dengan error", "err", err)
		os.Exit(1)
	}
}

// program mengimplementasikan service.Interface.
type program struct {
	cfg    *config.Config
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		p.run(ctx)
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
		}
	}
	return nil
}

func (p *program) run(ctx context.Context) {
	cfg, log := p.cfg, p.log

	st, err := store.Open(cfg.Spool.DBPath)
	if err != nil {
		log.Error("gagal membuka state lokal", "err", err)
		return
	}
	defer st.Close()

	reg := parser.NewRegistry(cfg.Templates, cfg.Machine.Serial, cfg.Location())
	proc := pipeline.New(cfg, st, reg, log)

	state := &api.State{
		StartedAt:      time.Now(),
		InboxDir:       cfg.Inbox.Dir,
		Filename:       cfg.Inbox.Filename,
		DeleteDeadline: cfg.Inbox.DeleteDeadline.D(),
		MaxAttempts:    cfg.Prima.Retry.MaxAttempts,
	}

	w := watcher.New(
		cfg.Inbox.Dir, cfg.Inbox.Filename, cfg.Inbox.Claim,
		cfg.Inbox.PollInterval.D(), cfg.Inbox.Stability.D(), cfg.Inbox.DeleteDeadline.D(),
		cfg.Spool.Dir, cfg.Spool.ErrorDir, log,
	)

	snd := sender.New(cfg, st, log)
	srv := api.New(st, state, log)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		w.Run(ctx, func(c watcher.Claimed) error {
			state.SetClaim(c.ClaimedAt, c.OriginalName, c.ClaimLatency.Milliseconds())
			return proc.Handle(c)
		})
	}()

	go func() {
		defer wg.Done()
		snd.Run(ctx)
	}()

	go func() {
		defer wg.Done()
		srv.Run(ctx, cfg.API.Addr)
	}()

	log.Info("agent berjalan",
		"version", version,
		"inbox", cfg.Inbox.Dir,
		"filename", cfg.Inbox.Filename,
		"claim", cfg.Inbox.Claim,
		"encoding", cfg.Encoding,
		"prima", cfg.Prima.URL,
		"admin", "http://"+cfg.API.Addr+"/healthz",
	)

	// Berhenti rapi saat Ctrl+C (mode konsol).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case <-sig:
		log.Info("sinyal berhenti diterima")
	}

	wg.Wait()
	log.Info("agent berhenti")
}

// runParse mem-parse satu berkas lokal dan mencetak payload, tanpa menyentuh
// folder bersama. Dipakai untuk menguji parser dengan berkas contoh mesin.
func runParse(configPath, file string) error {
	cfg, err := config.LoadLenient(configPath)
	if err != nil {
		// Mode ini harus bisa dicoba sebelum configs/agent.yaml dibuat: parser
		// hanya butuh encoding, template, dan identitas mesin, semuanya punya
		// nilai bawaan. Konfigurasi yang ada tapi rusak tetap dilaporkan.
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		cfg = config.Default()
		fmt.Fprintf(os.Stderr, "catatan: %s tidak ada, memakai nilai bawaan\n", configPath)
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	decoded, err := parser.Decode(raw, cfg.Encoding)
	if err != nil {
		return err
	}
	msg, err := astm.Parse(decoded)
	if err != nil {
		return err
	}

	reg := parser.NewRegistry(cfg.Templates, cfg.Machine.Serial, cfg.Location())
	envs := reg.Map(msg, filepath.Base(file), time.Now())

	fmt.Printf("file        : %s\n", filepath.Base(file))
	fmt.Printf("baris       : %d\n", msg.LineCount)
	fmt.Printf("pengirim    : %q serial=%q\n", msg.Sender, msg.Serial)
	fmt.Printf("terminator  : %v\n", msg.Terminated)
	fmt.Printf("order       : %d\n", len(msg.Orders))
	fmt.Printf("hasil       : %d\n", len(envs))
	if msg.Queries > 0 {
		fmt.Printf("query (Q)   : %d\n", msg.Queries)
	}
	fmt.Println(strings.Repeat("-", 60))

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for i, env := range envs {
		fmt.Printf("[%d] %s\n", i+1, env.Result.IdemKey())
		if err := enc.Encode(env.Payload); err != nil {
			return err
		}
	}
	return nil
}

func newLogger(cfg *config.Config) (*slog.Logger, func(), error) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	var w io.Writer = os.Stdout
	closer := func() {}
	if cfg.Log.File != "" {
		if dir := filepath.Dir(cfg.Log.File); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, closer, err
			}
		}
		f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, closer, err
		}
		w = io.MultiWriter(os.Stdout, f)
		closer = func() { f.Close() }
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.Log.Format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h), closer, nil
}
