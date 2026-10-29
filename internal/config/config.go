// Package config memuat konfigurasi agent dari berkas YAML.
//
// Prinsip: semua yang berubah di lapangan (path folder bersama, deadline hapus,
// encoding, URL & token Prima) masuk ke sini, bukan ke kode.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/parser"
)

// Duration membungkus time.Duration agar bisa ditulis "30s" di YAML.
type Duration time.Duration

// UnmarshalYAML membaca string durasi ("30s", "2m").
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("durasi tidak valid %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// D mengembalikan nilai time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config adalah akar konfigurasi.
type Config struct {
	Machine   Machine                    `yaml:"machine"`
	Timezone  string                     `yaml:"timezone"`
	Inbox     Inbox                      `yaml:"inbox"`
	Encoding  string                     `yaml:"encoding"`
	Spool     Spool                      `yaml:"spool"`
	Prima     Prima                      `yaml:"prima"`
	Templates map[string]parser.Template `yaml:"templates"`
	API       API                        `yaml:"api"`
	Log       Log                        `yaml:"log"`

	// Path berkas asal, untuk logging.
	Path string `yaml:"-"`
}

// Machine mengidentifikasi alat.
type Machine struct {
	Serial string `yaml:"serial"`
	Name   string `yaml:"name"`
}

// Inbox mengatur folder bersama Diana Server.
type Inbox struct {
	// Dir adalah folder bersama. Wajib diisi; bisa share jaringan (\\server\share).
	Dir string `yaml:"dir"`
	// Filename adalah nama tetap berkas dari mesin. Default "OUT.dat".
	// Sengaja bukan pola *.dat: TX_TMP.dat ada di folder yang sama.
	Filename string `yaml:"filename"`
	// PollInterval adalah jeda pemindaian. Polling dipakai (bukan fsnotify)
	// karena event filesystem tidak andal di share jaringan.
	PollInterval Duration `yaml:"poll_interval"`
	// Claim menentukan cara mengambil berkas: "rename" (disarankan) atau
	// "copy_verify_delete" bila rename ditolak ACL.
	Claim string `yaml:"claim"`
	// Stability adalah lama berkas harus diam (ukuran+mtime tidak berubah)
	// sebelum diklaim. Default 0 = langsung klaim.
	//
	// Default 0 disengaja: Diana Server me-rename TX_TMP.dat menjadi OUT.dat
	// secara atomik, jadi berkas yang terlihat sudah utuh dan menunggu hanya
	// menambah jarak ke timeout mesin. Isi >0 hanya bila folder bersama diisi
	// dengan cara lain, misalnya operator menyalin berkas manual.
	Stability Duration `yaml:"stability"`
	// DeleteDeadline hanya untuk alarm kesehatan, bukan penundaan hapus.
	// Dipakai juga sebagai batas berkas kosong boleh dibiarkan di folder bersama.
	DeleteDeadline Duration `yaml:"delete_deadline"`
}

// Spool adalah folder kerja lokal.
type Spool struct {
	Dir        string `yaml:"dir"`
	ArchiveDir string `yaml:"archive_dir"`
	ErrorDir   string `yaml:"error_dir"`
	DBPath     string `yaml:"db_path"`
}

// Prima adalah konfigurasi service tujuan.
type Prima struct {
	URL               string   `yaml:"url"`
	Method            string   `yaml:"method"`
	AuthHeader        string   `yaml:"auth_header"`
	TokenEnv          string   `yaml:"token_env"`
	Timeout           Duration `yaml:"timeout"`
	IdempotencyHeader string   `yaml:"idempotency_header"`
	PerResult         bool     `yaml:"per_result"`
	Retry             Retry    `yaml:"retry"`
}

// Retry mengatur pengiriman ulang.
type Retry struct {
	MaxAttempts       int    `yaml:"max_attempts"`
	Backoff           string `yaml:"backoff"`
	RetryOnStatus     []int  `yaml:"retry_on_status"`
	Treat409AsSuccess bool   `yaml:"treat_409_as_success"`
}

// API mengatur server admin lokal.
type API struct {
	Addr string `yaml:"addr"`
}

// Log mengatur keluaran log.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	File   string `yaml:"file"`
}

// Load membaca konfigurasi dari berkas YAML, mengisi default, lalu memvalidasi.
func Load(path string) (*Config, error) {
	return load(path, true)
}

