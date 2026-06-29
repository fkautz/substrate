# gVisor changes required for production LLIFS (running ledger)

Authoritative, growing list of EVERY change gVisor needs for the LLIFS shared-base
(GVISOR-3) + verified-lazy-restore design. Prototype increments are marked; the rest
are the production patch backlog. Update this whenever a new requirement is found.

Status key: [proto] prototyped here (not production); [needed] not yet done;
[found] a constraint discovered by experiment.

## A. MemoryFile / pgalloc (pkg/sentry/pgalloc)

A1. [proto] Shared-base option. `MemoryFileOpts.SharedBaseFile` + `SharedBaseBytes`;
    base-range chunks mapped `MAP_PRIVATE` from the shared read-only base file.
    (gvisor3-s1.pgalloc.patch.) Production: input validation, page-alignment
    requirements, docs, and a real base-region abstraction.

A2. [found] chunkSize is 1 GiB (`chunkShift=30`), not the 2 MiB LLIFS block. Base
    sharing must be FINER than the chunk. [proto] S1b maps the [0,SharedBaseBytes)
    sub-range of each chunk MAP_PRIVATE. Production: a first-class base-region map
    (e.g. per 2 MiB) decoupled from the 1 GiB chunk allocator, so sub-GiB agents
    share and the base can align to LLIFS blocks.

A3. [found][needed] Allocate contract conflict. `Allocate` documents "returns
    initially-zeroed pages." Base-overlaid pages are NOT zero -- they carry base
    content (that is the point). NARROWED by S1b test: `AllocateUncommitted` does
    NOT actively zero, so base content surfaces correctly through the existing alloc
    path for reads (TestSharedBaseCOW passes). Residual production work: the
    recycle/free path MUST NOT zero or decommit base-range pages (untested), and
    modes that populate/zero (AllocateAndCommit / AllocateAndWritePopulate) would
    COW the base away -- base-range allocations must force the uncommitted/base-aware
    path.

A4. [needed] Decommit/eviction for base chunks. `Decommit` does `fallocate`
    punch-hole on `f.file`; base chunks are not backed by `f.file`. Base-range
    decommit must instead `MADV_DONTNEED` the private mapping (drop COW-dirty pages
    back to the shared base) and never punch `f.file`. Eviction/releaser logic must
    treat base pages as droppable/clean.

A5. [needed] Memory accounting. Resident base pages should be accounted as shared
    (counted once per node), not as per-sandbox committed memory; the per-agent
    delta (COW-dirty pages) is what counts per agent. `memAcct` / usage reporting
    must distinguish base vs delta.

A6. [needed] Save (checkpoint). The save path (save_restore.go) must EXCLUDE
    base-range pages from the saved image (they are the shared base, not per-agent
    state) and save only the per-agent delta (COW-dirty / non-base pages).

## B. Restore path (pkg/sentry/pgalloc/save_restore.go + runsc)

B1. [needed] Base/delta split (the big one, S3). Restore must consume (base-file ref
    + memory delta) instead of one monolithic pages file: load ONLY the delta into
    non-base chunks; back the base range via the shared `MAP_PRIVATE` mapping; never
    reload base pages. Today restore reloads every page into a private memfd.
    DESIGN (from F7): record the base-backed set (F5) in memoryFileSaved; SaveTo skips
    it from the packed pages file; LoadFrom opens the MemoryFile with SharedBaseFile
    (S1 mapping) and skips the same set from page loading. The skip MUST be symmetric
    (same walk order both sides) so the packed offsets stay aligned. First piece done:
    F5 delta computation (BaseBackedRanges) is implemented + tested. Next: wire the
    skip into SaveTo (A6) and LoadFrom, threading the base-backed set through
    memoryFileSaved.

B2. [needed] Async page loader integration. The background/lazy restore page loader
    must skip the base range and operate only on the delta.
    HARNESS DONE: TestSaveRestoreRoundTrip drives the REAL SaveTo->LoadFrom path
    end-to-end in-tree (packed pages file via stateio FD writer/reader + stateify
    metadata over a bytes.Buffer; nil timeline; context.Background), restoring 16
    pages incl. a zero page (ExcludeCommittedZeroPages) byte-for-byte. This is the
    verification harness the base-backed-skip changes (A6 save, B1/B2 load) land on.
    FINDING: SaveTo/LoadFrom touch the global usage.MemoryAccounting (nil outside a
    full sentry), so unit tests must set MemoryFileOpts.DisableMemoryAccounting -- a
    reminder that base-vs-delta accounting (A5) lives in that same global path.

