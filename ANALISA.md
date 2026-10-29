# Analisa 8 Repo NUT-ARNAJAKDEV + Rekomendasi Stack
## Kebutuhan: baca file hasil mesin Erytra Eflexis di folder Windows → kirim ke database

Organisasi GitHub: [NUT-ARNAJAKDEV](https://github.com/NUT-ARNAJAKDEV) · Tanggal analisa: 29 Oktober 2026

| # | Repo | Bahasa utama |
|---|---|---|
| 1 | [IOT-Gateway](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway) | VBScript, Go, Python, Batch |
| 2 | [verify-27-fill](https://github.com/NUT-ARNAJAKDEV/verify-27-fill) | Go |
| 3 | [API-ref-logfile](https://github.com/NUT-ARNAJAKDEV/API-ref-logfile) | Go |
| 4 | [Move-file-automation](https://github.com/NUT-ARNAJAKDEV/Move-file-automation) | Batchfile |
| 5 | [AOI300R_VISION](https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION) | Python |
| 6 | [DE1-Stock-GO-Backend](https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend) | Go |
| 7 | [Emtry-and-countdown](https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown) | Go, Batchfile |
| 8 | [Copy-file-automation](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation) | Batchfile |

> **Temuan terpenting ada di bagian 2.9**: file `.DAT` dari mesin bukan log bebas, melainkan **ASTM E1394** — protokol standar alat laboratorium. Ini mengubah desain parser dan strategi idempotency.

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

### 2.1 [IOT-Gateway](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway) — **paling relevan, ini pendahulu langsung proyek Anda**

`https://github.com/NUT-ARNAJAKDEV/IOT-Gateway`

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

### 2.2 [verify-27-fill](https://github.com/NUT-ARNAJAKDEV/verify-27-fill) — potongan validasi

`https://github.com/NUT-ARNAJAKDEV/verify-27-fill`

Isi: 1 file Go 38 baris. Hitung baris `final_inspected2.log`; kalau 27 → keluar, kalau tidak → hapus file.

Yang layak diambil: **konsep gate** — jangan kirim record yang tidak lengkap.
Yang harus diubah: jangan hapus, **karantina** ke `error/` + catat alasannya. File yang salah adalah bukti untuk debugging, bukan sampah.

**Skor: 20 %** (hanya idenya).

---

### 2.3 [API-ref-logfile](https://github.com/NUT-ARNAJAKDEV/API-ref-logfile) — kerangka pengirim HTTP

`https://github.com/NUT-ARNAJAKDEV/API-ref-logfile`

Isi: 1 file Go 100 baris. Baca `ref.log` sebagai CSV → POST JSON ke `jsonplaceholder.typicode.com` → cetak status.

Yang layak diambil: pola `http.Client{Timeout: 10 * time.Second}`, `csv.Reader`, header `Content-Type`/`Accept`.
Yang kurang: tidak ada retry, tidak ada idempotency, `log.Fatalf` di semua error (proses mati, data di memori hilang), `ioutil` sudah deprecated sejak Go 1.16.

**Skor: 25 %** (kerangka, bukan solusi).

---

### 2.4 [Move-file-automation](https://github.com/NUT-ARNAJAKDEV/Move-file-automation) — pola folder → arsip

`https://github.com/NUT-ARNAJAKDEV/Move-file-automation`

Isi: `Scripts.bat` 24 baris. Loop tiap 5 detik, `move *.dat` dari `Source` ke `Destination`.

Yang layak diambil: **pola klaim file** — file yang sudah diproses harus keluar dari folder pantau.
Yang berbahaya: `move` tidak memeriksa apakah file masih ditulis mesin. File setengah jadi bisa terkirim, dan `move` gagal senyap kalau file terkunci.

**Skor: 20 %** (konsep saja; versi ini justru contoh anti-pola untuk kasus file-drop mesin).

---

### 2.5 [AOI300R_VISION](https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION) — mesin → database (tapi lewat GUI)

`https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION`

Isi: `app.py` 744 baris, Tkinter + `mysql.connector`. Memantau dua folder PASS/FALSE dengan `os.listdir` tiap 1 detik, menambah counter, `INSERT INTO result`, lalu menghapus file.

Yang layak diambil:

- Skema tabel `result` (`timestamp, yield, total, pass, false, model, pic`) dan `model` (`pass_path`, `false_path`) — rapi dan bisa dipakai ulang.
- Guard `if total == 0: return` sebelum insert — validasi bisnis yang benar.
- Ide "ambil timestamp terakhir sebagai baseline, tampilkan hanya data baru".

Yang harus diubah: penulis data tidak boleh menempel di GUI (kalau GUI ditutup, ingest berhenti), kredensial DB `root/root` hardcoded, `os.listdir` tanpa cek kestabilan file, dan file langsung dihapus setelah dihitung.

**Skor: 40 %** (logika domain bagus, arsitektur tidak cocok untuk service).

---

### 2.6 [DE1-Stock-GO-Backend](https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend) — bukan kasus serupa, tapi referensi stack

`https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend`

Isi: `main.go` (8,4 KB) Gin + `sqlx` + MySQL, `db.go`, endpoint `/api/staffs`, `/api/register`, `/api/receive`, `/api/withdraw`.

Nilainya bukan pada fungsinya, tapi pada bukti bahwa **Anda sudah punya basis Go + Gin + MySQL yang jalan**. Kalau nanti butuh endpoint admin/health untuk tool ini, pola di sini bisa langsung dipakai.

**Skor: 15 %** (referensi stack, bukan solusi).

---

### 2.7 [Emtry-and-countdown](https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown) — duplikat, dan namanya menyesatkan

`https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown`

Isi: `app.go` (5,8 KB) + `standby.bat` + 8 file `*.config`.

**`app.go` byte-identik dengan `standby.go` di IOT-Gateway** (MD5 `a85479e905f406a70dabf1c5223768ff`). Bukan mirip — sama persis. Repo ini adalah potongan IOT-Gateway yang di-commit ulang sebagai repo sendiri.

Deskripsinya menyebut *"count down to wait for sending API"*, tapi **tidak ada countdown di dalam kode**: tidak ada loop, tidak ada `time.Sleep`, tidak ada penghitung. Logikanya hanya: baca `state.config`; kalau tidak ada atau kosong → tulis `"Standby"`; kalau ada isi → `os.Exit(0)`. Countdown yang sebenarnya ada di `post.bat` IOT-Gateway (`timeout /t 45`), bukan di sini.

Yang layak diambil: ide `state.config` sebagai penanda status mesin (Standby/Running). Implementasinya sebaiknya kolom di DB, bukan file yang dibaca-tulis antarproses.

Yang harus ditolak: duplikasi kode tanpa sumber tunggal, dan `token.config` berisi token yang sama (`ee656444…`) — **kebocoran yang sama terjadi di repo kedua**.

**Skor: 10 %** (satu ide kecil, sisanya duplikat).

---

### 2.8 [Copy-file-automation](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation) — copy-paste dari Move-file-automation

`https://github.com/NUT-ARNAJAKDEV/Copy-file-automation`

Isi: `Scripts.bat` 731 byte (beda 7 byte dari versi Move: `move` → `copy`) + satu file `.DAT` contoh di `Source/` dan `Destination/`.

Path di dalamnya **masih menunjuk folder proyek lama**:

```
set "source=D:\ArnajakDEV.com\Application\[Batch] - Move file automation\Source"
```

Nama folder tidak cocok dengan repo ini (`Copy-file-automation`). Dijalankan apa adanya, ia membaca folder proyek lain.

Bug yang lebih serius: `copy` tidak menghapus sumber, dan tidak ada penanda "sudah diproses". Dengan loop 5 detik, file yang sama disalin ulang **terus-menerus tanpa henti**, menimpa tujuan setiap 5 detik. Versi `move` (2.4) setidaknya berhenti karena sumbernya hilang; versi `copy` tidak pernah selesai.

Yang layak diambil: **idenya justru lebih benar daripada `move`** — salin dulu, verifikasi, baru hapus sumber. Prinsip "jangan hapus sebelum tujuan terbukti" tepat untuk file-drop mesin. Yang salah hanya implementasinya: tanpa verifikasi, tanpa penanda, tanpa pemicu berbasis event.

**Skor: 15 %** (untuk idenya; kodenya tidak bisa dipakai).

---

### 2.9 Temuan kunci: file `.DAT` dari mesin adalah **ASTM E1394**

Ini bagian terpenting dari seluruh analisa, dan sumbernya justru repo paling sederhana (2.8), yang menyertakan file contoh asli: [`Source/OUT_20240517103745.DAT`](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation/blob/main/Source/OUT_20240517103745.DAT) — 14.937 byte.

Isinya bukan log teks bebas, melainkan **ASTM E1394** — standar pertukaran data alat laboratorium klinik (dikenal juga sebagai LIS2-A2 / LIS1-A). Struktur file (173 baris):

```
H|\^&|||REPORT^000^ERY|||||||P|1|20240516122116
P|1|41021262|||^^^^|
O|1|41021262||^^^abo.gru^Erytra.pln|R|20240514084521|...|REPORT^000^ERY|F|
R|1|^^^abo.gru^Erytra.pln|^^^-^4+^^B^|I.IAC^II.IAC^...^X.X||N||F||ADMIN|20240514084521|20240514090511|REPORT^000^ERY|
L|1|N
```

| Hal | Nilai |
|---|---|
| Baris total | 173 |
| Komposisi record | 1 `H`, 57 `P`, 57 `O`, 57 `R`, 1 `L` |
| Terminator | `L|1|N` — penanda ekspor selesai |
| Pemisah | field `|`, komponen `^` (dideklarasikan di record `H`) |
| Jumlah hasil | 57 |
| Pasien unik | 23 — satu pasien bisa 4 tes dalam satu file |
| Kode tes | `abo.gru`, `DGW11E.gru`, `DGW16A.gru`, `DGW23A.gru`, `DGW07N.gru` |
| Nilai ABO | `-^-^0`, `4+^-^A`, `-^4+^B`, `4+^4+^AB`, `-^4+^Dis` |
| Flag abnormal | semua `N` — **termasuk yang `Dis` (discrepancy)** |
| Nama file | `OUT_YYYYMMDDHHMMSS.DAT` = waktu ekspor |
| Encoding | ASCII / Windows-1252, line ending LF |

**Implikasi 1 — parser tidak perlu ditebak lagi.** Formatnya baku dan berpemisah jelas: split baris, ambil karakter pertama sebagai tipe record, split `|`, lalu split `^` untuk komponen. Untuk file ekspor seperti ini **tidak perlu** menangani framing E1381 (ENQ/ACK/STX/checksum) — itu hanya relevan untuk transport serial/TCP. Ukurannya sekitar 150 baris Go, bukan proyek besar.

**Implikasi 2 — satu file berisi 57 hasil, bukan satu.** Seluruh desain IOT-Gateway mengasumsikan "1 file = 1 record = 27 baris" — itulah asal `move.go` yang memecah baris dan `verify_rows.go` yang menuntut tepat 27 baris. Untuk file `.DAT` sebenarnya, asumsi itu salah total: memaksa 57 hasil menjadi satu record berarti **56 hasil hilang**.

**Implikasi 3 — `L|1|N` adalah penanda "file selesai" yang jauh lebih baik** daripada heuristik ukuran/mtime. Kalau record `L` sudah ada, file dipastikan lengkap. Tetap pakai cek kestabilan sebagai lapis kedua, tapi terminator ini yang utama.

**Implikasi 4 — idempotency per hasil, bukan per file.** Kunci alaminya `(patient_id, test_code, requested_at)`. Ekspor ulang rentang tanggal adalah hal lazim; `UNIQUE` di kunci inilah yang menyelamatkan data, bukan hash file.

**Implikasi 5 — `Dis` tidak ditandai abnormal oleh mesin.** Semua record berflag `N`, termasuk `-^4+^Dis` (discrepancy golongan darah). Ini temuan klinis: sistem hilir harus mendeteksi `Dis` sendiri dan menaikkannya sebagai peringatan. Kalau tidak, hasil yang tidak konsisten lolos sebagai "normal".

Ini juga menjelaskan kenapa pipeline VBS di IOT-Gateway begitu berbelit: mereka menulis ulang parser ASTM memakai regex di atas file log, lalu kehilangan struktur aslinya.

---

## 3. Rekapitulasi

| Repo | Bahasa | Fungsi asli | Kesesuaian | Yang diambil |
|---|---|---|---|---|
| [IOT-Gateway](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway) | VBS/Go/Py/Bat | Log mesin → API Odoo | **85 %** | Arsitektur, pemetaan field, kontrak payload |
| [AOI300R_VISION](https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION) | Python | Mesin → MySQL | 40 % | Skema tabel, guard validasi |
| [API-ref-logfile](https://github.com/NUT-ARNAJAKDEV/API-ref-logfile) | Go | File → HTTP POST | 25 % | Kerangka HTTP client |
| [verify-27-fill](https://github.com/NUT-ARNAJAKDEV/verify-27-fill) | Go | Validasi jumlah baris | 20 % | Konsep gate (bukan cara hapus) |
| [Move-file-automation](https://github.com/NUT-ARNAJAKDEV/Move-file-automation) | Batch | Pindah file | 20 % | Pola klaim/arsip |
| [DE1-Stock-GO-Backend](https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend) | Go | REST + MySQL | 15 % | Referensi Gin + sqlx |
| [Copy-file-automation](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation) | Batch | Salin file + file `.DAT` contoh | 15 % | **File contoh ASTM E1394** (paling berharga) + prinsip copy–verifikasi–hapus |
| [Emtry-and-countdown](https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown) | Go/Bat | Duplikat `standby.go` | 10 % | Ide penanda status mesin |

**Kesimpulan:** tidak ada satu repo pun yang bisa dipakai langsung. Yang Anda punya adalah **satu pipeline produksi yang terbukti jalan tapi rapuh** (IOT-Gateway), yang disalin-potong ke beberapa repo tanpa sumber tunggal — `standby.go` = `Emtry-and-countdown/app.go` (identik), dan `Copy-file-automation/Scripts.bat` = `Move-file-automation/Scripts.bat` (beda 7 byte).

Nilai sesungguhnya dari kedelapan repo ini ada di dua hal, bukan di kodenya:

1. **Pengetahuan domain** — pemetaan `case*.config` dan kontrak payload JSON-RPC.
2. **Satu file contoh asli** (`OUT_20240517103745.DAT`) yang membuktikan formatnya ASTM E1394 — ini yang menentukan desain parser, dan justru tidak dipakai oleh repo mana pun di atas.

---

## 4. Tujuh aturan yang wajib ada di tool baru

1. **File belum tentu selesai ditulis.** Jangan proses saat `CREATE`. Untuk file `.DAT` ASTM, **record `L|1|N` adalah bukti terkuat file sudah lengkap** — jadikan itu gate utama, lalu tambahkan cek ukuran + mtime tidak berubah selama 2–3 detik sebagai lapis kedua, dan buka dengan `FILE_SHARE_NONE`. Kalau gagal → coba lagi, jangan pindahkan.
2. **Idempotency per hasil, bukan per file.** Satu file ASTM berisi puluhan hasil. Kunci alaminya `(patient_id, test_code, requested_at)` dengan `UNIQUE`; hash file hanya untuk mencegah file yang sama diproses dua kali di tingkat berkas.
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
| Parser | **ASTM E1394** (tulis sendiri, ~150 baris) + adapter cadangan | Format `.DAT` sudah baku: split `\|` lalu `^`. Tidak perlu regex |
| Gate kestabilan | record `L\|1\|N` + cek `size`/`mtime` 2× + buka eksklusif | Solusi masalah #1 di atas, dengan bukti dari mesin sendiri |
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
- **Sudah terbukti di tangan Anda.** 4 dari 8 repo ini Go; `update.go` sudah melakukan POST JSON-RPC dengan benar.
- **Parser ASTM tidak butuh ekosistem berat.** Ini file ekspor, bukan framing serial — 150 baris Go cukup, tanpa dependensi tambahan.
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
[Erytra Eflexis] → tulis OUT_*.DAT → C:\Erytra\inbox\
                                      ↓  (watcher: menunggu record L|1|N)
                              C:\Erytra\processing\   ← klaim atomik (rename)
                                      ↓
                    parser ASTM E1394 → N hasil (H/P/O/R/L)
                             ↓ gagal            ↓ valid
                     C:\Erytra\error\      outbox SQLite (N payload + status)
                     (+ alasan di log)          ↓
                                        sender: retry backoff 1s→5m
                                                ↓
                            DB: UPSERT per hasil
                            (kunci: patient_id + test_code + requested_at)
                                                ↓ sukses
                                    C:\Erytra\archive\YYYY-MM-DD\
```

Satu binary, dua goroutine: **watcher** dan **sender**. Keduanya tidak saling menunggu — DB lambat tidak menghentikan pembacaan file. Satu file masuk → satu transaksi berisi N baris hasil; kalau satu baris gagal validasi, seluruh file masuk `error/` agar tidak ada hasil yang setengah masuk.

---

## 7. Skema database minimum

```sql
-- level berkas: mencegah file yang sama diproses dua kali
CREATE TABLE ingest_log (
  id            BIGSERIAL PRIMARY KEY,
  file_name     TEXT        NOT NULL,
  file_hash     CHAR(64)    NOT NULL,        -- sha256 isi file
  file_size     BIGINT      NOT NULL,
  record_count  INT         NOT NULL,        -- jumlah record R di dalamnya
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  status        TEXT        NOT NULL,        -- pending|sent|failed
  attempts      INT         NOT NULL DEFAULT 0,
  last_error    TEXT,
  UNIQUE (file_hash)
);

-- level hasil: bentuknya mengikuti ASTM E1394 (satu file = banyak baris)
CREATE TABLE astm_result (
  id             BIGSERIAL PRIMARY KEY,
  patient_id     TEXT        NOT NULL,       -- P-3, MRN pasien
  test_code      TEXT        NOT NULL,       -- abo.gru, DGW11E.gru, …
  requested_at   TIMESTAMPTZ NOT NULL,       -- O-6, waktu permintaan
  completed_at   TIMESTAMPTZ,                -- R-13, waktu selesai
  value_raw      TEXT        NOT NULL,       -- isi field R-4 apa adanya
  components     JSONB       NOT NULL,       -- hasil split '^'
  interpretation TEXT,                       -- 0 / A / B / AB / Dis
  abnormal_flag  TEXT,                       -- R-7 (semua 'N' di file contoh)
  result_status  TEXT,                       -- R-9, 'F' = final
  operator       TEXT,                       -- R-11
  instrument     TEXT,                       -- Erytra.pln
  file_hash      CHAR(64) REFERENCES ingest_log(file_hash),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (patient_id, test_code, requested_at)
);
```

Kunci desainnya ada di dua `UNIQUE` dengan peran berbeda:

- `ingest_log.file_hash` — file yang sama tidak diproses dua kali.
- `astm_result (patient_id, test_code, requested_at)` — hasil yang sama tidak masuk dua kali, **meski datang dari file ekspor yang berbeda**. Ini yang menyelamatkan Anda saat mesin mengekspor ulang rentang tanggal yang sama.

---

## 8. Struktur proyek & tahapan

```
cmd/agent/main.go          # wiring + service
internal/watcher/          # fsnotify + polling + gate kestabilan
internal/astm/             # parser E1394: record H/P/O/R/L, split | dan ^
internal/parser/           # validasi + normalisasi → model domain
internal/store/            # SQLite outbox
internal/sender/           # retry + backoff + upsert
internal/config/           # yaml + env
internal/api/              # /healthz, /ingest-log (gin)
configs/agent.example.yaml
testdata/OUT_*.DAT         # file contoh dari repo 2.8 + variasi lain
```

| Tahap | Isi | Kriteria selesai |
|---|---|---|
| M1 | Watcher + gate `L|1|N` + arsip + log | File setengah jadi tidak pernah terkirim |
| M2 | Parser ASTM + validator + `--dry-run` | 57 hasil dari `OUT_20240517103745.DAT` ter-parse benar, payload tercetak |
| M3 | Outbox SQLite + sender + UPSERT | Matikan DB 5 menit → tidak ada data hilang, tidak ada duplikat |
| M4 | Windows Service + config + `/healthz` | Reboot PC → service naik sendiri, data lanjut |
| M5 | Mini dashboard + notifikasi (Telegram/WA) | Operator tahu dalam < 1 menit kalau ada file gagal, atau ada hasil `Dis` |

M3 adalah pembeda antara "script yang jalan" dan "tool yang bisa dipercaya". Jangan lewati.

---

## 9. Yang perlu dikonfirmasi sebelum menulis kode

1. ~~**Format file dari mesin**~~ — **sudah terjawab**: ASTM E1394 (lihat 2.9). Yang masih perlu: 3–4 file `.DAT` lain untuk memastikan variasinya konsisten (mis. hasil dengan nama pasien, tes selain golongan darah, file kosong, file terpotong).
2. **Nama & lokasi folder** — lokal (`C:\...`) atau share jaringan (`\\server\...`)? Share jaringan mengubah strategi watcher.
3. **Database tujuan** — MySQL / Postgres / Odoo / lain? Dan apakah "update data" berarti UPSERT atau UPDATE baris yang sudah ada?
4. **Volume** — berapa file per jam, dan ukuran rata-rata? Menentukan perlu antrean atau cukup satu goroutine.
5. **PC-nya** — dedicated atau dipakai bersama software mesin? Menentukan budget RAM dan cara instalasi.
6. **Apakah mesin juga mengirim via serial/TCP (LIS1-A)?** Kalau ya, framing E1381 (ENQ/ACK/STX/checksum) perlu ditangani terpisah — dan itu pekerjaan yang berbeda dari membaca file.

---

## 10. Tindakan mendesak (terlepas dari proyek ini)

- **Rotasi token** `ee656444…` — bocor di **dua** repo publik sekaligus: [`IOT-Gateway/token.config`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/token.config) dan [`Emtry-and-countdown/token.config`](https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown/blob/main/token.config). Anggap sudah kompromi.
- **Satukan repo.** `Emtry-and-countdown/app.go` identik dengan `IOT-Gateway/standby.go`, dan `Copy-file-automation/Scripts.bat` adalah salinan `Move-file-automation`. Tanpa sumber tunggal, perbaikan bug akan terus terlewat di salah satu salinan.
- Hapus [`iot.exe`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/iot.exe), [`auto_timestamp.exe`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/auto_timestamp.exe), dan [`flowchart.jpg`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/flowchart.jpg) dari riwayat repo; pindahkan build ke rilis.
- Tambahkan `.gitignore` untuk `*.config` yang berisi kredensial.

---

## 11. Referensi cepat

| Repo | URL |
|---|---|
| IOT-Gateway | https://github.com/NUT-ARNAJAKDEV/IOT-Gateway |
| verify-27-fill | https://github.com/NUT-ARNAJAKDEV/verify-27-fill |
| API-ref-logfile | https://github.com/NUT-ARNAJAKDEV/API-ref-logfile |
| Move-file-automation | https://github.com/NUT-ARNAJAKDEV/Move-file-automation |
| AOI300R_VISION | https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION |
| DE1-Stock-GO-Backend | https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend |
| Emtry-and-countdown | https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown |
| Copy-file-automation | https://github.com/NUT-ARNAJAKDEV/Copy-file-automation |

**File contoh ASTM E1394 (paling penting):**
[`OUT_20240517103745.DAT`](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation/blob/main/Source/OUT_20240517103745.DAT) — 173 baris, 57 hasil, 23 pasien, 5 kode tes.

File kunci di IOT-Gateway (untuk dibaca ulang saat implementasi):

| File | Isi |
|---|---|
| [`app.py`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/app.py) | Tail log + filter keyword |
| [`update.go`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/update.go) | Pemetaan 27 baris → MCL, POST JSON-RPC |
| [`verify_rows.go`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/verify_rows.go) | Gate jumlah baris (contoh anti-pola) |
| [`move.go`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/move.go) | Pecah baris CSV → 27 baris |
| [`post.bat`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/post.bat) | Orkestrasi loop (contoh anti-pola) |
| [`final_inspected.vbs`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/final_inspected.vbs) | Perakitan 9 kolom + pemetaan `case*.config` |
| [`Readme!.csv`](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway/blob/main/Readme%21.csv) | Matriks 19 kolom per jenis pemeriksaan |
