# Analisa 6 Repo NUT-ARNAJAKDEV + Rekomendasi Stack
## Kebutuhan: baca file hasil mesin Erytra Eflexis di folder Windows → kirim ke database

---

## 1. Kebutuhan yang diterjemahkan ke istilah teknis

| Kalimat Anda | Kebutuhan teknis |
|---|---|
| "membaca sebuah file di folder tertentu di Windows" | File watcher + parser format file mesin |
| "ketika data itu ada" | Deteksi file baru **dan** deteksi file sudah selesai ditulis |
| "dikirim ke database untuk melakukan update data" | Delivery yang idempotent + retry, bukan INSERT buta |
| (implisit) jalan terus tanpa diawasi | Windows Service, auto-start, auto-restart, ada log |

Empat hal ini yang menentukan pilihan stack. Bukan bahasa pemrogramannya.

---

## 2. Analisa per repo

### 2.1 IOT-Gateway — **paling relevan, ini pendahulu langsung proyek Anda**

Repo ini persis kasusnya: mesin Erytra/Eflexis menulis log, lalu data dikirim ke database (Odoo lewat API `gfe.winnergy.co.th/api/iot/data`). Komposisi: 32 KB VBScript, 16 KB Go, 11 KB Python, 5 KB batch.

**Pipeline nyata yang berjalan di lapangan:**

```
C:\Log\erytra.log  (ditulis mesin)
   └─ app.py            tail tiap 1 detik, filter keyword
        ├─ run1.log          (END CardReading, Visio Result…)
        ├─ err1.log          (Full Error)
        └─ final_inspected.log  (END Close Scheduler / START CardFloorInitialization)
   └─ filter.bat        jalankan 9 skrip VBS berurutan, timeout tetap 2 detik/10 detik
        ├─ selected.vbs  → technique.log
        ├─ _date.vbs     → date.log
        ├─ _time.vbs     → time.log
        ├─ card.vbs      → card.log        (ambil 20 karakter terakhir)
        ├─ sample.vbs    → sample.log
        ├─ result.vbs    → result.log      (regex "Visio Result" s/d "ReadingIncidences")
        ├─ final_inspected.vbs → gabung 9 kolom CSV ke final_inspected.log
        └─ clear.vbs     → kosongkan file perantara
   └─ post.bat          loop: move.go → verify_rows.go → update.go → standby_write.go
        ├─ move.go          ambil baris teratas, pecah per koma → final_inspected2.log (27 baris)
        ├─ verify_rows.go   kalau jumlah baris ≠ 27 → hapus file
        ├─ update.go        map lines[0..26] → MCL0210…MCL0315, POST JSON-RPC
        └─ standby_write.go tulis state.config = "Standby"
```

**Yang bagus dan layak diambil:**

- `path.config` / `machine.config` / `url.config` / `token.config` — konfigurasi dipisah dari kode. Pola ini benar, hanya perlu naik kelas (env var + secret store).
- `case0..case13.config` + `Readme!.csv` — pemetaan *nama pemeriksaan* → 19 kolom hasil. Ini **domain knowledge berharga**, jangan dibuang; ini kandidat tabel master di DB.
- Bentuk payload JSON-RPC (`params.serial_no`, `location`, `province`, `iot_name`, `data[]`) — kalau nanti integrasi ke Odoo/Winnergy, kontrak ini sudah ada.
- Ide `verify_rows` (validasi kelengkapan sebelum kirim) — idenya benar, implementasinya yang salah.

**Yang harus ditolak (dan kenapa):**