// LoadLenient sama seperti Load tetapi tanpa validasi wajib.
// Dipakai mode -parse yang tidak menyentuh folder bersama maupun Prima.
func LoadLenient(path string) (*Config, error) {
	return load(path, false)
}

// Default mengembalikan konfigurasi dengan nilai bawaan tanpa membaca berkas.
// Dipakai mode -parse agar bisa dicoba sebelum configs/agent.yaml dibuat.
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

func load(path string, validate bool) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baca config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Path = path
	cfg.applyDefaults()
	if validate {
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Timezone == "" {
		c.Timezone = "Asia/Jakarta"
	}
	if c.Inbox.Filename == "" {
		c.Inbox.Filename = "OUT.dat"
	}
	if c.Inbox.PollInterval == 0 {
		c.Inbox.PollInterval = Duration(2 * time.Second)
	}
	if c.Inbox.Claim == "" {
		c.Inbox.Claim = "rename"
	}
	if c.Inbox.DeleteDeadline == 0 {
		c.Inbox.DeleteDeadline = Duration(30 * time.Second)
	}
	if c.Encoding == "" {
		c.Encoding = "utf-8"
	}
	if c.Spool.Dir == "" {
		c.Spool.Dir = filepath.Join("data", "spool")
	}
	if c.Spool.ArchiveDir == "" {
		c.Spool.ArchiveDir = filepath.Join("data", "archive")
	}
	if c.Spool.ErrorDir == "" {
		c.Spool.ErrorDir = filepath.Join("data", "error")
	}
	if c.Spool.DBPath == "" {
		c.Spool.DBPath = filepath.Join("data", "agent.db")
	}
	if c.Prima.Method == "" {
		c.Prima.Method = "POST"
	}
	if c.Prima.AuthHeader == "" {
		c.Prima.AuthHeader = "token"
	}
	if c.Prima.Timeout == 0 {
		c.Prima.Timeout = Duration(10 * time.Second)
	}
	if c.Prima.IdempotencyHeader == "" {
		c.Prima.IdempotencyHeader = "Idempotency-Key"
	}
	if c.Prima.Retry.MaxAttempts == 0 {
		c.Prima.Retry.MaxAttempts = 8
	}
	if c.Prima.Retry.Backoff == "" {
		c.Prima.Retry.Backoff = "1s,5s,30s,2m,5m"
	}
	if len(c.Prima.Retry.RetryOnStatus) == 0 {
		c.Prima.Retry.RetryOnStatus = []int{429, 500, 502, 503, 504}
	}
	if c.API.Addr == "" {
		c.API.Addr = "127.0.0.1:8099"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	}
}

// Validate memeriksa hal-hal yang tidak boleh kosong.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Inbox.Dir) == "" {
		return fmt.Errorf("inbox.dir wajib diisi")
	}
	if strings.ContainsAny(c.Inbox.Filename, "*?") {
		return fmt.Errorf("inbox.filename tidak boleh memakai wildcard (mesin juga menulis TX_TMP.dat di folder yang sama)")
	}
	switch c.Inbox.Claim {
	case "rename", "copy_verify_delete":
	default:
		return fmt.Errorf("inbox.claim harus \"rename\" atau \"copy_verify_delete\", bukan %q", c.Inbox.Claim)
	}
	if c.Inbox.Stability < 0 {
		return fmt.Errorf("inbox.stability tidak boleh negatif")
	}
	if strings.TrimSpace(c.Prima.URL) == "" {
		return fmt.Errorf("prima.url wajib diisi")
	}
	switch strings.ToLower(c.Encoding) {
	case "utf-8", "utf8", "windows-1252", "ansi":
	default:
		return fmt.Errorf("encoding harus utf-8 atau windows-1252, bukan %q", c.Encoding)
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone %q tidak dikenal (contoh yang sah: Asia/Jakarta, Asia/Makassar, UTC): %w", c.Timezone, err)
	}
	return nil
}

// Location mengembalikan zona waktu mesin.
func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

// Token mengambil token Prima dari environment.
func (c *Config) Token() string {
	if c.Prima.TokenEnv == "" {
		return ""
	}
	return os.Getenv(c.Prima.TokenEnv)
}

// BackoffSchedule mengubah "1s,5s,30s" menjadi slice durasi.
func (c *Config) BackoffSchedule() []time.Duration {
	var out []time.Duration
	for _, part := range strings.Split(c.Prima.Retry.Backoff, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, err := time.ParseDuration(part)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		out = []time.Duration{time.Second}
	}
	return out
}
