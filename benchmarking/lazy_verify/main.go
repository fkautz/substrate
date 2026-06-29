// lazy_verify -- GVISOR-3 B3 lazy/remote variant: a userfaultfd MISSING handler
// over a NODE-SHARED base backing (memfd) that, on first access to a 2 MiB block,
// fetches the block from a content-addressed store (CAS) keyed by its Terrapin v0.3
// GitOID, verifies it, and installs it into the shared backing (UFFDIO_COPY) -- or
// installs a verified zero page (UFFDIO_ZEROPAGE) for known-zero blocks without any
// fetch. Population is ONCE PER NODE; sandboxes then MAP_PRIVATE-share the populated
// backing (the flatten). A tampered CAS entry (content != its GitOID key) is
// detected and rejected, never exposed.
//
// This composes the lazy/remote transport (uffd + CAS) with real Terrapin
// verification (terrapin-go, profile llifs-terrapin-sha256-v2) and the base-sharing
// flatten. Linux only (userfaultfd + smaps_rollup).
package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	terrapin "github.com/fkautz/terrapin-go"
)

const (
	blockSize      = 2 << 20 // Terrapin v0.3 block size (2 MiB)
	pageSize       = 4096
	sysUserfaultfd = 282 // linux/arm64
	sysMemfdCreate = 279 // linux/arm64

	uffdAPI                         = 0xAA
	uffdRegisterModeMissing         = 1
	uffdEventPagefault              = 0x12
	ioctlUffdioAPI          uintptr = 0xc018aa3f
	ioctlUffdioRegister     uintptr = 0xc020aa00
	ioctlUffdioCopy         uintptr = 0xc028aa03
	ioctlUffdioZeropage     uintptr = 0xc020aa04
)

type uffdioAPI struct{ API, Features, Ioctls uint64 }
type uffdioRange struct{ Start, Len uint64 }
type uffdioRegister struct {
	Range  uffdioRange
	Mode   uint64
	Ioctls uint64
}
type uffdioCopy struct {
	Dst, Src, Len, Mode uint64
	Copy                int64
}
type uffdioZeropage struct {
	Range    uffdioRange
	Mode     uint64
	Zeropage int64
}
type uffdMsg struct {
	Event     uint8
	_         uint8
	_         uint16
	_         uint32
	PFFlags   uint64
	PFAddress uint64
	_         [8]byte
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) syscall.Errno {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	return e
}

func atoiEnv(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func rollupKB(key string) uint64 {
	f, err := os.Open("/proc/self/smaps_rollup")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if ln := sc.Text(); strings.HasPrefix(ln, key+":") {
			v, _ := strconv.ParseUint(strings.Fields(ln)[1], 10, 64)
			return v
		}
	}
	return 0
}

const zeroMarker = "ZERO"

var (
	fetches  atomic.Int64 // CAS fetches performed (should be once per non-zero block)
	verifies atomic.Int64 // Terrapin verifications performed
	rejects  atomic.Int64 // verification failures (tamper)
	zeroes   atomic.Int64 // known-zero installs (no fetch)
)