B3. [needed] Verify-before-expose on base fault-in (COW-6/6a/6b). A userfaultfd
    MISSING handler over the base region that fetches + Terrapin-verifies the 2 MiB
    block before exposure; known-zero installs via UFFDIO_ZEROPAGE without fetch.
    (Mechanism + ~0.9 ms/2 MiB verify cost proven in density_smoke.c / uffd_latency.)

## C. runsc CLI / control-plane plumbing

C1. [needed] Flags/config to pass the base memory snapshot file and the memory delta
    to `runsc restore` (e.g. --base-image / --memory-delta), plus State Root wiring
    (spec §12: ateapi/atelet/ateom).

C2. [needed] Compatibility gate before mapping a base: MATCH-COMPAT (spec §11.4
    MATRIX-3) -- runtime/arch/platform/page-size/checkpoint-format must match the
    base; fail closed otherwise.

C3. [needed] directfs (GVISOR-5): keep directfs DISABLED for the LLIFS lower until
    the native path preserves verify-before-expose + cache-domain semantics.

## D. Base-file production (checkpoint side)

D1. [proto] Produce the base memory snapshot in MemoryFile-offset (guest-address-
    linear, LLML1) layout -- the file the MAP_PRIVATE base maps. NARROWED by F6: the
    LIVE memfd is ALREADY offset-linear, so base export is a sparse copy of the live
    memfd (MemoryFile.ExportLinearBase, prototyped + tested), NOT a scatter of the
    packed pages.img. Cheap at capture time. To produce a base from an on-disk
    checkpoint instead, LoadFrom into a MemoryFile then ExportLinearBase.

D2. [needed] Base capture point: warmed, pre-secret (RHAZARD-4), with the runtime-
    state blob + memory layout committed by the base descriptor.

## E. RHAZARD (clone-divergence; some are guest-cooperative, not pure gVisor)

E1. [found] Kernel CSPRNG (getrandom, /dev/urandom) and clocks ARE refreshed per
    clone by gVisor -- no change needed (verified, rhz.go).
E2. [found][needed] boot_id is CLONED. Regenerate per clone where workloads key on
    boot/machine identity (gVisor or substrate hook).
E3. [found][needed] Userspace PRNG state is CLONED -- not fixable transparently;
    needs a cooperative reseed hook, a getrandom policy, or restore-fresh (RESET).
    (Mostly guest/substrate policy, not a gVisor patch.)
E4. [found] ASLR layout identical across clones -- residual; cooperative
    re-randomization only. Mitigated by trust/cache-domain boundaries.

## F. Checkpoint format facts (discovered; ground S2/S3)

F1. [found] pages.img is PACKED + SPARSE, not MemoryFile-offset-linear.
    `AsyncPagesFileSave.saveOff` writes committed non-zero pages sequentially;
    `ExcludeCommittedZeroPages` drops zero pages. So a page's pages-file position is
    NOT its MemoryFile offset.
F2. [found] The metadata is gVisor `state.Save(memoryFileSaved{unwaste/unfree sets,
    subreleased, memAcct, chunks})` -- the internal state encoding, NOT an externally
    parseable struct. CONSEQUENCE: S2 (linearize base) and S3 (delta split) must be
    IN-TREE gVisor/runsc features; an external checkpoint-rewriting tool is not
    viable (it would have to reimplement gVisor state decoding).
F3. [found] Restore `LoadFrom` / `AsyncPagesFileLoad` loads ALL committed pages into
    the per-sandbox memfd (awaitLoad over [0,max)). S3 must instead load only the
    delta and back the base via the shared MAP_PRIVATE mapping.
F4. [found][proto] Elegant restore path from S1: map the base region MAP_PRIVATE
    (S1/S1b), then WRITE the delta pages over that mapping -> kernel COWs each
    written page to a private copy; unwritten pages stay shared. No f.file backing
    of the base range is needed for restore. PROVEN in-tree by TestBasePlusDeltaRestore:
    clone A maps the base, writes a delta page over it (COW) and reads base+delta
    (base page 0xAA + delta page 0xCC); the base FILE stays 0xBB; clone B (no delta)
    reads pure base -- merged view + isolation + immutable shared base. (Bookkeeping
    A4/A5/A6 still must treat base-range pages as shared/clean; this proto proves the
    apply MECHANISM only, not the runsc restore plumbing B1/B2 or delta source F5.)
F6. [found] The LIVE memfd is offset-linear: chunk i is mapped from f.file at file
    offset i*chunkSize, so f.file offset O == MemoryFile offset O. The base snapshot
    is therefore just the live memfd's data extents -- export = sparse copy
    (SEEK_DATA/SEEK_HOLE), no scatter transform. (The PACKED pages.img of F1 is only
    the on-disk checkpoint/transport format; the live memfd is linear.) Implemented
    as MemoryFile.ExportLinearBase and verified by TestExportLinearBaseAndShare
    (export a populated MemoryFile, then use the exported file as a SharedBaseFile in
    another MemoryFile and read the content back).

