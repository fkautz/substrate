// Copyright 2024 The gVisor Authors.
// LLIFS GVISOR-3 B1: SaveTo->LoadFrom round-trip harness. This is the enabling
// test for the base/delta split: it drives the REAL pgalloc save/restore path
// (packed pages file via stateio + stateify metadata) end to end, so subsequent
// base-backed-skip changes to SaveTo/LoadFrom can be verified here.
package pgalloc

import (
	"bytes"
	"os"
	"sync"
	"syscall"
	"testing"

	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/state/stateio"
	"gvisor.dev/gvisor/pkg/state"
)

func mkMemFile(t *testing.T, opts MemoryFileOpts) *MemoryFile {
	t.Helper()
	b, err := os.CreateTemp("", "llifs-mf-*")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(b.Name())
	f, err := NewMemoryFile(b, opts)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSaveRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	pg := uint64(hostarch.PageSize)
	const npages = 16

	src := mkMemFile(t, MemoryFileOpts{DisableMemoryAccounting: true})
	defer src.Destroy()
	fr, err := src.Allocate(npages*pg, AllocOpts{Mode: AllocateAndCommit, Dir: BottomUp})
	if err != nil {
		t.Fatal(err)
	}
	ssl, err := src.MapInternal(fr, hostarch.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	smem := ssl.Head().ToSlice()
	val := func(p int) byte {
		if p == 7 {
			return 0 // a committed zero page, to exercise zero-exclusion
		}
		return byte(p*13 + 1)
	}
	for p := 0; p < npages; p++ {
		for i := 0; i < int(pg); i++ {
			smem[p*int(pg)+i] = val(p)
		}
	}

	// Pages file: use raw fds so the writer/reader own them (no os.File finalizer
	// double-close), reopening the same path for the read side.
	tmp, err := os.CreateTemp("", "llifs-pages-*")
	if err != nil {
		t.Fatal(err)
	}
	pfName := tmp.Name()
	tmp.Close()
	defer os.Remove(pfName)

	// --- SAVE ---
	wfd, err := syscall.Open(pfName, syscall.O_RDWR|syscall.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	aw := stateio.NewPagesFileFDWriterDefault(int32(wfd)) // takes ownership of wfd
	var swg sync.WaitGroup
	var saveErr error
	swg.Add(1)
	apfs, err := StartAsyncPagesFileSave(aw, func(e error) { saveErr = e; swg.Done() })
	if err != nil {
		t.Fatal(err)
	}
	var meta bytes.Buffer
	if err := src.SaveTo(ctx, &meta, &SaveOpts{PagesFile: apfs, ExcludeCommittedZeroPages: true}); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	apfs.MemoryFilesDone()
	swg.Wait()
	if saveErr != nil {
		t.Fatalf("async page save: %v", saveErr)
	}

	// --- LOAD ---
	rfd, err := syscall.Open(pfName, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	ar := stateio.NewPagesFileFDReaderDefault(int32(rfd)) // takes ownership of rfd
	var lwg sync.WaitGroup
	var loadErr error
	lwg.Add(1)
	apfl, err := StartAsyncPagesFileLoad(ar, func(e error) { loadErr = e; lwg.Done() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	dst := mkMemFile(t, MemoryFileOpts{DisableMemoryAccounting: true})
	defer dst.Destroy()
	if err := dst.LoadFrom(ctx, bytes.NewReader(meta.Bytes()), &LoadOpts{PagesFile: apfl}); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	apfl.MemoryFilesDone()
	if err := dst.AwaitLoadAll(); err != nil {
		t.Fatalf("AwaitLoadAll: %v", err)
	}
	lwg.Wait()
	if loadErr != nil {
		t.Fatalf("async page load: %v", loadErr)
	}

	// --- COMPARE ---
	dsl, err := dst.MapInternal(fr, hostarch.Read)
	if err != nil {
		t.Fatalf("dst MapInternal: %v", err)
	}
	dmem := dsl.Head().ToSlice()
	for p := 0; p < npages; p++ {
		want := val(p)
		for i := 0; i < int(pg); i++ {
			if dmem[p*int(pg)+i] != want {
				t.Fatalf("restored page %d byte %d: got %#x want %#x", p, i, dmem[p*int(pg)+i], want)
			}
		}
	}
}

// TestSaveWithBaseExcludesDelta verifies A6: when a base image is supplied to
// SaveTo, committed pages identical to the base are excluded from the pages file
// and recorded in memoryFileSaved.baseBacked; only the delta is written.
func TestSaveWithBaseExcludesDelta(t *testing.T) {
	ctx := context.Background()
	pg := uint64(hostarch.PageSize)
	const npages, ndelta = 16, 3

	src := mkMemFile(t, MemoryFileOpts{DisableMemoryAccounting: true})
	defer src.Destroy()
	fr, err := src.Allocate(npages*pg, AllocOpts{Mode: AllocateAndCommit, Dir: BottomUp})
	if err != nil {
		t.Fatal(err)
	}
	ssl, err := src.MapInternal(fr, hostarch.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	smem := ssl.Head().ToSlice()
	for p := 0; p < npages; p++ {
		for i := 0; i < int(pg); i++ {
			smem[p*int(pg)+i] = byte(p*13 + 1) // all non-zero, distinct
		}
	}

	base, err := os.CreateTemp("", "llifs-base-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(base.Name())
	defer base.Close()
	bsz, err := src.ExportLinearBase(base)
	if err != nil {
		t.Fatal(err)
	}
	// Modify ndelta pages AFTER export -> these are the only pages that must be
	// written when saving against the base.
	for _, p := range []int{2, 8, 13} {
		smem[p*int(pg)] ^= 0xFF
	}

	doSave := func(useBase bool) (uint64, []byte) {
		tmp, err := os.CreateTemp("", "llifs-pages-*")
		if err != nil {
			t.Fatal(err)
		}
		name := tmp.Name()
		tmp.Close()
		defer os.Remove(name)
		wfd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_TRUNC, 0)
		if err != nil {
			t.Fatal(err)
		}
		aw := stateio.NewPagesFileFDWriterDefault(int32(wfd))
		var wg sync.WaitGroup
		var serr error
		wg.Add(1)
		apfs, err := StartAsyncPagesFileSave(aw, func(e error) { serr = e; wg.Done() })
		if err != nil {
			t.Fatal(err)
		}
		so := &SaveOpts{PagesFile: apfs, ExcludeCommittedZeroPages: true}
		if useBase {
			so.SharedBaseFile = base
			so.SharedBaseBytes = bsz
		}
		var meta bytes.Buffer
		if err := src.SaveTo(ctx, &meta, so); err != nil {
			t.Fatalf("SaveTo: %v", err)
		}
		off := apfs.PagesFileOffset()
		apfs.MemoryFilesDone()
		wg.Wait()
		if serr != nil {
			t.Fatalf("async save: %v", serr)
		}
		return off, meta.Bytes()
	}

	offNoBase, _ := doSave(false)
	offBase, metaBase := doSave(true)

	if offNoBase != npages*pg {
		t.Errorf("no-base pages file: got %d bytes, want %d (%d pages)", offNoBase, npages*pg, npages)
	}
	if offBase != ndelta*pg {
		t.Errorf("with-base pages file: got %d bytes, want %d (%d delta pages)", offBase, ndelta*pg, ndelta)
	}

	var mfs memoryFileSaved
	if _, err := state.Load(ctx, bytes.NewReader(metaBase), &mfs); err != nil {
		t.Fatalf("load metadata: %v", err)
	}
	var bbPages uint64
	for st, en := range mfs.baseBacked {
		bbPages += (en - st) / pg
	}
	if bbPages != npages-ndelta {
		t.Errorf("baseBacked pages: got %d, want %d", bbPages, npages-ndelta)
	}
}
