// verify_share -- GVISOR-3 B3: Terrapin verify-before-expose composed with the
// base-sharing flatten. Demonstrates that the integrity layer and the density
// flatten hold TOGETHER:
//
//  1. Build a base of 2 MiB blocks and compute its Terrapin v0.3 manifest
//     (per-block GitOID-SHA256 + the whole-base identifier), using the real
//     terrapin-go (profile llifs-terrapin-sha256-v2).
//  2. VERIFY-BEFORE-EXPOSE (node level, once per node, MVERIFY-2): re-hash every
//     block and check it against the manifest BEFORE any sandbox maps the base.
//  3. SHARE: N sandboxes MAP_PRIVATE the verified base; /proc/self/smaps_rollup
//     shows the flatten (Rss counts the base per mapping, Pss once physically).
//  4. TAMPER: flip one byte of the base and re-verify -> the block's GitOID no
//     longer matches, so it is REJECTED and never exposed.
//
// Verification is once per node and independent of N, so it does not change the
// flatten ratio -- it only gates whether the shared base is trusted.
//
// Run on Linux (needs smaps_rollup). Env: VS_BASE_MB (default 64), VS_N (16),
// VS_DELTA_MB (4).
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	terrapin "github.com/fkautz/terrapin-go"
)

const blockSize = 2 << 20 // 2 MiB, the Terrapin v0.3 block size

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
		ln := sc.Text()
		if strings.HasPrefix(ln, key+":") {
			fields := strings.Fields(ln)
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			return v
		}
	}
	return 0
}

func main() {
	baseMB := atoiEnv("VS_BASE_MB", 64)
	nClones := atoiEnv("VS_N", 16)
	deltaMB := atoiEnv("VS_DELTA_MB", 4)
	nblocks := baseMB / 2
	if nblocks < 1 {
		nblocks = 1
	}
	baseLen := nblocks * blockSize

	// 1. Build the base file and its Terrapin manifest, streaming block-by-block so
	//    no large buffer stays resident to confound the rollup later.
	tmp, err := os.CreateTemp("", "verify-base-*")
	if err != nil {
		panic(err)
	}
	basePath := tmp.Name()
	defer os.Remove(basePath)
	manifest := make([]string, nblocks)
	blk := make([]byte, blockSize)
	for b := 0; b < nblocks; b++ {
		fill := byte(b%251 + 1) // non-zero, distinct per block
		for i := range blk {
			blk[i] = fill
		}
		if _, err := tmp.Write(blk); err != nil {
			panic(err)
		}
		manifest[b] = terrapin.Identifier(blk)
	}
	tmp.Close()
	bf, err := os.Open(basePath)
	if err != nil {
		panic(err)
	}
	baseID, err := terrapin.IdentifierFromReader(bf)
	bf.Close()
	if err != nil {
		fmt.Println("base identifier error:", err)
		os.Exit(1)
	}

	fmt.Printf("base: %d MiB = %d blocks of 2 MiB; terrapin id = %s\n", baseMB, nblocks, baseID)

	// verifyBase re-hashes every block against the manifest (verify-before-expose).
	verifyBase := func(path string) (bool, int, time.Duration) {
		f, err := os.Open(path)
		if err != nil {
			return false, -1, 0
		}
		defer f.Close()
		buf := make([]byte, blockSize)
		start := time.Now()
		for b := 0; b < nblocks; b++ {
			if _, err := f.ReadAt(buf, int64(b)*blockSize); err != nil {
				return false, b, time.Since(start)
			}
			if terrapin.Identifier(buf) != manifest[b] {
				return false, b, time.Since(start) // mismatch -> reject this block
			}
		}
		return true, -1, time.Since(start)
	}

	// 2. Verify-before-expose: gate the base before any sandbox maps it.
	ok, badBlk, dur := verifyBase(basePath)
	if !ok {
		fmt.Printf("FATAL: clean base failed verification at block %d\n", badBlk)
		os.Exit(1)
	}
	gbs := float64(baseLen) / dur.Seconds() / 1e9
	fmt.Printf("verify-before-expose: %d blocks OK in %s (%.2f GB/s; once per node, independent of N)\n",
		nblocks, dur.Round(time.Microsecond), gbs)

	// 3. Share the verified base MAP_PRIVATE across N sandboxes; a small per-clone
	//    delta write shows COW still works after verification.
	fd, err := syscall.Open(basePath, syscall.O_RDWR, 0)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(fd)
	deltaPages := deltaMB * 256
	maps := make([][]byte, 0, nClones)
	var sink byte
	for c := 0; c < nClones; c++ {
		m, err := syscall.Mmap(fd, 0, baseLen, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE)
		if err != nil {
			panic(err)
		}
		for p := 0; p < baseLen; p += 4096 {
			sink += m[p] // fault base pages resident (shared from the page cache)
		}
		for p := 0; p < deltaPages && p*4096 < baseLen; p++ {
			m[p*4096] ^= 0xFF // per-clone delta -> COW private
		}
		maps = append(maps, m)
	}
	rss := rollupKB("Rss")
	pss := rollupKB("Pss")
	priv := rollupKB("Private_Dirty")
	mib := func(kb uint64) float64 { return float64(kb) / 1024 }
	fmt.Printf("share N=%d: Rss=%.0fMiB (no-share) Pss=%.0fMiB (shared) Private_Dirty=%.0fMiB  (sink=%d)\n",
		nClones, mib(rss), mib(pss), mib(priv), sink)
	if pss > 0 {
		fmt.Printf("FLATTEN (Rss/Pss) = %.1fx  with verify-before-expose in front\n", float64(rss)/float64(pss))
	}

	// 4. Tamper test: corrupt one byte and re-verify -> rejected, not exposed.
	tf, err := os.OpenFile(basePath, os.O_RDWR, 0)
	if err != nil {
		panic(err)
	}
	victim := nblocks / 2
	orig := make([]byte, 1)
	tf.ReadAt(orig, int64(victim)*blockSize+123)
	tf.WriteAt([]byte{orig[0] ^ 0x01}, int64(victim)*blockSize+123)
	tf.Close()
	ok2, badBlk2, _ := verifyBase(basePath)
	if ok2 {
		fmt.Println("FAIL: tampered base passed verification (integrity broken)")
		os.Exit(1)
	}
	fmt.Printf("tamper test: flipped 1 byte in block %d -> verify REJECTED at block %d (not exposed)\n", victim, badBlk2)

	fmt.Println("RESULT: verify-before-expose and the base-sharing flatten hold together (Terrapin v0.3).")
}
