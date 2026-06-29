// hsrv.go -- a "warmed" HTTP workload for runsc checkpoint/restore clone demos.
//
// Allocates WARM_MB of heap filled with a checksummed pattern, then serves
// GET /healthz reporting:
//   - reqs:     an in-memory request counter (proves per-clone live state
//               survived restore: it CONTINUES across checkpoint/restore, and
//               each clone counts independently)
//   - checksum: ok if the warm memory still verifies (memory intact)
//   - uptime:   seconds since process start (preserved across restore -> a
//               restored clone reports an older start than its restore time)
//
// Build static (no libc) so the bundle needs no shared libs:
//   CGO_ENABLED=0 go build -o hsrv hsrv.go

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Client mode: `hsrv check` performs a real HTTP GET against the local
	// server over netstack loopback and prints the response. Used via
	// `runsc exec <container> /hsrv check` to health-check each clone.
	if len(os.Args) > 1 && os.Args[1] == "check" {
		resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			fmt.Println("CHECK FAILED:", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		fmt.Print(string(b))
		return
	}

	mb := 512
	if v := os.Getenv("WARM_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			mb = n
		}
	}
	n := mb * 1024 * 1024
	warm := make([]byte, n)
	for i := 0; i < n; i++ {
		warm[i] = byte((i*2654435761 + 1099511628211) & 0xff)
	}
	sample := func() uint64 {
		var s uint64
		for i := 0; i < n; i += 4096 {
			for o := 0; o < 64; o++ {
				s += uint64(warm[i+o])
			}
		}
		return s
	}
	ref := sample()

	var reqs int64
	start := time.Now()
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ok := "ok"
		if sample() != ref {
			ok = "BAD"
		}
		rc := atomic.AddInt64(&reqs, 1)
		fmt.Fprintf(w, "ok reqs=%d warm=%dMiB checksum=%s uptime=%ds pid=%d\n",
			rc, mb, ok, int(time.Since(start).Seconds()), os.Getpid())
	})

	fmt.Printf("ready warm=%dMiB port=%s pid=%d\n", mb, port, os.Getpid())
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, "serve error:", err)
		os.Exit(1)
	}
}
