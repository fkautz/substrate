# GVISOR-3: shared copy-on-write base memory in gVisor -- implementation design

Goal: when N sandboxes restore from one base, the base guest-memory pages are
PHYSICALLY shared (one resident copy) with copy-on-write, instead of each sentry
materializing its own full copy.

## Proven and measured already
- Mechanism (kernel): `benchmarking/density_smoke.c` -- N processes share one base
  MAP_PRIVATE; resident pages shared (pss ~ base/N), writes go COW-private,
  userfaultfd populates absent from base (never zero), known-zero installs without
  fetch.
- Baseline (runsc today, no sharing): 3 hsrv clones from one checkpoint =
  3 x ~557 MiB = 1674 MiB; 3 cr_workload clones = ~3 x 1 GiB physical (free(1)).
- Flatten target (from the smoke-test numbers): N clones over a B-byte base =
  ~1 x B (shared) + sum of per-clone deltas. E.g. 8 clones over a 1 GiB base with
  16 MiB dirty each ~= 1 GiB + 128 MiB instead of 8 GiB.

## Current behavior (the thing to change)
`pkg/sentry/pgalloc/pgalloc.go`:
- `MemoryFile` backs its guest memory in a single per-sandbox file (`f.file`, a
  memfd), grown in `extendChunksLocked` (~line 889) and mmapped in ONE
  `MAP_SHARED` mapping (~line 933). Per-chunk `chunkInfo.mapping` points into it.
- Restore (`save_restore.go`) loads ALL pages into that per-sandbox file (mmap at
  ~line 969 is also `MAP_SHARED`); the async page loader faults pages from the
  pages file into the private memfd. Result: every restore is a full copy.
- No sharing/dedup/COW-base flag exists; `runsc restore` only has `-direct` /
  `-fs-restore-direct` (O_DIRECT for reading the pages file).

## The change
Add a base-backed chunk mode to `MemoryFile`:
1. `MemoryFileOpts.SharedBaseFile *os.File` + `SharedBaseBytes uint64`: a
   read-only, pre-populated base file (the LLIFS base memory snapshot in
   MemoryFile-offset layout) covering the chunk range `[0, SharedBaseBytes)`.
2. In `extendChunksLocked`, for a new chunk whose file offset is within
   `[0, SharedBaseBytes)` and `SharedBaseFile != nil`, map it
   `MAP_PRIVATE` from `SharedBaseFile` at the chunk offset (instead of the shared
   `f.file` mmap). Delta chunks (>= SharedBaseBytes) stay as today. This is the
   exact primitive `density_smoke.c` proves: shared resident pages, COW on write.

## Why this is multi-session, not a one-line patch
The chunk mmap is the easy part. Correctness needs four more things:
1. RESTORE BASE/DELTA SPLIT. Restore today reloads every page. With a shared base
   it must load ONLY the per-agent delta pages and leave base pages to the
   MAP_PRIVATE base mapping. That requires the LLIFS State Root delta model
   (memory delta = changed pages over the base) in the runsc restore path -- i.e.
   restore consumes (base file + memory delta), not one monolithic pages file.
2. BASE FILE PRODUCTION. The shared base must be in MemoryFile-offset layout
   (guest-address-linear, LLML1), produced once from a warmed snapshot and shared
   read-only across sentries. gVisor's native checkpoint pages file is a different
   format, so this is a new artifact (the base memory snapshot object).
3. MEMORYFILE BOOKKEEPING. Decommit (`fallocate` punch on `f.file`), accounting,
   eviction, and save/restore all assume `f.file` backs every offset. Base chunks
   are backed by a different read-only file and must be treated as
   shared/read-only (no decommit to f.file, correct accounting as shared, excluded
   from re-save). This touches several `MemoryFile` paths.
4. VERIFY-BEFORE-EXPOSE. Base fault-in must Terrapin-verify the 2 MiB block before
   the page is exposed (userfaultfd MISSING handler over the base mapping), per
   COW-6/6a; known-zero via UFFDIO_ZEROPAGE.