| Temuan | Risiko |
|---|---|
| `app.py`: `processed_lines` hanya di memori | Restart aplikasi → baris yang sama dikirim ulang (duplikat) |
| `app.py`: `last_timestamp` dari `timestamp.config` direset tiap 00:00 oleh `auto_timestamp.py` | Duplikat harian, dan berpotensi melewatkan data lintas tengah malam |
| `verify_rows.go`: `os.Remove()` kalau baris ≠ 27 | **Kehilangan data permanen tanpa jejak.** Kalau mesin menulis 26 baris karena satu field kosong, record itu hilang |
| `update.go`: `lines[0]…lines[26]` positional | Satu baris kosong menggeser semua field. Tidak ada validasi tipe |
| `update.go`: `parsedTime.Add(-7 * time.Hour)` hardcoded | Timezone di-hardcode. Ganti WIB/WITA atau DST → tanggal salah |
| `update.go`: hapus file hanya kalau HTTP 200 | 200 dari gateway ≠ data tersimpan di DB. Kalau gagal di hilir, data hilang |
| `post.bat`: `go run` tiap iterasi + `timeout 4`/`2`/`45` tetap | Kompilasi ulang tiap siklus; kalau langkah lebih lambat dari timeout, rantai berantakan |
| `storage_check.py`: salin + truncate saat file ≥ 5 KB tiap 600 detik | Balapan baca/tulis dengan `app.py` — ini gejala, bukan solusi |
| `app.py` baca seluruh file tiap 1 detik (tanpa offset byte) | O(n) terus membesar; CPU naik seiring log tumbuh |
| `.exe` 7,3 MB × 2 dan `flowchart.jpg` 7,4 MB di-commit | Repo tidak bisa di-review, build tidak reproducible |
| **`token.config` berisi token asli di repo publik** (`ee656444…`) | Kredensial bocor. **Rotasi sekarang**, jangan tunggu |
| Encoding `Windows-1252` di-hardcode di config | File lain (UTF-16/ANSI) akan rusak senyap |

**Skor kesesuaian: 85 % konsep, ~30 % kualitas kode.** Ambil arsitekturnya, tulis ulang implementasinya.

---

### 2.2 verify-27-fill — potongan validasi

Isi: 1 file Go 38 baris. Hitung baris `final_inspected2.log`; kalau 27 → keluar, kalau tidak → hapus file.

Yang layak diambil: **konsep gate** — jangan kirim record yang tidak lengkap.
Yang harus diubah: jangan hapus, **karantina** ke `error/` + catat alasannya. File yang salah adalah bukti untuk debugging, bukan sampah.

**Skor: 20 %** (hanya idenya).

---

### 2.3 API-ref-logfile — kerangka pengirim HTTP

Isi: 1 file Go 100 baris. Baca `ref.log` sebagai CSV → POST JSON ke `jsonplaceholder.typicode.com` → cetak status.

Yang layak diambil: pola `http.Client{Timeout: 10 * time.Second}`, `csv.Reader`, header `Content-Type`/`Accept`.
Yang kurang: tidak ada retry, tidak ada idempotency, `log.Fatalf` di semua error (proses mati, data di memori hilang), `ioutil` sudah deprecated sejak Go 1.16.

**Skor: 25 %** (kerangka, bukan solusi).

---

### 2.4 Move-file-automation — pola folder → arsip

Isi: `Scripts.bat` 24 baris. Loop tiap 5 detik, `move *.dat` dari `Source` ke `Destination`.

Yang layak diambil: **pola klaim file** — file yang sudah diproses harus keluar dari folder pantau.
Yang berbahaya: `move` tidak memeriksa apakah file masih ditulis mesin. File setengah jadi bisa terkirim, dan `move` gagal senyap kalau file terkunci.

**Skor: 20 %** (konsep saja; versi ini justru contoh anti-pola untuk kasus file-drop mesin).

---

### 2.5 AOI300R_VISION — mesin → database (tapi lewat GUI)

Isi: `app.py` 744 baris, Tkinter + `mysql.connector`. Memantau dua folder PASS/FALSE dengan `os.listdir` tiap 1 detik, menambah counter, `INSERT INTO result`, lalu menghapus file.

Yang layak diambil:

- Skema tabel `result` (`timestamp, yield, total, pass, false, model, pic`) dan `model` (`pass_path`, `false_path`) — rapi dan bisa dipakai ulang.
- Guard `if total == 0: return` sebelum insert — validasi bisnis yang benar.
- Ide "ambil timestamp terakhir sebagai baseline, tampilkan hanya data baru".

