// GVISOR-3 S1b in-tree test (place in pkg/sentry/pgalloc/ of the gVisor tree;
// add "sharedbase_test.go" to the go_test srcs in pkg/sentry/pgalloc/BUILD).
// Verifies copy-on-write semantics of the shared base file: base-backed reads,
// a shared base across two MemoryFiles, and private (COW) writes. PASSES under
// `bazel test //pkg/sentry/pgalloc:pgalloc_test`.
package pgalloc

import (
	"bytes"
	"os"
	"testing"

	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
)

func TestSharedBaseCOW(t *testing.T) {
	const baseLen = 4 << 20 // 4 MiB, well under the 1 GiB chunk -> exercises finer mapping
	base, err := os.CreateTemp("", "llifs-base-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(base.Name())
	defer base.Close()
	pat := make([]byte, baseLen)
	for i := range pat {
		pat[i] = byte(i*131 + 7)
	}
	if _, err := base.WriteAt(pat, 0); err != nil {
		t.Fatal(err)
	}

	mk := func() *MemoryFile {
		b, err := os.CreateTemp("", "llifs-backing-*")
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(b.Name())
		f, err := NewMemoryFile(b, MemoryFileOpts{SharedBaseFile: base, SharedBaseBytes: baseLen})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	rd := func(f *MemoryFile, fr memmap.FileRange, n int) []byte {
		bs, err := f.MapInternal(fr, hostarch.Read)
		if err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), bs.Head().ToSlice()[:n]...)
	}

	f1 := mk()
	defer f1.Destroy()
	fr1, err := f1.Allocate(uint64(hostarch.PageSize), AllocOpts{Mode: AllocateUncommitted, Dir: BottomUp})
	if err != nil {
		t.Fatal(err)
	}
	if fr1.Start >= baseLen {
		t.Fatalf("allocation landed outside base range: %v", fr1)
	}
	if got := rd(f1, fr1, 64); !bytes.Equal(got, pat[fr1.Start:fr1.Start+64]) {
		t.Fatalf("base-backed read mismatch at off %d: got %x want %x", fr1.Start, got, pat[fr1.Start:fr1.Start+64])
	}

	f2 := mk()
	defer f2.Destroy()
	fr2, err := f2.Allocate(uint64(hostarch.PageSize), AllocOpts{Mode: AllocateUncommitted, Dir: BottomUp})
	if err != nil {
		t.Fatal(err)
	}
	if got := rd(f2, fr2, 64); !bytes.Equal(got, pat[fr2.Start:fr2.Start+64]) {
		t.Fatalf("second MemoryFile base read mismatch")
	}

	// Copy-on-write: write through f1; the base file and f2 stay unchanged, f1 sees it.
	bs, err := f1.MapInternal(fr1, hostarch.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	want := ^pat[fr1.Start]
	bs.Head().ToSlice()[0] = want
	chk := make([]byte, 1)
	if _, err := base.ReadAt(chk, int64(fr1.Start)); err != nil {
		t.Fatal(err)
	}
	if chk[0] != pat[fr1.Start] {
		t.Fatalf("base FILE mutated by a private write: %x", chk[0])
	}
	if got := rd(f2, fr2, 1); got[0] != pat[fr2.Start] {
		t.Fatalf("f2 observed f1 private write (no isolation)")
	}
	if got := rd(f1, fr1, 1); got[0] != want {
		t.Fatalf("f1 did not observe its own write: %x", got[0])
	}
}
