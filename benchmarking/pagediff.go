// pagediff.go -- page-by-page delta between two memfd snapshots.
//
// Usage: pagediff <base-snap> <later-snap> [pagesize]
//
// Reports how many 4 KiB pages differ between two guest-memory snapshots: this is
// the per-agent memory delta over the base (the N*delta term in the GVISOR-3 cost
// model 1*base + N*delta). Holes read as zero, so a page present in one snapshot
// and absent in the other counts as differing. Also prints a coarse region
// histogram (which 64 MiB spans hold the delta) to show clustering.
package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: pagediff <base-snap> <later-snap> [pagesize]")
		os.Exit(2)
	}
	pg := 4096
	if len(os.Args) > 3 {
		if v, err := strconv.Atoi(os.Args[3]); err == nil && v > 0 {
			pg = v
		}
	}

	a, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer a.Close()
	b, err := os.Open(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer b.Close()

	sa, _ := a.Stat()
	sb, _ := b.Stat()
	size := sa.Size()
	if sb.Size() > size {
		size = sb.Size()
	}

	const region = 64 << 20 // 64 MiB histogram buckets
	regions := make([]int64, size/region+1)

	ba := make([]byte, pg)
	bb := make([]byte, pg)
	var totalPages, diffPages int64
	for off := int64(0); off < size; off += int64(pg) {
		for i := range ba {
			ba[i] = 0
			bb[i] = 0
		}
		a.ReadAt(ba, off) // short read at EOF leaves zeros = hole semantics
		b.ReadAt(bb, off)
		totalPages++
		differ := false
		for i := 0; i < pg; i++ {
			if ba[i] != bb[i] {
				differ = true
				break
			}
		}
		if differ {
			diffPages++
			regions[off/region]++
		}
	}

	fmt.Printf("delta: pages_differ=%d / %d  =  %d MiB changed (page=%dB, span=%dMiB)\n",
		diffPages, totalPages, (diffPages*int64(pg))>>20, pg, size>>20)
	fmt.Println("region histogram (64MiB buckets with >0 changed pages):")
	for i, c := range regions {
		if c > 0 {
			fmt.Printf("  [%4d] off=%5dMiB  changed=%d pages (%d MiB)\n",
				i, int64(i)*region>>20, c, (c*int64(pg))>>20)
		}
	}
}
