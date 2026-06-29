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

B2. [needed] Async page loader integration. The background/lazy restore page loader
    must skip the base range and operate only on the delta.

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

D1. [needed] Produce the base memory snapshot in MemoryFile-offset (guest-address-
    linear, LLML1) layout -- the file the MAP_PRIVATE base maps. gVisor's native
    pages.img is a different format, so this is a new artifact.

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
F4. [found] Elegant restore path from S1: map the base region MAP_PRIVATE (S1/S1b),
    then WRITE the delta pages over that mapping -> kernel COWs each written page to
    a private copy; unwritten pages stay shared. No f.file backing of the base range
    is needed for restore. (Bookkeeping A4/A5/A6 still must treat base-range pages
    as shared/clean.)
F5. [found][needed] Delta computation. gVisor checkpoints ABSOLUTE state, not
    base-relative. The per-agent memory delta (LLMD1: changed pages vs base) must be
    computed -- either by content-diffing the agent's committed pages against the
    base file at snapshot time, or by base-relative dirty-page tracking added to the
    MemoryFile. This is new work beyond the existing save path.

Revised S2/S3 (grounded by F1-F5):
- S2 = in-tree "export base in MemoryFile-offset layout": scatter the packed
  pages.img to a sparse linear base file using the in-memory metadata. -> SharedBaseFile.
- S3 save = exclude base-range pages from the packed file AND from memAcct/metadata;
  emit only the delta (F5).
- S3 restore = map base MAP_PRIVATE (S1); load only delta pages, writing them over
  the base mapping (F4); base-aware bookkeeping (A4/A5/A6).

## Prototyped so far
- A1 (shared-base option) and A2/S1b (finer sub-chunk mapping): implemented, compile
  into runsc (gvisor3-s1.pgalloc.patch), and the copy-on-write semantics are verified
  by an in-tree bazel test (sharedbase_test.go -> TestSharedBaseCOW PASS: base-backed
  reads, shared base across two MemoryFiles, private writes leave base file + other
  MemoryFile unchanged). Everything else above is backlog; A6/B1/B3 (save/restore
  base-delta split + verify-before-expose) is the heavy remaining work that makes
  runsc clones actually share.
