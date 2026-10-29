// Package parser mengubah pesan ASTM menjadi payload siap kirim.
//
// Bagian terpenting: arti tiap komponen hasil bergantung pada template .pln
// yang dipakai mesin (Configuration Guide hal. 115-118). Karena itu nama
// komponen diambil dari registry template, bukan dari indeks tetap.
package parser

import (
	"strconv"
	"strings"
	"time"

	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/astm"
	"github.com/rsuppersahabatan/ErytraEflexisForWindows-API/internal/model"
)

// Template mendeskripsikan susunan komponen satu template .pln.
type Template struct {
	// Components adalah nama komponen sesuai urutan pada field hasil (R-4).
	Components []string `yaml:"components"`
	// InterpretationIndex menunjuk indeks komponen yang berisi interpretasi
	// (mis. golongan darah "B"). Bila nil, dipakai komponen tidak kosong terakhir.
	InterpretationIndex *int `yaml:"interpretation_index"`
}

// Registry menyimpan template yang dikenal beserta identitas mesin.
type Registry struct {
	templates map[string]Template
	machine   string
	loc       *time.Location
}

// NewRegistry membuat registry dari konfigurasi.
func NewRegistry(templates map[string]Template, machine string, loc *time.Location) *Registry {
	if loc == nil {
		loc = time.Local
	}
	normalized := make(map[string]Template, len(templates))
	for k, v := range templates {
		normalized[normalizeName(k)] = v
	}
	return &Registry{templates: normalized, machine: machine, loc: loc}
}

func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Map mengubah seluruh hasil dalam satu pesan menjadi envelope siap kirim.
func (r *Registry) Map(msg *astm.Message, sourceFile string, receivedAt time.Time) []model.Envelope {
	var out []model.Envelope

	patientID := strings.TrimSpace(msg.PatientID)
	instrument := strings.TrimSpace(msg.Sender)
	if instrument == "" {
		instrument = msg.Serial
	}

	for _, order := range msg.Orders {
		if len(order.Results) == 0 {
			continue
		}
		// Pasien melekat pada order, bukan pada pesan: satu berkas bisa memuat
		// puluhan pasien berbeda.
		pid := strings.TrimSpace(order.PatientID)
		if pid == "" {
			pid = patientID
		}
		if pid == "" {
			pid = order.SampleID
		}
		requestedAt := order.RequestedAt
		if requestedAt.IsZero() {
			requestedAt = receivedAt
		}

		for _, res := range order.Results {
			code := res.TestCode
			if code == "" {
				code = order.TestCode
			}
			tplName := res.Template
			if tplName == "" {
				tplName = order.Template
			}

			tpl, known := r.lookup(tplName)
			result := model.Result{
				Machine:        r.machine,
				PatientID:      pid,
				TestCode:       code,
				Template:       tplName,
				RequestedAt:    requestedAt.In(r.loc),
				CompletedAt:    localize(res.CompletedAt, r.loc),
				ValueRaw:       res.Value,
				Components:     res.Components,
				Interpretation: interpretation(res.Components, tpl, known),
				Modified:       isModified(res.Components),
				Discrepancy:    isDiscrepancy(res.Components),
				AbnormalFlag:   res.AbnormalFlag,
				ResultStatus:   res.Status,
				OrderStatus:    order.Status,
				Operator:       res.Operator,
				Instrument:     instrument,
			}

			out = append(out, model.Envelope{
				Result:  result,
				Payload: BuildPayload(result, tpl, known, sourceFile, receivedAt, r.loc),
			})
		}
	}
	return out
}

func (r *Registry) lookup(name string) (Template, bool) {
	if name == "" {
		return Template{}, false
	}
	tpl, ok := r.templates[normalizeName(name)]
	return tpl, ok
}

func localize(t *time.Time, loc *time.Location) *time.Time {
	if t == nil {
		return nil
	}
	v := t.In(loc)
	return &v
}

// interpretation mengambil nilai interpretasi: indeks yang dikonfigurasi bila
// ada dan terisi, kalau tidak komponen tidak kosong terakhir.
func interpretation(components []string, tpl Template, known bool) string {
	if known && tpl.InterpretationIndex != nil {
		i := *tpl.InterpretationIndex
		if i >= 0 && i < len(components) {
			if v := strings.TrimSpace(components[i]); v != "" {
				return v
			}
		}
	}
	for i := len(components) - 1; i >= 0; i-- {
		if v := strings.TrimSpace(components[i]); v != "" {
			return v
		}
	}
	return ""
}

// isModified mendeteksi nilai yang diubah manual oleh operator, mis. "M4+"
// (Configuration Guide hal. 127).
func isModified(components []string) bool {
	for _, c := range components {
		c = strings.TrimSpace(c)
		if len(c) < 2 || c[0] != 'M' {
			continue
		}
		if c[1] == '+' || c[1] == '-' || (c[1] >= '0' && c[1] <= '9') {
			return true
		}
	}
	return false
}

// isDiscrepancy mendeteksi hasil yang tidak konsisten, ditandai "Dis" pada
// salah satu komponen (lihat file contoh: `-^4+^Dis`).
//
// Deteksi ini harus ada di sini karena mesin tidak menandainya: abnormal flag
// untuk hasil seperti itu tetap "N", jadi tanpa pemeriksaan sendiri hasil
// meragukan akan lolos ke hilir seolah normal.
func isDiscrepancy(components []string) bool {
	for _, c := range components {
		if strings.EqualFold(strings.TrimSpace(c), "dis") {
			return true
		}
	}
	return false
}

// BuildPayload menyusun JSON yang dikirim ke Prima.
func BuildPayload(res model.Result, tpl Template, known bool, sourceFile string, receivedAt time.Time, loc *time.Location) model.Payload {
	components := make(map[string]string, len(res.Components))
	for i, v := range res.Components {
		name := "c" + strconv.Itoa(i)
		if known && i < len(tpl.Components) && strings.TrimSpace(tpl.Components[i]) != "" {
			name = tpl.Components[i]
		}
		components[name] = v
	}

	p := model.Payload{
		Machine:        res.Machine,
		PatientID:      res.PatientID,
		TestCode:       res.TestCode,
		Template:       res.Template,
		RequestedAt:    res.RequestedAt.UTC().Format(time.RFC3339),
		ValueRaw:       res.ValueRaw,
		Components:     components,
		Interpretation: res.Interpretation,
		Modified:       res.Modified,
		Discrepancy:    res.Discrepancy,
		AbnormalFlag:   res.AbnormalFlag,
		ResultStatus:   res.ResultStatus,
		OrderStatus:    res.OrderStatus,
		Operator:       res.Operator,
		Instrument:     res.Instrument,
		SourceFile:     sourceFile,
		ReceivedAt:     receivedAt.In(loc).UTC().Format(time.RFC3339),
	}
	if res.CompletedAt != nil {
		p.CompletedAt = res.CompletedAt.UTC().Format(time.RFC3339)
	}
	return p
}