F5. [found][proto] Delta computation. gVisor checkpoints ABSOLUTE state, not
    base-relative. The per-agent memory delta (LLMD1: changed pages vs base) must be
    computed -- either by content-diffing the agent's committed pages against the
    base file at snapshot time, or by base-relative dirty-page tracking added to the
    MemoryFile. PROTOTYPED as content-diff: MemoryFile.BaseBackedRanges(base, baseBytes)
    walks the accounted (memAcct) ranges and returns the coalesced page ranges whose
    contents are byte-identical to the base; the complement (plus committed pages at
    or beyond baseBytes) is the delta. Verified by TestBaseBackedRangesDelta (8 pages
    matching a base, modify 2 after export -> exactly 2 delta / 6 base-backed).
    FINDING: knownCommitted is a lazy flag (set by UpdateUsage at pgalloc.go:1846, not
    at Allocate, which inserts memAcct with knownCommitted=false at :888), so delta
    detection must compare page CONTENT, not trust knownCommitted. Production: the
    comparison should slot into SaveTo's existing per-page scan (which already reads
    each page to test for zero) rather than a separate full re-read, and compare
    against the mmapped base rather than Pread per page.

F7. [found] Save/load symmetry constraint (grounds B1/B2). SaveTo walks f.memAcct
    segments and appends each committed non-zero page to the pages file in WALK
    ORDER via asyncWritePages (saveOff bumps by length) -- the pages file is PACKED,
    position = scan order (confirms F1). The saved metadata (memoryFileSaved) records
    the accounting SETS, not per-page file offsets. CONSEQUENCE: LoadFrom must
    reconstruct the same offset mapping by walking the same committed structure in
    the same order. So a base/delta split must skip the base-backed set SYMMETRICALLY
    on both save (don't write) and load (don't read), and that set must be recorded
    in memoryFileSaved so both sides agree. This is the concrete shape of B1.

Revised S2/S3 (grounded by F1-F5):
- S2 = in-tree "export base in MemoryFile-offset layout": scatter the packed
  pages.img to a sparse linear base file using the in-memory metadata. -> SharedBaseFile.
- S3 save = exclude base-range pages from the packed file AND from memAcct/metadata;
  emit only the delta (F5).
- S3 restore = map base MAP_PRIVATE (S1); load only delta pages, writing them over
  the base mapping (F4); base-aware bookkeeping (A4/A5/A6).

## Prototyped so far
- A1 (shared-base option), A2/S1b (finer sub-chunk mapping), D1/F6 (ExportLinearBase
  base production), and F4 (base+delta apply mechanism): implemented, compile into
  runsc (gvisor3-s1.pgalloc.patch), and verified by in-tree bazel tests
  (sharedbase_test.go), all PASS under `bazel test //pkg/sentry/pgalloc:pgalloc_test`:
  - TestSharedBaseCOW: base-backed reads, shared base across two MemoryFiles,
    private/COW writes leave the base file + the other MemoryFile unchanged.
  - TestExportLinearBaseAndShare: export a populated MemoryFile, then use the
    exported file as a SharedBaseFile and read it back -- the S2->S1 loop.
  - TestBasePlusDeltaRestore: map a shared base, apply a delta over it (COW), read
    base+delta with isolation; base file immutable; a no-delta clone reads pure base
    -- the S3 restore-apply mechanism (F4).
  - TestBaseBackedRangesDelta: of 8 committed pages matching a base, modifying 2
    after export yields exactly 2 delta / 6 base-backed -- the F5 delta computation
    (BaseBackedRanges).
  - TestSaveRestoreRoundTrip: drives the real SaveTo->LoadFrom path (packed pages
    file via stateio + stateify metadata) and restores 16 pages incl. a zero page
    byte-for-byte -- the harness for the A6/B1/B2 base-backed-skip work.
- MECHANISM uncertainty for GVISOR-3 is now retired (map-base + export-base +
  apply-delta-over-base + delta-computation all proven in-tree).
- Remaining is INTEGRATION, not mechanism: B1/B2 (wire the apply path into runsc's
  restore -- LoadFrom skips the base range, loads only the delta), F5/A6 (compute the
  per-agent delta at save time + exclude base pages from the saved image), the
  base-aware MemoryFile bookkeeping (A4/A5 decommit/accounting), and B3
  (verify-before-expose). This integration is what makes REAL runsc clones share
  (flattens the measured 1674 MiB / 3-clone baseline toward ~1 x base + N x delta).