Yang harus diubah: penulis data tidak boleh menempel di GUI (kalau GUI ditutup, ingest berhenti), kredensial DB `root/root` hardcoded, `os.listdir` tanpa cek kestabilan file, dan file langsung dihapus setelah dihitung.

**Skor: 40 %** (logika domain bagus, arsitektur tidak cocok untuk service).

---

### 2.6 DE1-Stock-GO-Backend — bukan kasus serupa, tapi referensi stack

Isi: `main.go` (8,4 KB) Gin + `sqlx` + MySQL, `db.go`, endpoint `/api/staffs`, `/api/register`, `/api/receive`, `/api/withdraw`.

Nilainya bukan pada fungsinya, tapi pada bukti bahwa **Anda sudah punya basis Go + Gin + MySQL yang jalan**. Kalau nanti butuh endpoint admin/health untuk tool ini, pola di sini bisa langsung dipakai.

**Skor: 15 %** (referensi stack, bukan solusi).

---

## 3. Rekapitulasi

| Repo | Bahasa | Fungsi asli | Kesesuaian | Yang diambil |
|---|---|---|---|---|
| IOT-Gateway | VBS/Go/Py/Bat | Log mesin → API Odoo | **85 %** | Arsitektur, pemetaan field, kontrak payload |
| AOI300R_VISION | Python | Mesin → MySQL | 40 % | Skema tabel, guard validasi |
| API-ref-logfile | Go | File → HTTP POST | 25 % | Kerangka HTTP client |
| verify-27-fill | Go | Validasi jumlah baris | 20 % | Konsep gate (bukan cara hapus) |
| Move-file-automation | Batch | Pindah file | 20 % | Pola klaim/arsip |
| DE1-Stock-GO-Backend | Go | REST + MySQL | 15 % | Referensi Gin + sqlx |

**Kesimpulan:** tidak ada satu repo pun yang bisa dipakai langsung. Yang Anda punya adalah **satu pipeline produksi yang terbukti jalan tapi rapuh** (IOT-Gateway). Nilainya ada di pengetahuan domain dan kontrak datanya, bukan di kodenya.

---

## 4. Tujuh aturan yang wajib ada di tool baru

1. **File belum tentu selesai ditulis.** Mesin menulis file bertahap. Jangan proses saat `CREATE`. Tunggu ukuran + mtime tidak berubah selama 2–3 detik, lalu buka dengan `FILE_SHARE_NONE` / `FileShare.None`. Kalau gagal → coba lagi, jangan pindahkan.
2. **Idempotency.** Hash SHA-256 isi file (atau `nama + ukuran + mtime`) disimpan di tabel `ingest_log` dengan `UNIQUE`. File yang sama tidak boleh menghasilkan dua record.
3. **Outbox lokal.** Tulis payload ke SQLite dulu, baru kirim. DB/API mati → data tetap aman dan terkirim saat pulih. Ini pengganti `standby.log` yang rapuh.
4. **Jangan hapus, arsipkan.** `inbox/` → `processing/` → `archive/YYYY-MM-DD/`. Gagal parse → `error/` + alasan di log.
5. **Validasi eksplisit, bukan positional.** Setiap field punya nama, tipe, dan aturan wajib. Record tidak valid ditolak **dengan alasan**, tidak dihapus.
6. **Jangan hardcode waktu & path.** Timezone dari config (`Asia/Jakarta`), path dari config, kredensial dari env/secret.
7. **Observable.** Log JSON terstruktur, `/healthz`, jumlah antrean, file terakhir diproses, error terakhir. Tanpa ini, kegagalan di lantai produksi tidak akan ketahuan sampai ada yang komplain.

---

## 5. Rekomendasi stack

### Rekomendasi utama: **Go 1.23 — satu binary, jalan sebagai Windows Service**

