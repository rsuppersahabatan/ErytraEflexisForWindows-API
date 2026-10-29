// Package astm membaca berkas ASTM E1394 (LIS2-A2) yang dihasilkan Diana Server.
//
// Yang diimplementasikan hanya bagian "isi pesan" (record H/P/O/R/Q/L) karena
// berkas OUT.dat adalah hasil ekspor, bukan transmisi serial. Framing E1381
// (ENQ/ACK/STX/checksum) tidak relevan di jalur ini.
package astm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Tipe record yang dipakai Erytra Eflexis (Configuration Guide hal. 119).
const (
	RecHeader     = 'H'
	RecPatient    = 'P'
	RecOrder      = 'O'
	RecResult     = 'R'
	RecQuery      = 'Q'
	RecTerminator = 'L'
	RecComment    = 'C'
)

// ErrNotASTM dikembalikan bila isi berkas tidak diawali record header.
var ErrNotASTM = errors.New("bukan berkas ASTM: record header (H) tidak ditemukan")

// Delimiters dideklarasikan pada field pertama record H, contoh: H|\^&|...
type Delimiters struct {
	Field     byte // '|'
	Escape    byte // '\'
	Component byte // '^'
	Repeat    byte // '&'
}

// Record adalah satu baris ASTM yang sudah dipecah per field.
type Record struct {
	Type   byte
	Fields []string
}

// Field mengembalikan field ke-i (0 = tipe record) atau "" bila tidak ada.
func (r Record) Field(i int) string {
	if i < 0 || i >= len(r.Fields) {
		return ""
	}
	return r.Fields[i]
}

// Result adalah satu baris hasil (record R).
type Result struct {
	TestCode     string
	Template     string
	Value        string
	Components   []string
	AbnormalFlag string
	Status       string
	Operator     string
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// Order adalah satu permintaan pemeriksaan (record O) beserta hasil-hasilnya.
type Order struct {
	Seq         string
	PatientID   string
	SampleID    string
	TestCode    string
	Template    string
	Priority    string
	RequestedAt time.Time
	Status      string
	Results     []Result
}

// Message adalah hasil parsing satu berkas.
type Message struct {
	Delimiters  Delimiters
	Header      Record
	Sender      string // komponen ke-3 H-4, mis. "ERY"
	Serial      string // komponen ke-2 H-4, mis. "0000880"
	PatientID   string
	PatientName string
	Orders      []Order
	Queries     int
	Terminated  bool // record L ditemukan
	LineCount   int
}

// Complete menandakan pesan punya header, minimal satu hasil, dan terminator.
func (m *Message) Complete() bool {
	if !m.Terminated || len(m.Orders) == 0 {
		return false
	}
	for _, o := range m.Orders {
		if len(o.Results) > 0 {
			return true
		}
	}
	return false
}

// Parse membaca seluruh isi berkas ASTM.
func Parse(data []byte) (*Message, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, "\ufeff")

	msg := &Message{
		Delimiters: Delimiters{Field: '|', Escape: '\\', Component: '^', Repeat: '&'},
	}

	lastOrder := -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\x00")
		if strings.TrimSpace(line) == "" {
			continue
		}
		msg.LineCount++

		rec := splitRecord(line, msg.Delimiters.Field)
		if rec.Type == 0 {
			continue
		}

		switch rec.Type {
		case RecHeader:
			msg.Header = rec
			applyDelimiters(msg, rec)
			msg.Serial, msg.Sender = senderIdentity(rec, msg.Delimiters.Component)
		case RecPatient:
			msg.PatientID = strings.TrimSpace(rec.Field(2))
			msg.PatientName = componentName(rec.Field(5), msg.Delimiters.Component)
			lastOrder = -1
		case RecOrder:
			order := parseOrder(rec, msg.Delimiters.Component)
			// Pasien ditempelkan per order: satu berkas bisa memuat banyak pasien,
			// dan msg.PatientID saja akan tertimpa pasien terakhir.
			order.PatientID = msg.PatientID
			msg.Orders = append(msg.Orders, order)
			lastOrder = len(msg.Orders) - 1
		case RecResult:
			if lastOrder < 0 {
				continue
			}
			msg.Orders[lastOrder].Results = append(
				msg.Orders[lastOrder].Results,
				parseResult(rec, msg.Delimiters.Component),
			)
		case RecQuery:
			msg.Queries++
		case RecTerminator:
			msg.Terminated = true
		}
	}

	if msg.Header.Type == 0 {
		return nil, ErrNotASTM
	}
	return msg, nil
}

