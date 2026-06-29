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
- S2: produce a base memory snapshot file (MemoryFile-offset layout) from a
  checkpoint.
- S3: restore base/delta split -- restore loads only the delta, base via the
  shared mapping; fix MemoryFile bookkeeping for base chunks.
- S4: N-clone runsc test measuring the flatten (expect ~1 x base + N x delta,
  vs the measured 1674 MiB baseline).
- S5: Terrapin verify-before-expose on base fault-in (userfaultfd), tying in the
  CAS.

## Status
Mechanism proven, baseline measured, patch point pinned (`extendChunksLocked`,
pgalloc.go ~889/933). S1 onward is the build.
