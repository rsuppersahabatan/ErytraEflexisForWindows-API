// Command mockprima adalah service Prima tiruan untuk pengujian lokal.
//
// Jalankan: go run ./tools/mockprima -addr 127.0.0.1:9099
// Lalu arahkan prima.url di config ke alamat tersebut.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "alamat listen")
	status := flag.Int("status", 200, "kode respons yang dikembalikan")
	flag.Parse()

	var count atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/lab/result", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := count.Add(1)

		key := r.Header.Get("Idempotency-Key")
		log.Printf("#%d %s %s idem=%q bytes=%d", n, r.Method, r.URL.Path, key, len(body))

		if *status >= 300 {
			w.WriteHeader(*status)
			fmt.Fprintf(w, `{"error":"mock menolak dengan status %d"}`, *status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(*status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"seq":    n,
			"idem":   key,
			"at":     time.Now().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/_count", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count.Load()})
	})

	log.Printf("mock Prima mendengarkan di http://%s (status=%d)", *addr, *status)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