| Lapisan | Pilihan | Alasan |
|---|---|---|
| Watcher | `fsnotify` + polling cadangan 30 detik | Event tidak reliable di share jaringan/mapped drive — polling menyelamatkan |
| Gate kestabilan | cek `size`+`mtime` 2× berturut, lalu buka eksklusif | Solusi masalah #1 di atas |
| Parser | adapter per format (`csv`, fixed-width, key-value) | Format mesin bisa berubah; jangan satu parser monolitik |
| Encoding | `golang.org/x/text/encoding` (charmap/unicode) | Ganti `Windows-1252` hardcoded |
| Outbox | `modernc.org/sqlite` (pure Go, tanpa CGO) | Tanpa compiler C, binary tetap satu file |
| Database | `jackc/pgx` (Postgres) atau `go-sql-driver/mysql` (MySQL) | UPSERT: `ON CONFLICT DO UPDATE` / `ON DUPLICATE KEY UPDATE` |
| Service | `kardianos/service` | Install/uninstall service Windows, auto-restart saat crash |
| Config | YAML + override env | Ganti `*.config` satu-baris |
| Log | `log/slog` JSON + `lumberjack` | Rotasi otomatis, mudah di-parse |
| Admin/health | `gin` (sudah Anda kuasai dari DE1) | `/healthz`, `/api/ingest-log`, retry manual |

**Kenapa Go, bukan yang lain, untuk kasus ini:**

- **Satu `.exe`, tanpa runtime.** Di PC lantai produksi, tidak perlu install Python/Node/.NET. Tidak ada masalah PyInstaller vs antivirus.
- **Service Windows kelas satu.** `kardianos/service` → auto-start sebelum login, restart otomatis.
- **Sudah terbukti di tangan Anda.** 3 dari 6 repo ini Go; `update.go` sudah melakukan POST JSON-RPC dengan benar.
- **RAM ~15 MB** vs ~120 MB untuk Python+watchdog — penting kalau tool jalan 24/7 di PC yang sama dengan software mesin.

### Alternatif A: **Python 3.12 + watchdog**
`watchdog` + `pydantic` (validasi skema) + `SQLAlchemy` + `tenacity` (retry) + `structlog`; paket via PyInstaller, service via NSSM.
Pilih ini kalau: parsing file-nya berantakan (butuh `pandas`), atau Anda lebih cepat menulis Python. `AOI300R_VISION` membuktikan Anda nyaman di sini. Kelemahan: distribusi dan service lebih ribet, konsumsi RAM lebih besar.

### Alternatif B: **.NET 8 Worker Service**
`FileSystemWatcher` bawaan + `BackgroundService` + Dapper + Polly + Serilog; MSI installer.
Pilih ini kalau: butuh GUI konfigurasi (WPF) menyatu dengan service, atau lingkungannya mewajibkan stack Microsoft. Kelemahan: belum ada bukti C# di repo Anda.

### Database
- **Postgres** kalau boleh pilih bebas: `jsonb` untuk payload mentah, `ON CONFLICT` bersih, partisi tabel per bulan.
- **MySQL** kalau mengikuti infra yang ada (AOI memakai MySQL): aman, `INSERT … ON DUPLICATE KEY UPDATE` cukup.
- **Odoo** kalau tujuannya memang Odoo (seperti IOT-Gateway): jangan INSERT langsung ke tabel Odoo — pakai XML-RPC/JSON-RPC dengan **external ID** sebagai kunci idempotency.

---

## 6. Arsitektur yang direkomendasikan

```
[Erytra Eflexis] → tulis file → C:\Erytra\outbox\
                                      ↓  (watcher + gate kestabilan)
                              C:\Erytra\processing\   ← klaim atomik (rename)
                                      ↓
                          parser + validator (skema per format)
                             ↓ gagal            ↓ valid
                     C:\Erytra\error\      outbox SQLite (payload + status)
                     (+ alasan di log)          ↓
                                        sender: retry backoff 1s→5m
                                                ↓
                                    DB: UPSERT (kunci: file_hash)
                                                ↓ sukses
                                    C:\Erytra\archive\YYYY-MM-DD\
```