## Staged plan
- S1 (small): add the `MemoryFileOpts.SharedBaseFile` option + the base-chunk
  MAP_PRIVATE mapping in `extendChunksLocked`; compile into runsc; unit-test the
  COW semantics of a base-backed chunk (reads see base bytes; a write is private
  and does not modify the base file).
  STATUS: S1 + S1b DONE (patch in `gvisor3-s1.pgalloc.patch`). Option added;
  base-range chunks overlaid `MAP_PRIVATE|MAP_FIXED` from the shared base file at
  SUB-CHUNK granularity (the [0,SharedBaseBytes) portion of each 1 GiB chunk, so
  sub-GiB bases share); gofmt-clean; compiles into runsc. COW semantics verified by
  an in-tree bazel test (`sharedbase_test.go` -> TestSharedBaseCOW PASS with a 4 MiB
  base): base-backed reads, shared base across two MemoryFiles, and private writes
  that leave the base file and the other MemoryFile unchanged. (Also proven at the
  kernel level by `density_smoke.c`.)
  FINDING (granularity): gVisor's `chunkSize` is 1 GiB (`chunkShift=30`), NOT the
  LLIFS 2 MiB block. S1b handles sub-GiB bases by overlaying the sub-range; for
  production, a first-class base-region map aligned to LLIFS 2 MiB blocks (decoupled
  from the 1 GiB chunk allocator) is still wanted so the base can fault/verify per
  block. (This is the cheapest place the design changed once implemented.)
- S2: produce a base memory snapshot file (MemoryFile-offset layout). DONE (proto):
  the LIVE memfd is already offset-linear (F6), so base export is a sparse copy of
  the memfd's data extents -- implemented as `MemoryFile.ExportLinearBase` and
  verified by `TestExportLinearBaseAndShare` (export a populated MemoryFile, then use
  the exported file as a SharedBaseFile in another MemoryFile and read it back: the
  S2->S1 loop). No scatter transform needed (the packed pages.img of F1 is only the
  on-disk checkpoint format). Production: capture at checkpoint time, or LoadFrom an
  on-disk checkpoint then ExportLinearBase. (See ledger D1, F6.)
