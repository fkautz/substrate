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

A6. [proto] Save (checkpoint). The save path (save_restore.go) must EXCLUDE
    base-range pages from the saved image (they are the shared base, not per-agent
    state) and save only the per-agent delta (COW-dirty / non-base pages).
    DONE (proto): SaveOpts gains SharedBaseFile + SharedBaseBytes; SaveTo's scan
    threads a third page state `nowBase` (alongside wasCommitted/nowCommitted)
    through updateAddRange/updateNow so base-identical committed pages coalesce
    separately and are sent to recordBaseBacked (no pages-file write) instead of
    asyncWritePages; the base-backed set is stored in memoryFileSaved.baseBacked
    (map[uint64]uint64 start->end, mirrors subreleased; stateify autogen regenerates
    it automatically -- the *_state_autogen.go are bazel-generated, not checked in).
    The base-vs-page compare is folded INTO the existing per-page scan (which already
    reads each page for the zero test), so no extra pass; against base==nil the
    fast-path and behavior are unchanged. Verified by TestSaveWithBaseExcludesDelta:
    16 committed pages, modify 3 after export -> no-base save writes 16 pages,
    with-base save writes 3 (delta) and records 13 base-backed. Production: compare
    against the mmapped base rather than Pread per page; force the scan path when a
    base is set (currently guarded by baseFile==nil on the !ExcludeCommittedZeroPages
    fast path). The standalone BaseBackedRanges (F5) remains the tested reference for
    the same predicate.

## B. Restore path (pkg/sentry/pgalloc/save_restore.go + runsc)

