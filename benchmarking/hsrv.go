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

	// Load client: `hsrv load <N>` issues N health-check requests from a SINGLE
	// process (so the delta measurement is not confounded by many transient guest
	// client processes -- only the server's own divergence is measured).
	if len(os.Args) > 2 && os.Args[1] == "load" {
		nreq, _ := strconv.Atoi(os.Args[2])
		last := ""
		for i := 0; i < nreq; i++ {
			resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
			if err != nil {
				fmt.Println("LOAD FAILED:", err)
				os.Exit(1)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			last = string(b)
		}
		fmt.Printf("load done n=%d last=%s", nreq, last)
		return
	}

	// Control client: `hsrv dirty <MiB>` asks the server to write that many MiB
	// of its warm heap (one byte per 4 KiB page), producing a KNOWN delta so the
	// memfd-diff harness can be validated against ground truth.
	if len(os.Args) > 2 && os.Args[1] == "dirty" {
		resp, err := http.Get("http://127.0.0.1:" + port + "/dirty?mb=" + os.Args[2])
		if err != nil {
			fmt.Println("DIRTY FAILED:", err)
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
	// /dirty?mb=N writes one byte into each of the first N*256 pages of the warm
	// heap, dirtying exactly N MiB of resident pages (the ground-truth delta).
	http.HandleFunc("/dirty", func(w http.ResponseWriter, r *http.Request) {
		d, _ := strconv.Atoi(r.URL.Query().Get("mb"))
		pages := d * 256
		dirtied := 0
		for p := 0; p < pages && p*4096 < n; p++ {
			warm[p*4096]++
			dirtied++
		}
		fmt.Fprintf(w, "dirtied pages=%d (%d MiB) pid=%d\n", dirtied, dirtied/256, os.Getpid())
	})
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