func splitRecord(line string, sep byte) Record {
	parts := strings.Split(line, string(sep))
	rec := Record{Fields: parts}
	if len(parts) > 0 && len(parts[0]) > 0 {
		rec.Type = parts[0][0]
	}
	return rec
}

// applyDelimiters mengambil escape/component/repeat dari field pertama record H.
func applyDelimiters(msg *Message, rec Record) {
	decl := rec.Field(1)
	if len(decl) >= 3 {
		msg.Delimiters.Escape = decl[0]
		msg.Delimiters.Component = decl[1]
		msg.Delimiters.Repeat = decl[2]
	}
}

// senderIdentity membaca field H-4 yang berbentuk "REPORT^000^ERY" atau "WADIANA^0000880^ERY".
// Serial ada di komponen kedua; identitas lengkap dipakai sebagai nama instrumen.
func senderIdentity(rec Record, comp byte) (serial, sender string) {
	field := strings.TrimSpace(rec.Field(4))
	if field == "" {
		return "", ""
	}
	parts := strings.Split(field, string(comp))
	if len(parts) > 1 {
		serial = strings.TrimSpace(parts[1])
	}
	return serial, field
}

func componentName(field string, comp byte) string {
	if field == "" {
		return ""
	}
	parts := strings.Split(field, string(comp))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "^")
}

func parseOrder(rec Record, comp byte) Order {
	code, tpl := testIdentity(rec.Field(4), comp)
	o := Order{
		Seq:      strings.TrimSpace(rec.Field(1)),
		SampleID: strings.TrimSpace(rec.Field(2)),
		TestCode: code,
		Template: tpl,
		Priority: strings.TrimSpace(rec.Field(5)),
		Status:   orderStatus(rec),
	}
	if ts, err := ParseTimestamp(rec.Field(6)); err == nil {
		o.RequestedAt = ts
	}
	return o
}

func parseResult(rec Record, comp byte) Result {
	code, tpl := testIdentity(rec.Field(2), comp)
	value := rec.Field(3)
	r := Result{
		TestCode:     code,
		Template:     tpl,
		Value:        value,
		Components:   splitComponents(value, comp),
		AbnormalFlag: strings.TrimSpace(rec.Field(6)),
		Status:       strings.TrimSpace(rec.Field(8)),
		Operator:     strings.TrimSpace(rec.Field(10)),
	}
	if ts, err := ParseTimestamp(rec.Field(11)); err == nil {
		r.StartedAt = &ts
	}
	if ts, err := ParseTimestamp(rec.Field(12)); err == nil {
		r.CompletedAt = &ts
	}
	return r
}

// testIdentity memecah field "^^^abo.gru^Erytra.pln" menjadi kode tes dan template.
func testIdentity(field string, comp byte) (code, template string) {
	if field == "" {
		return "", ""
	}
	parts := strings.Split(field, string(comp))
	if len(parts) > 3 && strings.TrimSpace(parts[3]) != "" {
		code = strings.TrimSpace(parts[3])
		if len(parts) > 4 {
			template = strings.TrimSpace(parts[4])
		}
		return code, template
	}
	// Cadangan: komponen tidak kosong terakhir.
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p, ""
		}
	}
	return "", ""
}

func splitComponents(value string, comp byte) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, string(comp))
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// orderStatus mengambil field terakhir yang tidak kosong dan memvalidasinya
// terhadap status yang dikenal (F=final, C=corrected, X=cancelled, P=preliminary, I=in progress).
func orderStatus(rec Record) string {
	known := map[string]bool{"F": true, "C": true, "X": true, "P": true, "I": true, "R": true}
	for i := len(rec.Fields) - 1; i >= 1; i-- {
		v := strings.TrimSpace(rec.Fields[i])
		if v == "" {
			continue
		}
		if known[v] {
			return v
		}
		return ""
	}
	return ""
}

var timestampLayouts = []string{
	"20060102150405",
	"200601021504",
	"2006010215",
	"20060102",
}

// ParseTimestamp membaca waktu ASTM (YYYYMMDDHHMMSS) menjadi time.Time lokal.
// Zona waktu ditentukan oleh pemanggil lewat config; di sini hanya parsing.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("timestamp kosong")
	}
	for _, layout := range timestampLayouts {
		if len(s) != len(layout) {
			continue
		}
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("format timestamp tidak dikenal: %q", s)
}
