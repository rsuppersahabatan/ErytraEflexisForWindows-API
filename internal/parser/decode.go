package parser

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// Decode menormalkan isi berkas ke UTF-8 sesuai pengaturan Diana Server.
// LIS Encoding di Diana Server: UTF-8 (default) atau ANSI (Configuration Guide hal. 107).
func Decode(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf-8", "utf8":
		return data, nil
	case "windows-1252", "ansi":
		out, err := charmap.Windows1252.NewDecoder().Bytes(data)
		if err != nil {
			return nil, fmt.Errorf("decode windows-1252: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("encoding %q tidak didukung", encoding)
	}
}
