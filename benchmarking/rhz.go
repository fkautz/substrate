// rhz.go -- RHAZARD clone-divergence probe.
//
// Long-running HTTP server. GET /probe returns FRESH samples from every source
// that, if shared across clones restored from one checkpoint, is a correctness
// or security hazard:
//   getrandom : 8 bytes from the kernel CSPRNG (crypto/rand -> getrandom syscall)
//   urandom   : 8 bytes from /dev/urandom
//   kuuid     : /proc/sys/kernel/random/uuid (kernel generates a fresh UUID)
//   bootid    : /proc/sys/kernel/random/boot_id (per-boot identity)
//   prng      : next value from a math/rand PRNG seeded ONCE at startup
//               (in-memory state -> identical across clones == the hazard)
//   mono      : monotonic time since start (does the clock advance post-restore?)
//   wall      : wall-clock unix seconds
//   heapobj   : address of a startup heap allocation (ASLR / layout identity)
//   pid       : guest pid
//
// Restore N clones from one checkpoint, probe each, and diff: a field that is
// IDENTICAL across clones is shared frozen state that the substrate must refresh
// (RHAZARD-1..7); a field that DIFFERS is something gVisor already refreshes.
//
// Build: CGO_ENABLED=0 go build -o rhz rhz.go
// Probe: runsc exec <id> /rhz probe

package main

import (
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func readN(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "ERR"
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err := io.ReadFull(f, b); err != nil {
		return "ERR"
	}
	return hex.EncodeToString(b)
}

func proc(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "ERR"
	}
	return strings.TrimSpace(string(b))
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		resp, err := http.Get("http://127.0.0.1:" + port + "/probe")
		if err != nil {
			fmt.Println("PROBE FAILED:", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		fmt.Print(string(b))
		return
	}

	var mu sync.Mutex
	prng := rand.New(rand.NewSource(time.Now().UnixNano())) // seeded once, at startup
	start := time.Now()
	heapObj := new([16]byte) // a startup heap allocation

	http.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		cr := make([]byte, 8)
		crand.Read(cr)
		mu.Lock()
		pv := prng.Uint64()
		mu.Unlock()
		fmt.Fprintf(w,
			"getrandom=%s urandom=%s kuuid=%s bootid=%s prng=%016x mono=%dms wall=%d heapobj=%p pid=%d\n",
			hex.EncodeToString(cr), readN("/dev/urandom", 8),
			proc("/proc/sys/kernel/random/uuid"), proc("/proc/sys/kernel/random/boot_id"),
			pv, time.Since(start).Milliseconds(), time.Now().Unix(), heapObj, os.Getpid())
	})
	fmt.Printf("ready pid=%d\n", os.Getpid())
	http.ListenAndServe("0.0.0.0:"+port, nil)
}