Satu binary, dua goroutine: **watcher** dan **sender**. Keduanya tidak saling menunggu — DB lambat tidak menghentikan pembacaan file.

---

## 7. Skema database minimum

```sql
-- penjaga idempotency: satu file = satu baris
CREATE TABLE ingest_log (
  id            BIGSERIAL PRIMARY KEY,
  file_name     TEXT        NOT NULL,
  file_hash     CHAR(64)    NOT NULL,        -- sha256 isi file
  file_size     BIGINT      NOT NULL,
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  status        TEXT        NOT NULL,        -- pending|sent|failed
  attempts      INT         NOT NULL DEFAULT 0,
  last_error    TEXT,
  payload       JSONB       NOT NULL,
  UNIQUE (file_hash)
);

-- data hasil mesin (sesuaikan dengan format aktual)
CREATE TABLE inspection_result (
  id            BIGSERIAL PRIMARY KEY,
  machine_serial TEXT       NOT NULL,
  measured_at   TIMESTAMPTZ NOT NULL,        -- waktu mesin, sudah dinormalisasi
  sample_no     TEXT,
  technique     TEXT,
  test_result   TEXT,
  card_no       TEXT,
  raw           JSONB,                        -- 19 kolom MCL disimpan utuh
  source_hash   CHAR(64) REFERENCES ingest_log(file_hash),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source_hash)
);
```

Kunci desainnya: `UNIQUE (file_hash)` di dua tempat. Kirim ulang seribu kali, hasilnya tetap satu baris.

---

## 8. Struktur proyek & tahapan

```
cmd/agent/main.go          # wiring + service
internal/watcher/          # fsnotify + polling + gate kestabilan
internal/parser/           # adapter per format + validasi
internal/store/            # SQLite outbox
internal/sender/           # retry + backoff + upsert
internal/config/           # yaml + env
internal/api/              # /healthz, /ingest-log (gin)
configs/agent.example.yaml
```

| Tahap | Isi | Kriteria selesai |
|---|---|---|
| M1 | Watcher + gate kestabilan + arsip + log | File setengah jadi tidak pernah terkirim |
| M2 | Parser + validator + mode `--dry-run` | File asli dari mesin ter-parse benar, payload tercetak |
| M3 | Outbox SQLite + sender + UPSERT | Matikan DB 5 menit → tidak ada data hilang, tidak ada duplikat |
| M4 | Windows Service + config + `/healthz` | Reboot PC → service naik sendiri, data lanjut |
| M5 | Mini dashboard + notifikasi (Telegram/WA) | Operator tahu dalam < 1 menit kalau ada file gagal |

M3 adalah pembeda antara "script yang jalan" dan "tool yang bisa dipercaya". Jangan lewati.

---

## 9. Yang perlu dikonfirmasi sebelum menulis kode

1. **Format file dari mesin** — 1 sampel file asli (atau 3–4 variasi). Ini penentu 80 % pekerjaan. Tanpa ini, parser hanya bisa ditebak.
2. **Nama & lokasi folder** — lokal (`C:\...`) atau share jaringan (`\\server\...`)? Share jaringan mengubah strategi watcher.
3. **Database tujuan** — MySQL / Postgres / Odoo / lain? Dan apakah "update data" berarti UPSERT atau UPDATE baris yang sudah ada?
4. **Volume** — berapa file per jam, dan ukuran rata-rata? Menentukan perlu antrean atau cukup satu goroutine.
5. **PC-nya** — dedicated atau dipakai bersama software mesin? Menentukan budget RAM dan cara instalasi.

---

## 10. Tindakan mendesak (terlepas dari proyek ini)

- **Rotasi token** di `IOT-Gateway/token.config` — sudah publik dan bisa dipakai siapa pun. Anggap sudah kompromi.
- Hapus `iot.exe`, `auto_timestamp.exe`, dan `flowchart.jpg` dari riwayat repo; pindahkan build ke rilis.
- Tambahkan `.gitignore` untuk `*.config` yang berisi kredensial.