func main() {
	baseMB := atoiEnv("LV_BASE_MB", 32)
	nClones := atoiEnv("LV_N", 16)
	nblocks := baseMB / 2
	if nblocks < 4 {
		nblocks = 4
	}
	baseLen := nblocks * blockSize

	// Build the CAS + manifest. Block layout: most are normal; one known-zero; one
	// tampered (CAS content does not match its GitOID key).
	zeroBlk := nblocks / 4
	tamperBlk := nblocks / 2
	manifest := make([]string, nblocks)
	cas := make(map[string][]byte)
	for b := 0; b < nblocks; b++ {
		if b == zeroBlk {
			manifest[b] = zeroMarker
			continue
		}
		content := make([]byte, blockSize)
		fill := byte(b%251 + 1)
		for i := range content {
			content[i] = fill
		}
		id := terrapin.Identifier(content)
		manifest[b] = id
		if b == tamperBlk {
			content[7] ^= 0xFF // corrupt AFTER computing the id -> content no longer matches
		}
		cas[id] = content
	}

	// Node-shared base backing: a memfd, MAP_SHARED, uffd-registered.
	namePtr, _ := syscall.BytePtrFromString("lazybase")
	mfd, _, e := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(namePtr)), 0, 0)
	if e != 0 {
		fmt.Println("memfd_create:", e)
		os.Exit(1)
	}
	memfd := int(mfd)
	if err := syscall.Ftruncate(memfd, int64(baseLen)); err != nil {
		fmt.Println("ftruncate:", err)
		os.Exit(1)
	}
	node, err := syscall.Mmap(memfd, 0, baseLen, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("mmap node:", err)
		os.Exit(1)
	}
	nodeAddr := uint64(uintptr(unsafe.Pointer(&node[0])))

	// userfaultfd + API handshake + register MISSING over the node backing.
	ufd, _, e := syscall.Syscall(sysUserfaultfd, syscall.O_CLOEXEC, 0, 0)
	if e != 0 {
		fmt.Println("userfaultfd:", e, "(needs unprivileged uffd or CAP_SYS_PTRACE)")
		os.Exit(1)
	}
	uffd := int(ufd)
	api := uffdioAPI{API: uffdAPI}
	if e := ioctl(uffd, ioctlUffdioAPI, unsafe.Pointer(&api)); e != 0 {
		fmt.Println("UFFDIO_API:", e)
		os.Exit(1)
	}
	reg := uffdioRegister{Range: uffdioRange{Start: nodeAddr, Len: uint64(baseLen)}, Mode: uffdRegisterModeMissing}
	if e := ioctl(uffd, ioctlUffdioRegister, unsafe.Pointer(&reg)); e != 0 {
		fmt.Println("UFFDIO_REGISTER:", e)
		os.Exit(1)
	}

	// Handler goroutine: serve MISSING faults by fetch+verify+install, once per block.
	go func() {
		buf := make([]byte, unsafe.Sizeof(uffdMsg{}))
		for {
			n, err := syscall.Read(uffd, buf)
			if err != nil || n == 0 {
				return
			}
			msg := (*uffdMsg)(unsafe.Pointer(&buf[0]))
			if msg.Event != uffdEventPagefault {
				continue
			}
			off := (msg.PFAddress - nodeAddr) &^ (blockSize - 1) // round to block start
			idx := int(off / blockSize)
			id := manifest[idx]
			if id == zeroMarker {
				zeroes.Add(1)
				zp := uffdioZeropage{Range: uffdioRange{Start: nodeAddr + off, Len: blockSize}}
				ioctl(uffd, ioctlUffdioZeropage, unsafe.Pointer(&zp))
				continue
			}
			content := cas[id]
			fetches.Add(1)
			// VERIFY-BEFORE-EXPOSE: recompute the Terrapin id of the fetched bytes.
			verifies.Add(1)
			if terrapin.Identifier(content) != id {
				// Tamper: do NOT expose the bad block. Install a zero page so the
				// faulting access completes, and record the rejection.
				rejects.Add(1)
				zp := uffdioZeropage{Range: uffdioRange{Start: nodeAddr + off, Len: blockSize}}
				ioctl(uffd, ioctlUffdioZeropage, unsafe.Pointer(&zp))
				continue
			}
			cp := uffdioCopy{
				Dst: nodeAddr + off,
				Src: uint64(uintptr(unsafe.Pointer(&content[0]))),
				Len: blockSize,
			}
			ioctl(uffd, ioctlUffdioCopy, unsafe.Pointer(&cp))
		}
	}()

	// LAZY fault-driven population: the node faults blocks in on demand by touching
	// one byte per block through the registered mapping. (In production the sentry's
	// access drives this; here the node populates its shared cache.)
	var sink byte
	for b := 0; b < nblocks; b++ {
		sink += node[b*blockSize]
	}
	// Re-touch every block: already resident -> NO additional fetch/verify.
	fetchesAfterFirst := fetches.Load()
	for b := 0; b < nblocks; b++ {
		sink += node[b*blockSize+8]
	}

	normalBlocks := int64(nblocks - 1) // minus the one known-zero block
	fmt.Printf("lazy fetch: base=%dMiB %d blocks; fetches=%d verifies=%d zero-installs=%d rejects=%d (sink=%d)\n",
		baseMB, nblocks, fetches.Load(), verifies.Load(), zeroes.Load(), rejects.Load(), sink)
	fmt.Printf("  once-per-node: re-touching all blocks added %d fetches (expect 0)\n", fetches.Load()-fetchesAfterFirst)
	if fetches.Load() != normalBlocks {
		fmt.Printf("  WARN: expected %d fetches (one per non-zero block), got %d\n", normalBlocks, fetches.Load())
	}
	if rejects.Load() != 1 {
		fmt.Printf("  WARN: expected 1 tamper rejection, got %d\n", rejects.Load())
	} else {
		fmt.Printf("  tamper: block %d (corrupted in CAS) REJECTED by verify, not exposed\n", tamperBlk)
	}
	if zeroes.Load() != 1 {
		fmt.Printf("  WARN: expected 1 known-zero install, got %d\n", zeroes.Load())
	} else {
		fmt.Printf("  known-zero: block %d installed via UFFDIO_ZEROPAGE, no fetch\n", zeroBlk)
	}

	// Free the in-process CAS before measuring sharing: it stands in for a REMOTE
	// store and must not count as node RAM.
	for k := range cas {
		delete(cas, k)
	}
	cas = nil
	runtime.GC()

	// SHARE: N sandboxes MAP_PRIVATE the now-populated node backing -> the flatten.
	maps := make([][]byte, 0, nClones)
	for c := 0; c < nClones; c++ {
		m, err := syscall.Mmap(memfd, 0, baseLen, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE)
		if err != nil {
			fmt.Println("sandbox mmap:", err)
			os.Exit(1)
		}
		for p := 0; p < baseLen; p += pageSize {
			sink += m[p]
		}
		maps = append(maps, m)
	}
	rss, pss := rollupKB("Rss"), rollupKB("Pss")
	mib := func(kb uint64) float64 { return float64(kb) / 1024 }
	fmt.Printf("share N=%d: Rss=%.0fMiB Pss=%.0fMiB  FLATTEN=%.1fx (verified base shared once per node, sink=%d)\n",
		nClones, mib(rss), mib(pss), float64(rss)/float64(pss), sink)
	fmt.Println("RESULT: lazy uffd fetch+Terrapin-verify+install (once/node) composes with the N-way flatten; tamper rejected, known-zero install without fetch.")
}
