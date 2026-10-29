// Package model berisi tipe domain yang dipakai lintas paket.
package model

import "time"

// Result adalah satu hasil pemeriksaan hasil normalisasi dari record ASTM.
type Result struct {
	Machine        string
	PatientID      string
	TestCode       string
	Template       string
	RequestedAt    time.Time
	CompletedAt    *time.Time
	ValueRaw       string
	Components     []string
	Interpretation string
	Modified       bool
	AbnormalFlag   string
	ResultStatus   string
	OrderStatus    string
	Operator       string
	Instrument     string
}

// IdemKey adalah kunci idempotency yang dikirim ke Prima sebagai Idempotency-Key.
// Satu hasil pemeriksaan = satu kunci, tidak bergantung pada file sumbernya.
func (r Result) IdemKey() string {
	return r.PatientID + "|" + r.TestCode + "|" + r.RequestedAt.UTC().Format(time.RFC3339)
}

// Payload adalah bentuk JSON yang dikirim ke service Prima.
// Field-nya sengaja datar dan bernama jelas supaya mudah dipetakan di sisi penerima.
type Payload struct {
	Machine        string            `json:"machine"`
	PatientID      string            `json:"patient_id"`
	TestCode       string            `json:"test_code"`
	Template       string            `json:"template,omitempty"`
	RequestedAt    string            `json:"requested_at"`
	CompletedAt    string            `json:"completed_at,omitempty"`
	ValueRaw       string            `json:"value_raw"`
	Components     map[string]string `json:"components"`
	Interpretation string            `json:"interpretation,omitempty"`
	Modified       bool              `json:"modified"`
	AbnormalFlag   string            `json:"abnormal_flag,omitempty"`
	ResultStatus   string            `json:"result_status,omitempty"`
	OrderStatus    string            `json:"order_status,omitempty"`
	Operator       string            `json:"operator,omitempty"`
	Instrument     string            `json:"instrument,omitempty"`
	SourceFile     string            `json:"source_file"`
	ReceivedAt     string            `json:"received_at"`
}

// Envelope memasangkan hasil ternormalisasi dengan payload yang akan dikirim.
// Kunci idempotency diambil dari Result, isi request dari Payload.
type Envelope struct {
	Result  Result
	Payload Payload
}
