# Analisa 8 Repo NUT-ARNAJAKDEV + Manual Erytra Eflexis + Rekomendasi Stack
## Kebutuhan: baca file hasil mesin Erytra Eflexis di folder Windows → kirim ke service Prima (HTTP POST)

Organisasi GitHub: [NUT-ARNAJAKDEV](https://github.com/NUT-ARNAJAKDEV) · Tanggal analisa: 29 Oktober 2026

Sumber:
- 8 repo di [github.com/NUT-ARNAJAKDEV](https://github.com/NUT-ARNAJAKDEV)
- `01. ErytraEflexis Configuration Guide_0.01.pdf` — *Erytra Eflexis® Configuration Guide*, DxCTS, SEP 2020 (133 halaman)

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

> **Tiga temuan yang menentukan desain, semuanya di bagian 2.9 dan 2.10:**
> 1. File `.DAT` adalah **ASTM E1394**, bukan log bebas.
> 2. Diana Server menulis `TX_TMP.dat` lalu **rename ke `OUT.dat`** → rename itu sendiri adalah penanda "file selesai".
> 3. **`OUT.dat` wajib dihapus oleh sisi LIS**; kalau tidak, mesin memunculkan error setelah timeout. Ini membalik prioritas desain: hapus cepat, proses belakangan.
>
> **Dikonfirmasi dari lapangan:** nama file tetap **`OUT.dat`**; path folder dan nilai timeout **dinamis lewat config**; tujuan pengiriman bukan database, melainkan **service Prima lewat HTTP POST**. Nama file yang tetap itu memunculkan race condition saat menghapus — solusinya di 2.10; konsekuensi target HTTP ada di bagian 7.

---

## 1. Kebutuhan yang diterjemahkan ke istilah teknis

| Kalimat Anda | Kebutuhan teknis |
|---|---|
| "membaca sebuah file di folder tertentu di Windows" | File watcher + parser format file mesin |
| "ketika data itu ada" | Deteksi file baru **dan** deteksi file sudah selesai ditulis |
| "dikirim ke service Prima dengan method POST" | HTTP client yang idempotent + retry terklasifikasi, bukan POST buta |
| (implisit) jalan terus tanpa diawasi | Windows Service, auto-start, auto-restart, ada log |

Empat hal ini yang menentukan pilihan stack. Bukan bahasa pemrogramannya.

Satu catatan penting soal "tidak pakai DB": itu berarti **tidak ada database tujuan** — bukan berarti tanpa penyimpanan lokal sama sekali. Antrean lokal (SQLite) tetap wajib, karena tanpa itu kegagalan jaringan Prima = data hilang. Bedanya, SQLite di sini bukan tujuan data, hanya **penyangga** yang boleh dihapus kapan saja setelah semua terkirim.

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

Karena target akhirnya memang **service HTTP Prima**, repo ini naik relevansinya: kerangka `net/http` + timeout di sini bisa dipakai langsung, asalkan tiga hal ditambahkan — klasifikasi status (4xx vs 5xx), `Idempotency-Key`, dan retry berjenjang.

**Skor: 35 %** (kerangka pengirim; naik dari 25 % setelah target dipastikan HTTP POST).

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

### 2.10 Manual konfigurasi resmi: kontrak integrasi yang sebenarnya

Sumber: `01. ErytraEflexis Configuration Guide_0.01.pdf` (DxCTS, SEP 2020). Bagian LIS ada di halaman 92–127.

#### Tiga kanal fisik, dan mana yang relevan

| Kanal | Cara kerja | Untuk kasus Anda |
|---|---|---|
| Serial (COM 1/2/3) | Frame bernomor, handshake ACK/NAK, checksum | Tidak — butuh kabel & framing E1381 |
| **File sharing** | Tukar file `OUT.dat` / `IN.dat` di folder bersama | **Ya, ini yang dipakai** |
| TCP/IP | Port default 8888, protokol https ke Diana Server | Tidak — butuh listener, bukan folder |

Keduanya (serial & TCP) memerlukan framing E1381. **Mode file sharing tidak** — cukup baca/tulis file. Inilah alasan parser 150 baris tadi sudah cukup.

#### Kontrak pertukaran file (hal. 97–100)

| Arah | File | Siapa yang membuat | Siapa yang menghapus |
|---|---|---|---|
| Analyser → LIS | `TX_TMP.dat` → **rename** → `OUT.dat` | Diana Server | **LIS (tool Anda)** |
| LIS → Analyser | `IN.dat` | LIS (tool Anda) | Diana Server |

Detail yang menentukan desain:

1. **Diana Server menulis `TX_TMP.dat` dulu, lalu me-rename menjadi `OUT.dat` setelah selesai.** Rename di filesystem bersifat atomik — jadi **keberadaan `OUT.dat` sudah berarti file lengkap**. Tidak perlu menebak dari ukuran/mtime.
2. **"LIS deletes OUT.DAT when received. If not deleted within a defined timeout period, error messages are generated."** (hal. 99). Ini kewajiban, bukan pilihan: kalau tool Anda lambat menghapus, **mesin yang menampilkan error**.
3. `IN.dat` adalah permintaan worklist dari LIS ke alat. Diana Server mendeteksi, menghapus, lalu meneruskan isinya ke instrumen. Kalau tidak dihapus dalam timeout, file dihapus otomatis **dan** error dicatat (hal. 100).
4. "The exchange common folder should be unique" — satu folder bersama per instrumen, jangan dipakai dua alat.

**Konsekuensi desain yang penting:** urutan kerja tool Anda harus **salin → hapus `OUT.dat` → baru parse & kirim**. Bukan sebaliknya. IOT-Gateway melakukan kebalikannya (hapus hanya kalau HTTP 200), sehingga kalau API lambat atau mati, `OUT.dat` mengendap dan mesin memunculkan error.

#### Tipe record ASTM yang benar (hal. 119)

Manual menyebut enam tipe: `H` (header), `L` (terminator), **`Q` (query)**, `P` (patient), `O` (order), `R` (result).

IOT-Gateway hanya menangani `P`/`O`/`R`. **`Q` tidak ditangani** — padahal di mode bidirectional, `Q` adalah record pertama yang datang (`Q|1|ALL||ALL||||||||I|` untuk Query All, atau `Q|1|^=E00251711038521^||ALL|…` untuk Query by Sample). Kalau tool Anda membaca folder yang sama, record `Q` akan muncul dan harus dikenali (minimal diabaikan dengan benar, bukan bikin parser gagal).

#### Identitas pengirim menjelaskan asal file contoh

| Isi field `H` | Arti |
|---|---|
| `WADIANA^0000880^ERY` | Komunikasi **bidirectional** — alat menjawab permintaan LIS (serial asli `0000880`) |
| `REPORT^000^ERY` | **Forwarding hasil yang sudah ada** — pengiriman unidirectional, tanpa LIS |

File contoh di repo 2.8 memakai `REPORT^000^ERY`. Artinya file itu **bukan** hasil komunikasi LIS normal, melainkan ekspor/forward hasil — konsisten dengan namanya yang bertimestamp (`OUT_20240517103745.DAT`) dan bukan `OUT.dat` polos. Ini perlu dikonfirmasi ke lapangan: apakah produksi memakai `OUT.dat` (sesuai manual) atau file bertimestamp (hasil ekspor manual).

#### Template (`Plantillas`) menentukan arti kolom hasil (hal. 115–118)

Ini bagian yang paling sering diremehkan. Isi field hasil **bergantung pada template `.pln`** yang dipilih, dan nama template ikut dikirim di record `R`:

```
R|1|^^^DGW11E.gru^3 Screen(AHG).pln|3+^3+^-^Pos^Pos|I.IAC^II.IAC^III.IAC^IA C^IA||N||F||ADMIN|...
R|1|^^^DGW01J.gru^ABO2D.pln|M4+^-^4+^-^-^-^-^3+^A^Neg^A|A.GD^B.GD^AB.GD^D.RH^D'.RH^CTL.HM^A1.GI^B.GI^Dir Gr^Rh^Rev Gr||N||F||...
```

Contoh dari manual:

| Template | Komponen hasil |
|---|---|
| `3 Screen(AHG).pln` | 5 komponen: `3+^3+^-^Pos^Pos` |
| `ABO2D.pln` | 11 komponen: A.GD, B.GD, AB.GD, D.RH, D'.RH, CTL.HM, A1.GI, B.GI, Dir Gr, Rh, Rev Gr |
| `abo.gru^Erytra.pln` (file contoh) | 9 komponen: `^^^-^4+^^B^` |

Jadi **jumlah dan arti komponen tidak tetap antar tes**. Inilah bukti keras bahwa pendekatan `lines[0]…lines[26]` di `update.go` salah secara struktural — bukan cuma rapuh, tapi memang tidak bisa benar untuk lebih dari satu template.

Segment yang paling umum dipakai: Bloodgroup (Group), Rh, Group Rh, IA (antibody screening interpretation), Rh Pheno.

#### Nilai khusus di dalam field hasil

| Tanda | Arti | Sumber |
|---|---|---|
| `M4+` | Nilai **dimodifikasi manual** oleh operator | hal. 127 |
| `Dis` | Discrepancy — hasil tidak konsisten | file contoh |
| `Pos` / `Neg` | Interpretasi kualitatif | hal. 126 |
| Status `X` di record `O` | Sampel **dibatalkan** (cancelled) | hal. 125 |

Keempatnya harus disimpan apa adanya, dan `M4+`/`Dis`/`X` sebaiknya memicu peringatan — bukan diperlakukan sebagai hasil normal.

#### Pengaturan yang memengaruhi tool Anda

| Parameter | Lokasi | Efek |
|---|---|---|
| `LIS_ACTIVATED` | `erytra.properties` (hal. 50) | Harus `true`, kalau tidak tidak ada file sama sekali |
| `LIS_SHARED_MODE` | `erytra.properties` (hal. 51) | `true` → sampel di luar alat dikirim balik sebagai **cancelled** (record `O` status `X`) |
| `LIS_AUTO_QUERY_BY_SAMPLE` | `erytra.properties` (hal. 53) | Query otomatis per sampel → record `Q` lebih sering muncul |
| `RECEIVED_AND_SHOW_DEMOGRAPHIC_DATA` | `erytra.properties` (hal. 104) | Data nama/tanggal lahir/gender bisa ikut terkirim (khusus versi CE) |
| **LIS Encoding** | Diana Server (hal. 107) | **UTF-8 default; ANSI dipakai untuk Host Demo** → parser wajib mendukung keduanya |
| **Timeout** | Diana Server (hal. 106) | Batas waktu tool Anda harus menghapus `OUT.dat` |
| `SendBCReacSupp = 1` | Registry Windows (hal. 104) | Mengirim barcode kartu & reagen ke LIS — inilah asal data "card" di IOT-Gateway |

#### Alat uji yang sudah tersedia: Host Demo (hal. 129–133)

Software ini menyertakan **Host Demo** — program yang **mensimulasikan LIS**, ada di `E:\Data\SistemaDiana`, dengan jendela *Pending samples* dan *Received results*. Fungsinya persis: "isolates the problem to either Diana Server or Customer LIS".

Artinya: **tool Anda bisa diuji tanpa mesin dan tanpa LIS asli.** Konfigurasi Host Demo harus sama dengan konfigurasi Diana Server, barcode sampel harus sama dengan yang di alat, dan nama tes harus sama dengan profil `.gru` yang aktif (lihat `DSC.def` / `DSC_Custom.def`). Ini menghapus alasan "belum bisa dites karena mesinnya sedang dipakai".

#### Yang manual tidak sebutkan — dan jawabannya dari lapangan

| Pertanyaan | Jawaban | Konsekuensi desain |
|---|---|---|
| Path folder bersama | **Dinamis, dari config** | Tidak ada path yang di-hardcode; agent membaca `inbox_dir` dari config dan bisa dipindah tanpa rebuild |
| Nilai timeout | **Dinamis, dari config** | Simpan `delete_deadline` sebagai parameter; dipakai untuk alarm kesehatan, bukan untuk menunda hapus |
| `OUT.dat` atau `OUT_<timestamp>.DAT`? | **`OUT.dat`** (nama tetap) | Watcher mencocokkan nama persis; `OUT_<timestamp>.DAT` hanya artefak ekspor manual, bukan alur produksi |

Karena nama filenya **tetap** `OUT.dat`, ada dua konsekuensi teknis yang tidak muncul kalau namanya unik per transaksi:

**1. Jangan pernah memakai pola `*.dat`.** `TX_TMP.dat` ada di folder yang sama dan isinya setengah jadi. Pola `*.dat` — yang dipakai `Move-file-automation` dan `Copy-file-automation` — akan menyalin file setengah jadi. Cocokkan nama persis `OUT.dat` (di Windows bandingkan case-insensitive).

**2. Ada race condition saat menghapus.** Urutannya bisa begini: Anda menyalin `OUT.dat` → Diana selesai menulis transaksi baru dan me-rename `TX_TMP.dat` menjadi `OUT.dat` (menimpa) → Anda menghapus `OUT.dat` → **transaksi baru hilang tanpa jejak**.

Tiga cara menanganinya, berurutan dari yang paling aman:

| Cara | Mekanisme | Catatan |
|---|---|---|
| **Rename-to-claim** (disarankan) | `OUT.dat` → `OUT.<timestamp>.<rand>.dat` di folder yang sama, lalu baca file hasil rename | Satu operasi metadata yang atomik. File yang diklaim pasti file yang tadi terlihat — race hilang sepenuhnya. Sekaligus memenuhi kewajiban "hapus" dari sudut pandang Diana |
| Verifikasi sebelum hapus | Bandingkan `size`+`mtime`+hash file di path dengan yang baru disalin; hapus hanya kalau identik | Aman, tapi ada jendela waktu antara cek dan hapus |
| Copy lalu hapus langsung | Paling sederhana | Hanya boleh kalau volume transaksi sangat rendah dan tidak ada pengiriman beruntun |

Untuk file 15 KB di share LAN, rename-to-claim selesai dalam hitungan milidetik — jauh di bawah timeout apa pun.

---

## 3. Rekapitulasi

| Repo | Bahasa | Fungsi asli | Kesesuaian | Yang diambil |
|---|---|---|---|---|
| [IOT-Gateway](https://github.com/NUT-ARNAJAKDEV/IOT-Gateway) | VBS/Go/Py/Bat | Log mesin → API Odoo | **85 %** | Arsitektur, pemetaan field, kontrak payload |
| [AOI300R_VISION](https://github.com/NUT-ARNAJAKDEV/AOI300R_VISION) | Python | Mesin → MySQL | 40 % | Skema tabel, guard validasi |
| [API-ref-logfile](https://github.com/NUT-ARNAJAKDEV/API-ref-logfile) | Go | File → HTTP POST | 35 % | Kerangka HTTP client (target akhirnya memang HTTP) |
| [verify-27-fill](https://github.com/NUT-ARNAJAKDEV/verify-27-fill) | Go | Validasi jumlah baris | 20 % | Konsep gate (bukan cara hapus) |
| [Move-file-automation](https://github.com/NUT-ARNAJAKDEV/Move-file-automation) | Batch | Pindah file | 20 % | Pola klaim/arsip |
| [DE1-Stock-GO-Backend](https://github.com/NUT-ARNAJAKDEV/DE1-Stock-GO-Backend) | Go | REST + MySQL | 15 % | Referensi Gin + sqlx |
| [Copy-file-automation](https://github.com/NUT-ARNAJAKDEV/Copy-file-automation) | Batch | Salin file + file `.DAT` contoh | 15 % | **File contoh ASTM E1394** (paling berharga) + prinsip copy–verifikasi–hapus |
| [Emtry-and-countdown](https://github.com/NUT-ARNAJAKDEV/Emtry-and-countdown) | Go/Bat | Duplikat `standby.go` | 10 % | Ide penanda status mesin |

**Kesimpulan:** tidak ada satu repo pun yang bisa dipakai langsung. Yang Anda punya adalah **satu pipeline produksi yang terbukti jalan tapi rapuh** (IOT-Gateway), yang disalin-potong ke beberapa repo tanpa sumber tunggal — `standby.go` = `Emtry-and-countdown/app.go` (identik), dan `Copy-file-automation/Scripts.bat` = `Move-file-automation/Scripts.bat` (beda 7 byte).

Nilai sesungguhnya dari kedelapan repo ini ada di dua hal, bukan di kodenya:

1. **Pengetahuan domain** — pemetaan `case*.config` dan kontrak payload JSON-RPC.
2. **Satu file contoh asli** (`OUT_20240517103745.DAT`) yang membuktikan formatnya ASTM E1394 — ini yang menentukan desain parser, dan justru tidak dipakai oleh repo mana pun di atas.

Dan nilai terbesar dari seluruh bahan yang dianalisa justru ada di **manual konfigurasi**, bukan di repo: kontrak `TX_TMP.dat` → `OUT.dat`, kewajiban menghapus file, dan fakta bahwa tata letak hasil ditentukan template. Ketiganya tidak bisa disimpulkan dari kode mana pun di GitHub itu — hanya dari manual dan pengamatan lapangan.

---

## 4. Sembilan aturan yang wajib ada di tool baru

1. **Kelengkapan file dibuktikan mesin, bukan ditebak.** Mode file sharing: Diana Server menulis `TX_TMP.dat` lalu **rename** ke `OUT.dat` — rename atomik, jadi `OUT.dat` yang muncul sudah utuh. Lapis kedua: record `L|1|N`. Lapis ketiga: ukuran + mtime diam 2–3 detik. **Cocokkan nama persis `OUT.dat`; jangan pernah memantau `*.dat`** — `TX_TMP.dat` ada di folder yang sama dan isinya setengah jadi.
2. **Klaim file dengan rename, jangan copy-lalu-hapus.** Karena nama `OUT.dat` dipakai ulang, hapus tanpa klaim bisa menghapus transaksi baru yang datang di tengah proses. `OUT.dat` → `OUT.<ts>.<rand>.dat` (rename atomik) = klaim + hapus dalam satu operasi, sekaligus memenuhi kewajiban protokol.
3. **Hapus dari folder bersama secepat mungkin — itu kewajiban, bukan kerapian.** Manual (hal. 99): kalau LIS tidak menghapus dalam timeout, mesin memunculkan error. Jalur ini tidak boleh menyentuh jaringan Prima sama sekali.
4. **Idempotency per hasil, bukan per file.** Satu file ASTM berisi puluhan hasil. Kunci alaminya `(patient_id, test_code, requested_at)`. Kirim sebagai **`Idempotency-Key`** (atau field kunci di body) supaya Prima bisa menolak duplikat; di sisi kita, kunci yang sama disimpan `UNIQUE` di SQLite.
5. **Outbox lokal, dan bedakan 4xx dari 5xx.** Tulis payload ke SQLite dulu, baru kirim. **5xx / timeout / jaringan → retry dengan backoff. 4xx → jangan diulang**, pindahkan ke `failed_permanent` + notifikasi. Mengulang `400 Bad Request` selamanya adalah bug paling umum di integrator seperti ini.
6. **Arsipkan salinan Anda sendiri, bukan file mesin.** `spool/` → `archive/YYYY-MM-DD/`. Gagal parse → `error/` + alasan di log. Karena tidak ada DB tujuan, arsip inilah satu-satunya bukti mentah yang Anda punya.
7. **Validasi eksplisit, bukan positional.** Setiap field punya nama dan tipe; komponen hasil ditafsirkan **berdasarkan nama template** (`ABO2D.pln`, `3 Screen(AHG).pln`), bukan indeks tetap.
8. **Semua yang berubah di lapangan masuk config, bukan kode.** Path folder, deadline hapus, encoding (UTF-8/ANSI), URL & token Prima. Tanpa rebuild.
9. **Observable.** Log JSON terstruktur, `/healthz`, jumlah antrean, file terakhir diproses, error terakhir, dan **lama waktu dari deteksi sampai `OUT.dat` hilang** — metrik yang langsung menunjukkan apakah Anda masih di bawah timeout mesin.

---

## 5. Rekomendasi stack

### Rekomendasi utama: **Go 1.23 — satu binary, jalan sebagai Windows Service**

| Lapisan | Pilihan | Alasan |
|---|---|---|
| Watcher | `fsnotify` + polling cadangan 30 detik | Event tidak reliable di share jaringan/mapped drive — polling menyelamatkan |
| Parser | **ASTM E1394** (tulis sendiri, ~150 baris) + registry template | Format baku: split `\|` lalu `^`. Arti komponen dari nama template `.pln`, bukan indeks tetap |
| Klaim file | `os.Rename` (atomik) + fallback copy–verifikasi–hapus | Satu operasi mengklaim sekaligus mengosongkan folder bersama |
| Encoding | `golang.org/x/text/encoding` (charmap/unicode) | UTF-8 default, ANSI/Windows-1252 sebagai opsi |
| Outbox | `modernc.org/sqlite` (pure Go, tanpa CGO) | Antrean + state pengiriman; binary tetap satu file |
| **Pengiriman** | `net/http` standar + `Idempotency-Key` | Cukup; tidak perlu SDK. Timeout eksplisit, retry terklasifikasi |
| Service | `kardianos/service` | Install/uninstall service Windows, auto-restart saat crash |
| Config | YAML + override env | Ganti `*.config` satu-baris |
| Log | `log/slog` JSON + `lumberjack` | Rotasi otomatis, mudah di-parse |
| Admin/health | `gin` (sudah Anda kuasai dari DE1) | `/healthz`, `/api/ingest-log`, tombol retry manual |

**Kenapa Go, bukan yang lain, untuk kasus ini:**

- **Satu `.exe`, tanpa runtime.** Di PC lantai produksi, tidak perlu install Python/Node/.NET. Tidak ada masalah PyInstaller vs antivirus.
- **Service Windows kelas satu.** `kardianos/service` → auto-start sebelum login, restart otomatis.
- **Sudah terbukti di tangan Anda.** 4 dari 8 repo ini Go; `update.go` sudah melakukan POST JSON-RPC dengan benar — termasuk header token.
- **Parser ASTM tidak butuh ekosistem berat.** Ini file ekspor, bukan framing serial — 150 baris Go cukup, tanpa dependensi tambahan.
- **RAM ~15 MB** vs ~120 MB untuk Python+watchdog — penting kalau tool jalan 24/7 di PC yang sama dengan software mesin.

### Alternatif A: **Python 3.12 + watchdog**
`watchdog` + `pydantic` (validasi skema) + `sqlite3`/`SQLAlchemy` + `httpx` + `tenacity` (retry) + `structlog`; paket via PyInstaller, service via NSSM.
Pilih ini kalau: parsing file-nya berantakan (butuh `pandas`), atau Anda lebih cepat menulis Python. `AOI300R_VISION` membuktikan Anda nyaman di sini. Kelemahan: distribusi dan service lebih ribet, konsumsi RAM lebih besar.

### Alternatif B: **.NET 8 Worker Service**
`FileSystemWatcher` bawaan + `BackgroundService` + `HttpClientFactory` + Polly + Serilog; MSI installer.
Pilih ini kalau: butuh GUI konfigurasi (WPF) menyatu dengan service, atau lingkungannya mewajibkan stack Microsoft. Kelemahan: belum ada bukti C# di repo Anda.

### Kontrak HTTP ke Prima

Karena targetnya service, bukan database, yang menentukan kualitas integrasi bukan skema tabel melainkan **kontrak HTTP**. Minimum yang harus disepakati dengan tim Prima:

| Aspek | Yang perlu dipastikan |
|---|---|
| Endpoint | URL per hasil, atau satu URL batch untuk satu file? |
| Auth | Token di header (seperti `update.go`: header `token`) / Bearer / API key? |
| Bentuk body | Satu objek hasil, atau `{ "data": [ ... ] }` berisi 57 hasil? |
| **Idempotency** | Apakah Prima menerima `Idempotency-Key`? Kalau tidak, apakah mereka menolak duplikat berdasarkan `patient_id + test_code + requested_at`? |
| Respons sukses | `200` / `201`? Ada body balasan berisi ID? |
| Respons duplikat | `409` (anggap sukses) atau `200`? |
| Respons validasi | `400`/`422` dengan detail field? |
| Timeout server | Berapa lama Prima memproses sebelum klien menyerah? |

Kalau kontrak ini belum ada, usulkan yang paling aman: **satu POST per hasil** dengan `Idempotency-Key`, `200/201` = sukses, `409` = sudah ada (perlakukan sukses), `4xx` lain = permanen, `5xx`/timeout = retry. Mengirim 57 hasil dalam satu batch memang lebih efisien, tapi kalau satu item ditolak, seluruh batch gagal dan Anda harus memilah ulang — per hasil lebih mudah dioperasikan.

---

## 6. Arsitektur yang direkomendasikan

```
[Folder bersama]  ← path dari config (bisa share jaringan)
   Diana: tulis TX_TMP.dat → rename → OUT.dat
                                            ↓  watcher: nama persis "OUT.dat"
                          rename  OUT.dat → OUT.<ts>.<rand>.dat   ← KLAIM (atomik)
                                            │   folder bersama langsung bersih
                                            ↓
                            baca file hasil klaim → tulis ke spool lokal
                                            ↓
                            parse ASTM E1394 → N hasil (H/Q/P/O/R/L)
                             ↓ gagal            ↓ valid
                          error\ + alasan   outbox SQLite (N payload + status)
                                                ↓
                            sender: POST ke Prima, retry backoff 1s→5m
                            (4xx = berhenti, 5xx/timeout = ulangi)
                                                ↓ 2xx / 409
                                    archive\YYYY-MM-DD\
```

Dua jalur dengan prioritas berbeda, dan itu disengaja:

- **Jalur cepat (milidetik):** satu operasi `rename` mengklaim sekaligus mengosongkan folder bersama. Tidak menyentuh jaringan Prima, tidak menyalin byte lewat jaringan dua kali.
- **Jalur lambat (bisa menit):** baca → parse → validasi → POST → retry. Boleh gagal berkali-kali tanpa mengganggu mesin.

Inilah pembeda utama dengan IOT-Gateway, yang menghapus `OUT.dat` hanya setelah HTTP 200 — sehingga gangguan API langsung berubah menjadi error di layar mesin.

Kalau `rename` ditolak (ACL share hanya mengizinkan hapus, bukan rename), pakai fallback: salin → verifikasi hash → hapus. Jangan pernah `copy` lalu `move` terpisah tanpa verifikasi.

---

## 7. State lokal (SQLite) — bukan database tujuan

Tidak ada database di sisi Anda. Yang tetap ada adalah **antrean + jejak pengiriman** di SQLite lokal, supaya kegagalan Prima tidak menghilangkan data dan supaya tidak ada hasil terkirim dua kali.

```sql
-- satu baris per file yang diklaim dari folder bersama
CREATE TABLE ingest_file (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  file_name     TEXT    NOT NULL,            -- OUT.<ts>.<rand>.dat (hasil klaim)
  file_hash     TEXT    NOT NULL,            -- sha256 isi file
  file_size     INTEGER NOT NULL,
  record_count  INTEGER NOT NULL,            -- jumlah record R
  received_at   TEXT    NOT NULL,            -- RFC3339
  status        TEXT    NOT NULL,            -- parsed|parse_failed
  last_error    TEXT,
  UNIQUE (file_hash)
);

-- satu baris per hasil; inilah antrean yang dikirim ke Prima
CREATE TABLE outbox (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  idem_key       TEXT    NOT NULL,           -- patient_id|test_code|requested_at
  file_hash      TEXT    NOT NULL REFERENCES ingest_file(file_hash),
  payload        TEXT    NOT NULL,           -- JSON yang dikirim apa adanya
  status         TEXT    NOT NULL,           -- pending|sending|sent|failed_permanent
  attempts       INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,                      -- untuk backoff
  http_status    INTEGER,
  last_error     TEXT,
  sent_at        TEXT,
  UNIQUE (idem_key)
);

CREATE INDEX idx_outbox_pending ON outbox (status, next_attempt_at);
```

Peran kedua `UNIQUE` berbeda, dan keduanya perlu:

- `ingest_file.file_hash` — file yang sama tidak diparse dua kali.
- `outbox.idem_key` — hasil yang sama tidak dikirim dua kali, **meski datang dari file ekspor berbeda**. Nilai `idem_key` inilah yang juga dikirim sebagai `Idempotency-Key` ke Prima, sehingga duplikasi tertahan di kedua sisi.

Isi `payload` sengaja disimpan utuh (bukan hasil normalisasi), supaya kalau kontrak Prima berubah, Anda bisa mengirim ulang dari arsip tanpa membaca ulang file mesin.

Karena SQLite ini hanya penyangga, tabel `outbox` boleh dipangkas berkala: `DELETE FROM outbox WHERE status='sent' AND sent_at < date('now','-30 day')`. Arsip file di `archive/` tetap disimpan lebih lama — itulah bukti mentah Anda satu-satunya.

---

## 8. Struktur proyek & tahapan

```
cmd/agent/main.go          # wiring + service
internal/watcher/          # fsnotify + polling + klaim (rename) + gate kestabilan
internal/astm/             # parser E1394: record H/Q/P/O/R/L, split | dan ^
internal/parser/           # registry template (.pln) + validasi → model domain
internal/store/            # SQLite outbox
internal/sender/           # POST ke Prima: retry, backoff, klasifikasi status
internal/config/           # yaml + env
internal/api/              # /healthz, /ingest-log (gin)
configs/agent.example.yaml
testdata/OUT_*.DAT         # file contoh dari repo 2.8 + variasi lain
```

**Konfigurasi — semua yang berubah di lapangan ada di sini, tanpa rebuild:**

```yaml
machine:
  serial: "059-000-1027"          # dari machine.config lama
  name: "ANGTHONG_IoT-1"

inbox:
  dir: "\\\\server\\lis-share"    # WAJIB dari config; bisa share jaringan
  filename: "OUT.dat"             # nama tetap, cocokkan persis (case-insensitive)
  poll_fallback: 30s              # pengaman kalau fsnotify tidak bekerja di share
  claim: rename                   # rename | copy_verify_delete
  delete_deadline: 30s            # dari Diana Server; hanya untuk alarm, bukan penundaan

encoding: utf-8                   # utf-8 | windows-1252 (ANSI)

spool:
  dir: "C:\\Erytra\\spool"
  archive_dir: "C:\\Erytra\\archive"
  error_dir: "C:\\Erytra\\error"

prima:
  url: "https://prima.example/api/lab/result"   # dari tim Prima
  method: POST
  auth_header: "token"            # sesuaikan: token | Authorization
  token_env: PRIMA_TOKEN          # kredensial dari env, bukan file ini
  timeout: 10s
  idempotency_header: "Idempotency-Key"
  per_result: true                # true = satu POST per hasil; false = batch
  retry:
    max_attempts: 8
    backoff: "1s,5s,30s,2m,5m"
    retry_on_status: [429, 500, 502, 503, 504]
    treat_409_as_success: true

templates:                        # arti komponen per template .pln
  "ABO2D.pln": [A.GD, B.GD, AB.GD, D.RH, "D'.RH", CTL.HM, A1.GI, B.GI, "Dir Gr", Rh, "Rev Gr"]
  "3 Screen(AHG).pln": [I, II, III, "IA C", IA]

log:
  level: info
  format: json
  file: "C:\\Erytra\\logs\\agent.log"
```

Dua baris yang paling mudah salah: `inbox.filename` harus **`OUT.dat`** (bukan `*.dat`), dan `inbox.claim: rename` — bukan `copy` lalu `move` terpisah.| Tahap | Isi | Kriteria selesai |
|---|---|---|
| M1 | Watcher `OUT.dat` + klaim via rename + spool + arsip + log | Folder bersama bersih < 1 detik setelah file muncul; file baru yang datang saat proses tidak ikut terhapus |
| M2 | Parser ASTM + registry template + `--dry-run` | 57 hasil dari `OUT_20240517103745.DAT` ter-parse benar, payload tercetak |
| M3 | Outbox SQLite + sender + klasifikasi 4xx/5xx | Prima dimatikan 5 menit → tidak ada data hilang, tidak ada duplikat; `400` tidak diulang |
| M4 | Windows Service + config + `/healthz` | Reboot PC → service naik sendiri, data lanjut |
| M5 | Uji dengan **Host Demo** + mini dashboard + notifikasi | Alur penuh teruji tanpa mesin; operator tahu < 1 menit kalau ada file gagal atau hasil `Dis` |

M3 adalah pembeda antara "script yang jalan" dan "tool yang bisa dipercaya". Jangan lewati.

---

## 9. Yang perlu dikonfirmasi sebelum menulis kode

Sudah terjawab:

- oleh manual: **format file** (ASTM E1394), **mekanisme kelengkapan** (rename atomik), **kewajiban hapus**, **encoding** (UTF-8/ANSI), **tipe record** (H/Q/P/O/R/L), **template menentukan layout**.
- oleh lapangan: **nama file tetap `OUT.dat`**; **path folder dan timeout dinamis lewat config**; **tujuan = HTTP POST ke service Prima**, bukan database.

Yang masih terbuka:

| # | Yang ditanyakan | Kenapa penting | Cara mendapatkannya |
|---|---|---|---|
| 1 | **Kontrak API Prima** (URL, auth, body, kode respons) | Menentukan seluruh lapisan sender | Tim Prima — lihat tabel di bagian 5 |
| 2 | **Nilai timeout sebenarnya** | Bukan untuk menunda hapus, tapi untuk menyetel alarm `/healthz` | Layar konfigurasi Diana Server (hal. 106) |
| 3 | **Path folder bersama** | Mengisi `inbox.dir` di config | Konfigurasi Diana Server |
| 4 | **Template `.pln` yang aktif** | Menentukan arti tiap komponen hasil | Listas.exe → Templates (hal. 117) |
| 5 | **Volume** | File per jam & ukuran — menentukan perlu antrean atau tidak | Log `DianaASTM.log` |
| 6 | **Host Demo tersedia?** | Untuk uji tanpa mesin | Cek `E:\Data\SistemaDiana` (hal. 129) |
| 7 | **3–4 file `.DAT` lain** | Memastikan variasi (nama pasien, `X` cancelled, `M4+`, file kosong) | Ekspor dari mesin |

Catatan: kalau ternyata integrasi berjalan di **mode serial atau TCP/IP**, pekerjaannya berbeda — perlu framing E1381 (ENQ/ACK/STX/checksum) dan itu bukan lagi "baca file". Pastikan dulu modenya file sharing.

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

**Manual konfigurasi (lokal):**
`01. ErytraEflexis Configuration Guide_0.01.pdf` — halaman kunci: 50–53 (properti LIS), 97–100 (file sharing), 106–107 (timeout & encoding), 115–118 (template), 119–127 (ASTM), 129–133 (Host Demo).

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