B1. [proto] Base/delta split (the big one, S3). Restore must consume (base-file ref
    + memory delta) instead of one monolithic pages file: load ONLY the delta into
    non-base chunks; back the base range via the shared `MAP_PRIVATE` mapping; never
    reload base pages. Today restore reloads every page into a private memfd.
    DONE (proto, SYNC path): the full save->restore base/delta split works end to end
    through the real SaveTo/LoadFrom. SaveTo records the base-backed set in
    memoryFileSaved.baseBacked and writes only the delta (A6). LoadFrom gains
    SharedBaseFile/SharedBaseBytes: it overlays [0,baseBytes) of its restore mapping
    MAP_PRIVATE from the base (the S1 mmap, applied to LoadFrom's own mapping per F8),
    skips the base-backed set from the page stream (deltaSubRanges = committed minus
    baseBacked, symmetric on both sides per F7), and applies delta by writing through
    the mapping (forEachMappingSlice) -- which COWs the private base range and writes
    through the shared region beyond baseBytes. Verified by TestRestoreOverBase:
    base-backed pages read NON-ZERO base content (so it came from the overlay, not the
    fresh memfd or the delta-only stream), delta pages read delta content, and the
    base file is unmodified (COW isolation). deltaSubRanges with an empty set returns
    the whole segment, so non-base save/restore wire format is byte-identical.
    REMAINING (production): this uses the SYNCHRONOUS pages path (LoadOpts.PagesFile
    nil; base+async is rejected with an error). Productionizing = teach the ASYNC
    page loader (B2) to apply base-range delta through the mapping (COW) instead of
    the memfd FD, since runsc uses the async FD path. See F8.

B2. [proto] Async page loader integration. The background/lazy restore page loader
    must skip the base range and operate only on the delta.
    DONE (proto): the async LoadFrom branch now inserts only deltaSubRanges into the
    loader''s `unloaded` set (skipping base-backed pages, symmetric with save) and
    advances PagesFileOffset by delta lengths only; the sync-only guard is removed so
    base + async PagesFile is supported. KEY CORRECTION TO F8: the FDReader does NOT
    write to the memfd FD -- it embeds NoRegisterClientFD (NeedRegisterDestinationFD
    false, so df is nil) and reads the pages file straight INTO THE MAPPING MEMORY
    via aio.Read/Readv on the iovecs built from forEachMappingSlice(chunk.mapping).
    Since LoadFrom applies the MAP_PRIVATE base overlay BEFORE the loader builds those
    iovecs, the async read COWs the base overlay for base-range delta and writes
    through the shared region beyond it -- no async-loader rewrite was needed. Verified
    by TestRestoreOverBaseAsync (the runsc-style async FD pages file + base overlay:
    base-backed pages from the overlay, delta COW-applied, base file unmodified, pages
    file holds only the 3 delta pages). Packed-stream offsets stay aligned because both
    save and load traverse the same delta bytes in ascending order (F7).
    HARNESS DONE: TestSaveRestoreRoundTrip drives the REAL SaveTo->LoadFrom path
    end-to-end in-tree (packed pages file via stateio FD writer/reader + stateify
    metadata over a bytes.Buffer; nil timeline; context.Background), restoring 16
    pages incl. a zero page (ExcludeCommittedZeroPages) byte-for-byte. This is the
    verification harness the base-backed-skip changes (A6 save, B1/B2 load) land on.
    FINDING: SaveTo/LoadFrom touch the global usage.MemoryAccounting (nil outside a
    full sentry), so unit tests must set MemoryFileOpts.DisableMemoryAccounting -- a
    reminder that base-vs-delta accounting (A5) lives in that same global path.

B3. [proto] Verify-before-expose (COW-6/6a/6b). Terrapin-verify the 2 MiB blocks of
    the shared base before exposure; known-zero installs via UFFDIO_ZEROPAGE without
    fetch (lazy/remote variant). PROTO (benchmarking/verify_share, real terrapin-go
    v0.3): node-level verify-before-expose (re-hash every block vs the per-block GitOID
    manifest) composed with the N-way MAP_PRIVATE flatten + a tamper test. Result:
    verification does NOT change the flatten (8.0x@N=16, 15.6x@N=32, same as the
    integrity-free sec-10 numbers); verify cost is once PER NODE and independent of N
    (35 ms / 64 MiB, ~1.9 GB/s; MVERIFY-2 realized); a flipped byte is REJECTED at its
    block. ARCHITECTURE FINDING: verification is NODE-level (verify the shared base
    once, then per-sandbox MAP_PRIVATE share), NOT a per-sandbox uffd handler -- a
    per-sandbox UFFDIO_COPY would make a PRIVATE page per faulter and defeat sharing.
    So Terrapin verification lives ABOVE gVisor (substrate verifies, hands gVisor a
    trusted base fd; pgalloc just maps it via B1/B2). REMAINING for production: the
    lazy/remote variant -- a uffd MISSING handler over a NODE-SHARED backing that
    fetches each absent block from the CAS, verifies it, and populates the shared
    backing once per node (the sec-8 transport case); plus UFFDIO_ZEROPAGE for
    known-zero. The verify step itself is this proto.

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

F8. [found][CORRECTED] LoadFrom bypasses the extendChunksLocked base overlay. On
    restore, LoadFrom does its OWN single mmap of f.file (MAP_SHARED over the whole
    fileSize, save_restore.go ~967) and assigns chunk.mapping from it -- it does NOT
    call extendChunksLocked, so the load side must apply the [0,baseBytes) MAP_PRIVATE
    base overlay itself (done in LoadFrom, B1). CORRECTION: the original worry that the
    async loader "writes delta to the memfd FD" was WRONG. The pages-file FDReader
    embeds NoRegisterClientFD -> NeedRegisterDestinationFD is false -> the DestinationFile
    is nil and AddRead/AddReadv read the pages file directly INTO THE MAPPING MEMORY
    (aio.Read/Readv over the iovecs from forEachMappingSlice(chunk.mapping)). Because
    the overlay is established before the loader builds those iovecs, async delta reads
    COW the base overlay automatically -- so BOTH the sync and async paths apply
    base-range delta through the mapping, and no async-loader rewrite was required
    (B2 was a small skip-the-base-backed-set change). Pages at/after baseBytes are in
    the MAP_SHARED region and write through to f.file as before.

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
  - TestSaveWithBaseExcludesDelta: save-side A6 -- with a base, SaveTo writes only
    the 3 delta pages (vs 16 without) and records 13 base-backed in metadata.
  - TestRestoreOverBase: load-side B1 -- save against a base (delta-only stream) then
    restore with the base overlaid MAP_PRIVATE; base-backed pages read base content
    (from the overlay), delta pages read delta content (COW over the mapping), base
    file unmodified. The full base/delta save->restore split, end to end (sync path).
  - TestRestoreOverBaseAsync: B2 -- the same, via the runsc-style ASYNC FD pages file;
    the async loader reads delta into the overlaid mapping (COW). Pages file holds only
    the 3 delta pages; base-backed from overlay, delta COW-applied, base unmodified.
  - TestNCloneFlatten: S4 -- N MemoryFiles restore one delta-only checkpoint over one
    shared base via the real LoadFrom; smaps_rollup Rss/Pss shows the flatten. Measured
    4.9x (N=8), 10.1x (N=16), 15.0x (N=32), ~90% of ideal. The flatten realized through
    the real gVisor code path.
- MECHANISM uncertainty for GVISOR-3 is now retired (map-base + export-base +
  apply-delta-over-base + delta-computation all proven in-tree).
- Remaining is INTEGRATION, not mechanism: B1/B2 (wire the apply path into runsc's
  restore -- LoadFrom skips the base range, loads only the delta), F5/A6 (compute the
  per-agent delta at save time + exclude base pages from the saved image), the
  base-aware MemoryFile bookkeeping (A4/A5 decommit/accounting), and B3
  (verify-before-expose). This integration is what makes REAL runsc clones share
  (flattens the measured 1674 MiB / 3-clone baseline toward ~1 x base + N x delta).
