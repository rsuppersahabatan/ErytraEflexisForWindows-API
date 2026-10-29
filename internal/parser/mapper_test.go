package parser

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/astm"
)

func sampleMessage(t *testing.T) *astm.Message {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "OUT_sample.dat"))
	if err != nil {
		t.Fatalf("baca testdata: %v", err)
	}
	msg, err := astm.Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return msg
}

func testRegistry() *Registry {
	idx := 8
	return NewRegistry(map[string]Template{
		"ABO2D.pln": {
			Components:          []string{"A.GD", "B.GD", "AB.GD", "D.RH", "D'.RH", "CTL.HM", "A1.GI", "B.GI", "Dir Gr", "Rh", "Rev Gr"},
			InterpretationIndex: &idx,
		},
		"Erytra.pln": {Components: []string{}},
	}, "059-000-1027", time.UTC)
}

func TestMapProducesOneEnvelopePerResult(t *testing.T) {
	reg := testRegistry()
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())
	if got, want := len(envs), 4; got != want {
		t.Fatalf("envelope = %d, want %d", got, want)
	}
}

func TestMapKeepsPatientPerResult(t *testing.T) {
	reg := testRegistry()
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())

	want := []string{"MRN0001", "MRN0001", "MRN0002", "MRN0003"}
	for i, env := range envs {
		if got := env.Payload.PatientID; got != want[i] {
			t.Errorf("env[%d].PatientID = %q, want %q", i, got, want[i])
		}
	}
}

func TestTemplateGivesComponentNames(t *testing.T) {
	reg := testRegistry()
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())

	// env[1] = DGW11E.gru dengan template ABO2D.pln (11 komponen).
	second := envs[1].Payload
	if got, want := second.Template, "ABO2D.pln"; got != want {
		t.Fatalf("Template = %q, want %q", got, want)
	}
	if got, want := second.Components["A.GD"], "4+"; got != want {
		t.Errorf("A.GD = %q, want %q", got, want)
	}
	if got, want := second.Components["Dir Gr"], "A"; got != want {
		t.Errorf("Dir Gr = %q, want %q", got, want)
	}
	if got, want := second.Interpretation, "A"; got != want {
		t.Errorf("Interpretation = %q, want %q", got, want)
	}
}

// Tanpa nama template, komponen tetap lengkap dengan nama c0..cN.
func TestUnknownTemplateFallsBackToIndexedNames(t *testing.T) {
	reg := NewRegistry(nil, "M", time.UTC)
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())

	first := envs[0].Payload
	if got, want := first.Components["c6"], "B"; got != want {
		t.Errorf("c6 = %q, want %q", got, want)
	}
	if got, want := first.Interpretation, "B"; got != want {
		t.Errorf("Interpretation = %q, want %q (fallback komponen terakhir)", got, want)
	}
}

// Nilai berawalan M (mis. M4+) berarti diubah manual oleh operator.
func TestModifiedFlag(t *testing.T) {
	reg := testRegistry()
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())

	if envs[0].Payload.Modified {
		t.Error("env[0].Modified = true, want false")
	}
	if !envs[2].Payload.Modified {
		t.Error("env[2].Modified = false, want true (nilai M4+)")
	}
}

func TestDiscrepancyInterpretation(t *testing.T) {
	reg := testRegistry()
	envs := reg.Map(sampleMessage(t), "OUT_sample.dat", time.Now())

	if got, want := envs[3].Payload.Interpretation, "Dis"; got != want {
		t.Errorf("Interpretation = %q, want %q", got, want)
	}
}

// Kunci idempotency tidak boleh bergantung pada berkas sumber.
func TestIdemKeyStable(t *testing.T) {
	reg := testRegistry()
	a := reg.Map(sampleMessage(t), "OUT_1.dat", time.Now())
	b := reg.Map(sampleMessage(t), "OUT_2.dat", time.Now())

	for i := range a {
		if a[i].Result.IdemKey() != b[i].Result.IdemKey() {
			t.Errorf("idem key berbeda untuk hasil yang sama: %q vs %q",
				a[i].Result.IdemKey(), b[i].Result.IdemKey())
		}
	}
}
