package astm

import (
	"os"
	"path/filepath"
	"testing"
)

func loadSample(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "OUT_sample.dat"))
	if err != nil {
		t.Fatalf("baca testdata: %v", err)
	}
	return data
}

func TestParseSample(t *testing.T) {
	msg, err := Parse(loadSample(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got, want := msg.LineCount, 16; got != want {
		t.Errorf("LineCount = %d, want %d", got, want)
	}
	if !msg.Terminated {
		t.Error("Terminated = false, want true (record L ada)")
	}
	if got, want := msg.Serial, "000"; got != want {
		t.Errorf("Serial = %q, want %q", got, want)
	}
	if got, want := msg.Sender, "REPORT^000^ERY"; got != want {
		t.Errorf("Sender = %q, want %q", got, want)
	}
	if got, want := len(msg.Orders), 5; got != want {
		t.Errorf("Orders = %d, want %d", got, want)
	}
	if got, want := msg.Queries, 1; got != want {
		t.Errorf("Queries = %d, want %d", got, want)
	}
	if !msg.Complete() {
		t.Error("Complete() = false, want true")
	}
}

// Satu berkas memuat banyak pasien; pasien harus melekat pada order masing-masing.
func TestParsePatientPerOrder(t *testing.T) {
	msg, err := Parse(loadSample(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Urutan sesuai kemunculan record O di testdata. Seq tidak dipakai sebagai
	// kunci karena O|1| muncul lagi untuk pasien berikutnya.
	want := []string{"MRN0001", "MRN0001", "MRN0002", "MRN0003", "MRN0004"}
	if len(msg.Orders) != len(want) {
		t.Fatalf("Orders = %d, want %d", len(msg.Orders), len(want))
	}
	for i, o := range msg.Orders {
		if got := o.PatientID; got != want[i] {
			t.Errorf("order ke-%d: PatientID = %q, want %q", i, got, want[i])
		}
	}
}

func TestParseResultFields(t *testing.T) {
	msg, err := Parse(loadSample(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	first := msg.Orders[0].Results[0]
	if got, want := first.TestCode, "abo.gru"; got != want {
		t.Errorf("TestCode = %q, want %q", got, want)
	}
	if got, want := first.Template, "Erytra.pln"; got != want {
		t.Errorf("Template = %q, want %q", got, want)
	}
	if got, want := first.AbnormalFlag, "N"; got != want {
		t.Errorf("AbnormalFlag = %q, want %q", got, want)
	}
	if got, want := first.Status, "F"; got != want {
		t.Errorf("Status = %q, want %q", got, want)
	}
	if got, want := first.Operator, "ADMIN"; got != want {
		t.Errorf("Operator = %q, want %q", got, want)
	}
	if first.CompletedAt == nil {
		t.Fatal("CompletedAt nil, want terisi")
	}
	if got, want := len(first.Components), 8; got != want {
		t.Errorf("len(Components) = %d, want %d", got, want)
	}
	if got, want := first.Components[6], "B"; got != want {
		t.Errorf("Components[6] = %q, want %q", got, want)
	}
}

// Order yang dibatalkan (status X) tetap terbaca, walau tanpa hasil.
func TestParseCancelledOrder(t *testing.T) {
	msg, err := Parse(loadSample(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	last := msg.Orders[len(msg.Orders)-1]
	if got, want := last.Status, "X"; got != want {
		t.Errorf("status order terakhir = %q, want %q", got, want)
	}
	if len(last.Results) != 0 {
		t.Errorf("order dibatalkan punya %d hasil, want 0", len(last.Results))
	}
}

func TestParseRejectsNonASTM(t *testing.T) {
	if _, err := Parse([]byte("hello world\nbukan astm\n")); err == nil {
		t.Fatal("Parse() error = nil, want ErrNotASTM")
	}
}

func TestIncompleteWithoutTerminator(t *testing.T) {
	raw := "H|\\^&|||REPORT^000^ERY|||||||P|1|20261029120000\n" +
		"P|1|MRN0001|||Doe^John||19800101|M|\n" +
		"O|1|MRN0001||^^^abo.gru^Erytra.pln|R|20261029101500||||||||||||||||||REPORT^000^ERY|F|\n" +
		"R|1|^^^abo.gru^Erytra.pln|^^^-^4+^^B^|x||N||F||ADMIN|20261029101500|20261029103000|REPORT^000^ERY|\n"

	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Terminated {
		t.Error("Terminated = true, want false")
	}
	if msg.Complete() {
		t.Error("Complete() = true, want false untuk berkas tanpa terminator")
	}
}

func TestParseTimestamp(t *testing.T) {
	ts, err := ParseTimestamp("20261029101500")
	if err != nil {
		t.Fatalf("ParseTimestamp: %v", err)
	}
	if got, want := ts.Format("2006-01-02 15:04:05"), "2026-10-29 10:15:00"; got != want {
		t.Errorf("timestamp = %q, want %q", got, want)
	}

	if _, err := ParseTimestamp("bukan-waktu"); err == nil {
		t.Error("ParseTimestamp(bukan-waktu) error = nil, want error")
	}
}