- S3: restore base/delta split -- restore loads only the delta, base via the
  shared mapping; fix MemoryFile bookkeeping for base chunks. GROUNDED: restore
  today loads ALL committed pages (F3); the S1 mapping gives an elegant path --
  map base MAP_PRIVATE then WRITE delta pages over it (kernel COWs each) (F4); the
  delta must be computed (diff-vs-base or dirty-tracking) since gVisor checkpoints
  absolute state (F5). (See ledger F1-F5.)
  STATUS: S3 restore-APPLY MECHANISM DONE (proto): TestBasePlusDeltaRestore proves
  the F4 path in-tree -- clone A maps the shared base, applies a delta (writes
  page1 over the base mapping -> COW) and reads base+delta (page0 base 0xAA,
  page1 delta 0xCC); the base FILE stays 0xBB; clone B (no delta) reads pure base.
  So map-base-then-write-delta-over-it produces the correct merged view with
  isolation and an immutable shared base. REMAINING (production, not mechanism):
  wire this into runsc's restore flow (LoadFrom must SKIP the base range and load
  only delta pages -- B1/B2/F3), compute the delta at save time (F5/A6), and make
  MemoryFile bookkeeping base-aware for decommit/accounting (A4/A5). The mechanism
  uncertainty is now retired; what is left is integration/plumbing.
  PROGRESS (B1/B2): the page-file is PACKED in memAcct-walk order (F7), so the
  base/delta split is a SYMMETRIC skip of a base-backed set on both save and load,
  recorded in memoryFileSaved. First piece done: F5 delta computation --
  MemoryFile.BaseBackedRanges(base, baseBytes) returns the base-identical accounted
  pages (the complement is the delta), verified by TestBaseBackedRangesDelta. The
  measured read-mostly delta is ~1.7% of base (sec 9 of density-prototype-findings),
  so the split is worth wiring. Second piece done: an in-tree SaveTo->LoadFrom
  round-trip harness (TestSaveRestoreRoundTrip) that exercises the real packed
  pages-file + stateify-metadata path, so the base-backed-skip changes can be
  verified as a unit (pages file = stateio FD writer/reader over a temp file;
  metadata = a bytes.Buffer; timeline nil; DisableMemoryAccounting to avoid the
  sentry-global usage accounting). Third piece done: SAVE-side A6 -- SaveOpts gains
  SharedBaseFile/SharedBaseBytes, SaveTo's scan threads a `nowBase` page state so
  base-identical committed pages are excluded from the pages file and recorded in
  memoryFileSaved.baseBacked (stateify autogen regenerates automatically), verified
  by TestSaveWithBaseExcludesDelta (16 pages, 3 delta -> with-base save writes 3,
  records 13 base-backed). Fourth piece done: the LOAD side. LoadFrom gains
  SharedBaseFile/SharedBaseBytes; it overlays [0,baseBytes) of its own restore
  mapping MAP_PRIVATE from the base (per F8, LoadFrom maps its own memfd), skips the
  base-backed set from the page stream (deltaSubRanges, symmetric with save), and
  applies delta through the mapping (COW for the private base range; write-through
  beyond baseBytes). Verified by TestRestoreOverBase: the full save->restore base/
  delta split end to end -- base-backed pages read base content from the overlay,
  delta pages read delta content, base file unmodified. Fifth piece done: the ASYNC
  path (B2), which is what runsc uses. The worry that the async loader writes to the
  memfd FD (F8) was WRONG: the pages-file FDReader reads straight into the mapping
  memory (iovecs from forEachMappingSlice), so with the overlay in place the async
  delta reads COW the base automatically. B2 was just making the async branch skip the
  base-backed set (deltaSubRanges) and dropping the sync-only guard. Verified by
  TestRestoreOverBaseAsync. So the GVISOR-3 base/delta mechanism is now PROVEN on both
  the sync and async (production) pgalloc paths. Remaining before a runsc-observable
  flatten: base-aware bookkeeping (A4/A5 decommit/accounting), runsc CLI plumbing to
  pass the base image + State Root (C1), and then S4 (the N-clone flatten measurement
  vs the 1674 MiB baseline); B3 verify-before-expose layers on after.
- S4: N-clone runsc test measuring the flatten (expect ~1 x base + N x delta,
  vs the measured 1674 MiB baseline).
- S5: Terrapin verify-before-expose on base fault-in (userfaultfd), tying in the
  CAS.

## Status
Mechanism proven, baseline measured, patch point pinned. S1 + S1b + S2 DONE, and the
S3 restore-APPLY mechanism is now proven in-tree too. All three building blocks --
shared-base MemoryFile option (finer sub-chunk mapping), base export
(ExportLinearBase), and base+delta apply (map base + write delta over it -> COW) --
are implemented, compile into runsc, and pass in-tree bazel tests
(`gvisor3-s1.pgalloc.patch`, `sharedbase_test.go`: TestSharedBaseCOW +
TestExportLinearBaseAndShare + TestBasePlusDeltaRestore). The GVISOR-3 MECHANISM
uncertainty is retired. What remains is INTEGRATION, not mechanism: wire the apply
path into runsc's restore (LoadFrom skips the base range, loads only the delta --
B1/B2), compute the per-agent delta at save time (F5/A6), and make MemoryFile
bookkeeping base-aware for decommit/accounting (A4/A5). Then S4 (N-clone runsc
flatten measurement, expect ~1 x base + N x delta vs the measured 1674 MiB baseline)
and S5 (Terrapin verify-before-expose on base fault-in). Full gVisor production
backlog tracked in `gvisor-changes-for-production.md`.
