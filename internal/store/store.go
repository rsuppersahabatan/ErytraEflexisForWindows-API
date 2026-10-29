// Package store adalah state lokal (SQLite): antrean kirim dan jejak berkas.
//
// Ini bukan database tujuan — tidak ada database tujuan di sisi kita. Perannya
// hanya menjaga agar kegagalan jaringan Prima tidak menghilangkan hasil, dan
// agar satu hasil tidak terkirim dua kali.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Status outbox.
const (
	StatusPending   = "pending"
	StatusSending   = "sending"
	StatusSent      = "sent"
	StatusPermanent = "failed_permanent"
)

// Item adalah satu baris antrean kirim.
type Item struct {
	ID            int64
	IdemKey       string
	FileHash      string
	Payload       string
	Status        string
	Attempts      int
	NextAttemptAt string
	HTTPStatus    int
	LastError     string
	SentAt        string
}

// Stats adalah ringkasan untuk endpoint kesehatan.
type Stats struct {
	Pending     int    `json:"pending"`
	Sending     int    `json:"sending"`
	Sent        int    `json:"sent"`
	Failed      int    `json:"failed_permanent"`
	Stuck       int    `json:"stuck"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
}

// Store membungkus koneksi SQLite.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS ingest_file (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  file_name     TEXT    NOT NULL,
  file_hash     TEXT    NOT NULL,
  file_size     INTEGER NOT NULL,
  record_count  INTEGER NOT NULL,
  received_at   TEXT    NOT NULL,
  status        TEXT    NOT NULL,
  last_error    TEXT,
  UNIQUE (file_hash)
);

CREATE TABLE IF NOT EXISTS outbox (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  idem_key        TEXT    NOT NULL,
  file_hash       TEXT    NOT NULL,
  payload         TEXT    NOT NULL,
  status          TEXT    NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  http_status     INTEGER,
  last_error      TEXT,
  sent_at         TEXT,
  UNIQUE (idem_key)
);

CREATE INDEX IF NOT EXISTS idx_outbox_pending ON outbox (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_outbox_file ON outbox (file_hash);
`

// Open membuka (dan bila perlu membuat) basis data lokal.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("buat folder db: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("buka sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: satu penulis, hindari SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("buat skema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close menutup koneksi.
func (s *Store) Close() error { return s.db.Close() }

// SaveFile mencatat berkas yang sudah diklaim.
// Mengembalikan inserted=false bila hash sudah pernah tercatat.
func (s *Store) SaveFile(name, hash string, size int64, recordCount int) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO ingest_file
		 (file_name, file_hash, file_size, record_count, received_at, status)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		name, hash, size, recordCount, nowRFC(), "parsed",
	)
	if err != nil {
		return false, fmt.Errorf("simpan ingest_file: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkFileError menandai berkas yang gagal diparse.
func (s *Store) MarkFileError(hash, reason string) error {
	_, err := s.db.Exec(
		`UPDATE ingest_file SET status = 'parse_failed', last_error = ? WHERE file_hash = ?`,
		reason, hash,
	)
	return err
}

// EnqueueInput adalah satu hasil yang akan dikirim.
type EnqueueInput struct {
	IdemKey  string
	FileHash string
	Payload  string
}

// Enqueue memasukkan hasil ke antrean. Duplikat (idem_key sama) diabaikan.
func (s *Store) Enqueue(items []EnqueueInput) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO outbox (idem_key, file_hash, payload, status) VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, it := range items {
		res, err := stmt.Exec(it.IdemKey, it.FileHash, it.Payload, StatusPending)
		if err != nil {
			return 0, fmt.Errorf("enqueue %s: %w", it.IdemKey, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, tx.Commit()
}

// NextPending mengambil antrean yang siap dikirim.
func (s *Store) NextPending(limit int, now time.Time) ([]Item, error) {
	rows, err := s.db.Query(
		`SELECT id, idem_key, file_hash, payload, status, attempts,
		        COALESCE(next_attempt_at,''), COALESCE(http_status,0),
		        COALESCE(last_error,''), COALESCE(sent_at,'')
		 FROM outbox
		 WHERE status = ?
		   AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		 ORDER BY id
		 LIMIT ?`,
		StatusPending, now.UTC().Format(time.RFC3339), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.IdemKey, &it.FileHash, &it.Payload, &it.Status,
			&it.Attempts, &it.NextAttemptAt, &it.HTTPStatus, &it.LastError, &it.SentAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkSending menandai sedang dikirim.
func (s *Store) MarkSending(id int64) error {
	_, err := s.db.Exec(
		`UPDATE outbox SET status = ?, attempts = attempts + 1 WHERE id = ?`,
		StatusSending, id,
	)
	return err
}

// MarkSent menandai berhasil terkirim.
func (s *Store) MarkSent(id int64, httpStatus int) error {
	_, err := s.db.Exec(
		`UPDATE outbox SET status = ?, http_status = ?, sent_at = ?, last_error = NULL,
		 next_attempt_at = NULL WHERE id = ?`,
		StatusSent, httpStatus, nowRFC(), id,
	)
	return err
}

// MarkRetry mengembalikan ke antrean dengan jadwal berikutnya.
func (s *Store) MarkRetry(id int64, httpStatus int, errMsg string, nextAt time.Time) error {
	_, err := s.db.Exec(
		`UPDATE outbox SET status = ?, http_status = ?, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		StatusPending, httpStatus, errMsg, nextAt.UTC().Format(time.RFC3339), id,
	)
	return err
}

// MarkPermanent menandai gagal permanen (4xx selain 409): jangan diulang.
func (s *Store) MarkPermanent(id int64, httpStatus int, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE outbox SET status = ?, http_status = ?, last_error = ?, next_attempt_at = NULL WHERE id = ?`,
		StatusPermanent, httpStatus, errMsg, id,
	)
	return err
}

// Requeue mengembalikan satu item ke antrean (dipakai tombol retry manual).
func (s *Store) Requeue(id int64) error {
	_, err := s.db.Exec(
		`UPDATE outbox SET status = ?, attempts = 0, next_attempt_at = NULL WHERE id = ?`,
		StatusPending, id,
	)
	return err
}

// RequeueFile memasukkan ulang semua item satu berkas yang gagal permanen.
func (s *Store) RequeueFile(hash string) (int64, error) {
	res, err := s.db.Exec(
		`UPDATE outbox SET status = ?, attempts = 0, next_attempt_at = NULL
		 WHERE file_hash = ? AND status = ?`,
		StatusPending, hash, StatusPermanent,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Stats merangkum keadaan antrean.
// Stats merangkum isi outbox. maxAttempts dipakai untuk menandai item yang
// sudah dicoba lebih sering dari batas wajar: item itu tetap diulang (data tidak
// boleh hilang), tetapi harus terlihat sebagai "stuck" agar operator sadar ada
// gangguan yang tidak kunjung pulih. Nilai <= 0 mematikan penandaan.
func (s *Store) Stats(maxAttempts int) (Stats, error) {
	var st Stats
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM outbox GROUP BY status`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return st, err
		}
		switch status {
		case StatusPending:
			st.Pending = n
		case StatusSending:
			st.Sending = n
		case StatusSent:
			st.Sent = n
		case StatusPermanent:
			st.Failed = n
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}

	// Item yang masih menunggu tetapi sudah dicoba terlalu sering.
	if maxAttempts > 0 {
		_ = s.db.QueryRow(
			`SELECT COUNT(*) FROM outbox
			 WHERE status IN (?, ?) AND attempts >= ?`,
			StatusPending, StatusSending, maxAttempts,
		).Scan(&st.Stuck)
	}

	var msg, at sql.NullString
	_ = s.db.QueryRow(
		`SELECT last_error, sent_at FROM outbox
		 WHERE last_error IS NOT NULL ORDER BY id DESC LIMIT 1`,
	).Scan(&msg, &at)
	if msg.Valid {
		st.LastError = msg.String
	}
	if at.Valid {
		st.LastErrorAt = at.String
	}
	return st, nil
}

// Recent mengambil item terbaru untuk ditampilkan di dashboard.
func (s *Store) Recent(limit int) ([]Item, error) {
	rows, err := s.db.Query(
		`SELECT id, idem_key, file_hash, payload, status, attempts,
		        COALESCE(next_attempt_at,''), COALESCE(http_status,0),
		        COALESCE(last_error,''), COALESCE(sent_at,'')
		 FROM outbox ORDER BY id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.IdemKey, &it.FileHash, &it.Payload, &it.Status,
			&it.Attempts, &it.NextAttemptAt, &it.HTTPStatus, &it.LastError, &it.SentAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Compact membuang riwayat terkirim yang lebih lama dari hari tertentu.
func (s *Store) Compact(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`DELETE FROM outbox WHERE status = ? AND sent_at IS NOT NULL AND sent_at < ?`,
		StatusSent, cutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }
