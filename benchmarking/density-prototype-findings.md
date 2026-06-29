# Density-gate prototype: findings

Environment: Lima VM `llifs`, Ubuntu, kernel 6.8.0 aarch64, 4 vCPU / 8 GiB.
Platform: gVisor `systrap` (no `/dev/kvm` under Apple vz; nested KVM unavailable).
Unprivileged userfaultfd enabled. Page size 4096 B.

## 1. Density primitives: PASS (`benchmarking/density_smoke.c`)

A standalone C test (no gVisor/Terrapin/CAS) proving the rung-3 kernel mechanism.
256 MiB base, N = 8 processes, each `MAP_PRIVATE` over one `memfd` base:

```
non-writer x7   rss=256 MiB   pss= 34 MiB   private_dirty=  0 MiB
writer (wrote half)  rss=256 MiB   pss=144 MiB   private_dirty=128 MiB
userfaultfd: data pages faulted=32 -> populated from base (0xC3), never zero
userfaultfd: zero pages faulted=32 -> UFFDIO_ZEROPAGE, no base fetch
RESULT: PASS
```

Maps 1:1 to the §16.1 acceptance test:
- resident base pages are PHYSICALLY shared across N (`rss=base`, `pss~=base/N`);
- a write creates a private page for the writer ONLY (`private_dirty=128 MiB` on the
  writer, `0` on all others -- no bleed);
- an ABSENT range never reads as zero: first touch faults and is populated from the
  verified base (`UFFDIO_COPY`);
- a KNOWN-ZERO range installs a verified zero page (`UFFDIO_ZEROPAGE`) with no fetch.

Conclusion: the LLIFS density mechanism is physically achievable on this kernel. The
remaining gVisor work is to route `MemoryFile` through this primitive, not to invent
a mechanism.

## 2. runsc runs here: PASS

`sudo runsc --platform=systrap --network=none do echo ...` runs a sandbox and returns
output. Prebuilt runsc (release-20260608.0) is sufficient for rungs 1-2 (verified
lazy filesystem and lazy restore page delivery); the source build is needed only to
apply the GVISOR-3 patch.

## 3. GVISOR-3 patch point (pkg/sentry/pgalloc/pgalloc.go)

`MemoryFile` is a `memmap.File` backed by one `*os.File` (`NewMemoryFile`, ~line 423)
whose chunks are mmapped `MAP_SHARED` (~lines 499, 933); pages are allocated per
sandbox via `Allocate` (~line 649). The restore path already supports an async "pages
file" (`save_restore.go` ~line 118), which is the GVISOR-2 reuse point.

The shared-copy-on-write base (GVISOR-3) patch:
- introduce a base-backed region whose chunks are mmapped `MAP_PRIVATE` over a SHARED,
  externally-populated, verified base fd (the LLIFS CAS-provided base memory snapshot),
  so resident base pages are physically shared across sandboxes and writes fault to
  private copies (the exact behavior proven in section 1);
- keep per-agent dirty pages on the normal per-sandbox backing;
- on a not-yet-resident base range, populate the shared backing via userfaultfd
  MISSING (verify-before-expose), never a private or zero page (COW-6/6a/6b).

The gap is pluggability: `NewMemoryFile` takes one file and maps `MAP_SHARED`; there
is no hook to back a sub-range from a shared base fd `MAP_PRIVATE`. That hook is the
net-new capability the spec calls GVISOR-3.

## 4. Real-workload checkpoint/restore + baseline density gap (`cr_workload.c`)

A static "warmed" workload (1 GiB heap filled with a checksummed pattern, a tick
counter incremented every second) run under runsc as an OCI bundle, then
`runsc checkpoint` / `runsc restore`:

```
wl1 before checkpoint:  tick=8  checksum=ok
checkpoint:             0.84 s, image 1.1 GiB
wl1r after restore:     tick=9, 10, 11 ... checksum=ok   <- CONTINUES, not reset
```

Proves a real, non-cooperating, stateful GiB-scale workload checkpoint/restores
correctly: live memory AND execution state survive (the counter resumes from the
checkpoint value, the 1 GiB pattern still verifies). This validates the
runtime-state / delta concept on gVisor's native C/R.

Baseline density gap (the "before" that GVISOR-3 flattens): three clones restored
from the SAME 1.1 GiB checkpoint each run independently (checksum=ok) and each
materializes its OWN full copy:

```
3 sandboxes: sentry rss 1064 + 1066 + 1065 MiB = 3196 MiB total (~N x base)
```

So today N restores cost ~N x base RAM with no sharing. The section-1 smoke test
shows the achievable end state (8 sharers over a 256 MiB base => pss ~ base/8, i.e.
~1 x base). The GVISOR-3 MemoryFile patch is exactly the bridge between these two
measured endpoints. (One checkpoint -> N independent restored clones also
demonstrates the fan-out shape used by fork, §6.3.)

## 5. HTTP-server clone demo (`hsrv.go`)

A static Go HTTP workload (512 MiB warm heap + checksum; `/healthz` reports an
in-memory request counter, checksum, uptime, pid) run under runsc, then
checkpointed and restored into multiple clones. Health checks are issued by an
in-sandbox client over netstack loopback (`runsc exec <id> /hsrv check`), which
avoids host CNI plumbing while still exercising a real TCP/HTTP exchange and the
network-state restore path.

```
c1 health x3:        reqs=1, reqs=2, reqs=3   checksum=ok   (stateful server)
checkpoint:          0.39 s, 515 MiB
3 clones from 1 checkpoint, health-checked:
  round 1:  h_a reqs=4  h_b reqs=4  h_c reqs=4   checksum=ok  uptime=11s
  round 2:  h_a reqs=5  h_b reqs=5  h_c reqs=5
memory:    3 clones x ~557 MiB sentry rss = 1674 MiB total (~N x base)
```

Proves: (1) the server listens and answers HTTP health checks; (2) restore
preserves live state -- the request counter CONTINUES from the checkpointed value
(3 -> 4), not reset, and the 512 MiB heap still verifies; (3) clones are
INDEPENDENT (each counter increments separately, 4 -> 5); (4) the same per-clone
memory baseline holds for a real networked Go service. Networking note: the
host->netstack ingress path needs CNI-style veth ownership (gVisor netstack must
own the interface IP, or the in-netns kernel RSTs the connection); deferred as
orthogonal to the C/R density work -- in-sandbox loopback was used instead.

## 6. RHAZARD clone-divergence (`rhz.go`)

Checkpoint one instance, restore 3 clones, probe FRESH samples from each source.
A field IDENTICAL across clones is shared frozen state (a hazard); DIFFERENT means
gVisor already refreshes it.

```
source                 across clones    verdict
getrandom (kernel)     all different    SAFE  (gVisor gives fresh kernel entropy)
/dev/urandom           all different    SAFE
monotonic + wall clock advance normally SAFE  (not frozen/zeroed)
userspace PRNG         IDENTICAL        HAZARD (math/rand state cloned -> same seq)
boot_id                IDENTICAL        HAZARD (cloned boot identity)
ASLR / heap address    IDENTICAL        WEAKENING (same layout in every clone)
/proc/.../uuid         ERR              gVisor unimplemented (minor)
```

Conclusion: the fan-out-clone model is entropy-safe for KERNEL randomness and
clocks (gVisor refreshes getrandom / urandom; clocks advance), but userspace PRNG
state, boot_id, and ASLR are CLONED. The substrate cannot transparently fix a
userspace PRNG or re-randomize a running process without cooperation, so the
RHAZARD policy is: kernel-CSPRNG randomness is safe to clone; userspace
PRNG/boot_id require a reseed hook (RHAZARD-1, cooperative) OR a policy that
RNG-sensitive agents use getrandom OR restore-fresh (RESET) instead of clone; ASLR
uniformity across a clone set is a residual mitigated only by trust/cache-domain
boundaries (RHAZARD-7). This sharpens the non-cooperative tenet: clone-safety is
conditional on the randomness source.

## 7. Cold-fault verify-before-expose latency (`uffd_latency.c` + sha256 bench)

Measures the cost the lazy-fault thesis depends on: hashing a 2 MiB block on first
touch before exposing it. On this VM (aarch64, vz):

```
verify  (GitOID G = sha256 over 2 MiB)     ~0.90 ms/block   2.33 GB/s  (ARMv8 SHA;
                                                             openssl cross-check 2.33 GB/s)
populate floor (userfaultfd UFFDIO_COPY     ~1.18 ms/block   1.76 GB/s
  2 MiB from local base, p99 1.80 ms)
cold fault + verify (serial, local base)   ~2.1 ms/block
```

Findings:
- Verifying is CHEAPER than faulting (2.33 vs 1.76 GB/s) -- verify-before-expose is
  not the bottleneck; the page-fault populate dominates. Verify adds ~0.9 ms on top
  of an unavoidable ~1.2 ms populate on the COLD path (~75%).
- Amortized once per block per node (MVERIFY-2): the Nth clone over an
  already-verified-resident base pays ~0 for shared base blocks; only its own
  unique pages fault+verify. Full 1 GiB base verify = ~0.43 s once per node.
- A ~100 MiB resume working set ~= 50 blocks ~= ~105 ms added to TTR on the first
  cold restore; near-zero on subsequent clones.
- Conclusion: "verify every exposed byte" holds at acceptable cost and does not
  scale with agent count; the dominant cold-start cost is transport (remote fetch),
  which §8 hedging/prefetch/packs target, not hashing.
- Caveats: local base (remote fetch adds network latency, orthogonal to verify,
  hedged per §8); single-threaded handler (parallelizable across vCPUs); 2.33 GB/s
  is this vz-aarch64 VM.

## 9. Pre-integration benchmarks: the N*delta term (memfd-diff harness)

Before sinking days into the B1/B2 restore-path rewrite, we measured the two
unmeasured terms in the post-GVISOR-3 cost model `1*base + N*delta`: the per-agent
DELTA (how much a running clone diverges from its base) and that base sharing holds
at production scale. Method needs NO gVisor rebuild: the live `runsc-memory` memfd is
offset-linear (F6), so snapshotting `/proc/<sentry>/fd/<memfd>` at two points and
page-diffing them measures the divergence directly. Tools: `memsnap.go` (sparse
SEEK_DATA/SEEK_HOLE copy of the sentry memfd) + `pagediff.go` (4 KiB page diff +
region histogram); workload `hsrv.go` (512 MiB warm Go HTTP service) with added
`load <N>` (single-process N requests, so transient guest clients do not confound)
and `dirty <MiB>` (writes a KNOWN number of pages for ground-truth validation).

Setup: hsrv warmed under runsc (systrap), base committed guest memory = 514 MiB
(512 MiB warm heap + ~2 MiB Go runtime). Sentry memfd = fd 8 `/memfd:runsc-memory`.

### 9a. Read-mostly delta (the headline)
Drove 5000 real `/healthz` requests from one client process, then diffed:
```
READ-MOSTLY DELTA (5000 req):  2205 / 262144 pages  =  8.6 MiB changed  (1.7% of base)
  region  0   (0-64 MiB)    507 pages   Go runtime / early heap
  region  8 (512-576 MiB)  1472 pages   heap growth just past the warm buffer
  region 15 (960 MiB)       226 pages   runtime stacks
```
The 512 MiB warm working set (regions 1-7, 64-512 MiB) shows ZERO changed pages: a
large read-only working set is fully shareable. Serving 5000 requests dirtied ~8.6
MiB -- the per-clone delta is working-set-bound, not base-bound.

### 9b. Harness validation against ground truth
Asked the server to dirty a known 100 MiB (25600 pages), then diffed:
```
VALIDATION DELTA:  25707 / 262144 pages  =  100.4 MiB  (ground truth 100 MiB)
```
25707 vs 25600 expected (the extra ~107 is concurrent runtime churn) -- the harness
measures real divergence to within 0.4%. The dirtied pages landed in regions 0-1
(start of the warm heap), exactly where the writes went. Total mem0->mem2 = 108 MiB
= 8.6 (read) + 100 (dirty), consistent. The measurement is trustworthy.

### 9c. Base sharing at production scale (density_smoke @ 1 GiB, N=64)
```
1 GiB base, N=64 MAP_PRIVATE sharers:
  each non-writer:  rss=1024 MiB   pss= 16 MiB (= base/64)   private_dirty=0
  writer (half):    rss=1024 MiB   pss=520 MiB               private_dirty=512 MiB
```
`pss = base/N` holds cleanly at a 1 GiB base across 64 sharers (also checked N=8 ->
137 MiB = 1024/8). The "1*base" term is real at scale; resident base pages are
physically shared, writes stay private.

### 9d. ExportLinearBase (base capture) throughput
`memsnap` is the `ExportLinearBase` operation (sparse memfd copy). 522 MiB committed
copied in 0.14 s = ~3.7 GB/s; a 1 GiB base exports in ~0.27 s. Negligible on the
capture path.

### 9e. The flatten, with measured numbers
Guest-memory plane (what GVISOR-3 directly shares), read-mostly delta 8.6 MiB,
base 514 MiB:
```
                         N=64 clones
  before (today):  N * 514 MiB           = 32,900 MiB  (~32 GiB)
  after GVISOR-3:  514 + N * 8.6 MiB      =  1,064 MiB  (~1 GiB)   -> ~31x flatter
```
Node-level density (8 GiB node). The prior baseline measured ~557 MiB sentry RSS per
clone (sec 5), of which ~514 is the shareable guest plane and ~43 MiB is per-sentry
process memory (Go heap, netstack) that GVISOR-3 does NOT share:
```
  before:  floor(8192 / 557)                 =  14 clones / node
  after:   floor((8192 - 514) / (43 + 8.6))  = 148 clones / node   -> ~10x density
```
So GVISOR-3 flattens the guest-memory plane ~31x (this workload) and lifts node
density ~10x; the per-sentry ~43 MiB process overhead becomes the NEW density floor
and the next optimization target. Density is now delta-bound, not base-bound -- the
core thesis, measured.

### 9f. Caveats
- Delta is workload- and runtime-dependent: 8.6 MiB is a read-mostly Go service over
  5000 requests. A write-heavy or large-allocating agent has a bigger delta (the
  100 MiB dirty shows the harness tracks it linearly); density scales as
  (RAM - base)/delta, so a 50 MiB delta still gives ~150 clones/node here.
- Per-clone delta also includes gVisor's per-restore refreshed state (kernel entropy,
  clocks; RHAZARD sec 6) -- small, included in the measured 8.6 MiB.
- Measured on aarch64/vz; the ~43 MiB sentry-own floor is platform/build-dependent.
- This measures divergence of a running instance from its start image == per-clone
  delta from a shared base (all clones start identical, diverge independently).

GO/NO-GO: the N*delta term is small and base sharing holds at scale -> the B1/B2
restore-path integration is justified. Proceed.

## 10. S4: N-clone flatten through the REAL pgalloc save/restore path

After building the full base/delta split into gVisor's pgalloc (A6 save exclusion +
B1 load overlay/skip + B2 async loader; see gvisor3-shared-base-design.md), measure
the actual flatten through that code path. `benchmarking/flatten_test.go`
(TestNCloneFlatten, in-tree pgalloc test): build one warmed base, ExportLinearBase it,
save a delta-only checkpoint against it, then restore N MemoryFiles over the ONE
shared base via the real LoadFrom and read /proc/self/smaps_rollup. One measurement
gives both footprints: Rss counts the base once PER MAPPING (the would-be no-sharing
cost ~N*(base+delta)) while Pss counts it once PHYSICALLY (the shared cost
~base + N*delta), so Rss/Pss is the flatten.

```
config                       measured   ideal    Pss(actual)  Rss(no-share)  Priv_Dirty
N=8,  base=128MiB, delta=8     4.9x      5.7x      215 MiB      1057 MiB        93 MiB
N=16, base=128MiB, delta=4    10.1x     11.0x      206 MiB      2068 MiB        80 MiB
N=32, base=64MiB,  delta=2    15.0x     16.5x      137 MiB      2061 MiB        73 MiB
```

Findings:
- The base is PHYSICALLY shared through the real restore path: Pss is far below Rss
  (e.g. 206 vs 2068 MiB at N=16). If the base were not shared, Pss would equal Rss.
  Pss vs Rss is the authoritative proof; the smaps Shared_Clean bucket reads ~0-4 MiB
  (a classification quirk for MAP_PRIVATE file pages), but Pss accounts the shared
  base once regardless.
- Measured flatten is consistently ~90% of the ideal N*(base+delta)/(base+N*delta);
  the gap is a fixed ~15-30 MiB of Go runtime + page tables for the N mappings, which
  amortizes as base grows. Private_Dirty tracks ~N*delta (the per-clone COW pages).
- Flatten scales with N and inversely with delta -- density is DELTA-bound, not
  base-bound, confirmed end to end through the gVisor code path (not just the raw
  kernel primitive of section 1).
- Cross-check with the real-workload delta (section 9, ~8.6 MiB read-mostly delta over
  a 514 MiB base): at N=32 that is an ideal ~21x flatten; this synthetic run confirms
  the mechanism realizes ~90% of such ratios.

This is S4 at the pgalloc layer (where the sharing physically happens), using the real
SaveTo/LoadFrom base/delta code. A runsc-CLI-observable run (threading the base image
through controller.go/kernel_restore.go + the restore CLI, C1) is the remaining
integration, deferred as orthogonal to proving the flatten.

## 11. B3: Terrapin verify-before-expose composed with the flatten

Section 10 measured the flatten WITHOUT integrity (a trusted local base). B3 adds
Terrapin verify-before-expose and shows the two hold TOGETHER.
`benchmarking/verify_share/` (Go, uses the REAL terrapin-go v0.3, profile
llifs-terrapin-sha256-v2 via a local replace):

1. Build a base of 2 MiB blocks; compute the per-block GitOID manifest and the
   whole-base identifier with terrapin.Identifier / IdentifierFromReader.
2. VERIFY-BEFORE-EXPOSE (node level, once): re-hash every block and check it against
   the manifest BEFORE any sandbox maps the base.
3. SHARE: N sandboxes MAP_PRIVATE the verified base; smaps Rss/Pss = the flatten.
4. TAMPER: flip one byte and re-verify -> the block''s GitOID mismatches and is
   REJECTED, never exposed.

```
config                         flatten   Pss     verify (once/node)   tamper
N=16, base=64MiB, delta=4       8.0x     129MiB  35ms (1.90 GB/s)     rejected @ block
N=32, base=64MiB, delta=2      15.6x     132MiB  35ms (same, N-indep) rejected @ block
base terrapin id: terrapin-sha256:a8c4f0ea24fac860f1e4d88b18cd32c4226a01f03b515cd50350c808949c96ac
```

Findings:
- Verify-before-expose does NOT change the flatten: 8.0x / 15.6x match the
  integrity-free section-10 numbers at the same configs. Verification gates whether
  the shared base is TRUSTED; it does not add resident pages.
- Verify cost is once PER NODE and independent of N (same 35 ms for a 64 MiB base at
  N=16 and N=32) -- the MVERIFY-2 amortization, realized. ~1.9 GB/s including file
  reads + the full terrapin identifier (vs the ~2.33 GB/s bare-sha256 microbench of
  sec 7; the gap is the tree/manifest construction + I/O). A 1 GiB base verifies in
  ~0.5 s once per node.
- Tampering is caught: a single flipped byte changes the block''s GitOID, so the
  block fails verification and is not exposed. Integrity holds.
- ARCHITECTURE: verification sits at the NODE level (verify the shared base once,
  before sandboxes map it), NOT as a per-sandbox userfaultfd handler. A per-sandbox
  uffd populate would UFFDIO_COPY a PRIVATE page per faulter and defeat sharing; the
  spec''s "amortized once per block per node" (MVERIFY-2) is exactly node-level
  verify + per-sandbox MAP_PRIVATE share. So Terrapin verification belongs ABOVE
  gVisor (the substrate verifies, then hands gVisor a trusted base fd, which pgalloc
  maps via the B1/B2 overlay). The lazy/remote variant (uffd over a node-shared
  backing, verifying each block as it is fetched from the CAS, once per node) is the
  section-8 transport case and reuses this same verify step.

NOTE: verify_share/go.mod uses a local `replace` to /Users/fkautz/src/terrapin-go
(branch terrapin-v0.3); build it where terrapin-go is checked out (the llifs VM / host).

## 12. B3 lazy/remote: userfaultfd CAS-fetch + Terrapin-verify, once per node

Section 11 verified the base EAGERLY (re-hash all blocks before mapping). The
lazy/remote variant is the transport case (spec COW-6/6a + sec 8): blocks are
fetched from a content-addressed store ON DEMAND, verified, and installed into a
node-shared backing only when first accessed. `benchmarking/lazy_verify/` (Go, real
terrapin-go v0.3, hand-rolled userfaultfd; Linux/uffd):

- A memfd is the NODE-SHARED base backing, mapped MAP_SHARED and registered with a
  userfaultfd in MISSING mode.
- A handler goroutine serves MISSING faults: map the faulting 2 MiB block to its
  Terrapin GitOID, fetch the bytes from the CAS, recompute the GitOID
  (verify-before-expose), and UFFDIO_COPY into the shared backing -- or UFFDIO_ZEROPAGE
  for known-zero blocks (no fetch). A tampered CAS entry (content != its GitOID key)
  is detected and NOT exposed.
- After population, N sandboxes MAP_PRIVATE the shared backing -> the flatten.

```
base=32MiB, 16 blocks (1 known-zero, 1 tampered):
  fetches=15  verifies=15  zero-installs=1  rejects=1
  once-per-node: re-touching every block added 0 fetches
  tamper:    block 8 (corrupted in CAS) REJECTED by verify, not exposed
  known-zero: block 4 installed via UFFDIO_ZEROPAGE, no fetch
  share N=16:  Rss=575MiB  Pss=63MiB   FLATTEN= 9.1x
  share N=32:  Rss=1087MiB Pss=63MiB   FLATTEN=17.3x
```

Findings:
- Lazy fault-driven fetch works through userfaultfd: a block is fetched + verified
  only on first access. Re-touching every block adds ZERO fetches -> population is
  ONCE PER NODE; the fetch/verify count (15) is independent of the sandbox count N.
  This is the MVERIFY-2 amortization in the transport path.
- Verify-before-expose holds in the fault handler: the fetched bytes are Terrapin-
  verified BEFORE UFFDIO_COPY exposes them. A single corrupted byte changes the
  block''s GitOID, so the block is rejected and never installed.
- Known-zero blocks install via UFFDIO_ZEROPAGE with no CAS fetch (zero-skip).
- It composes with the flatten: Pss stays ~constant (the base resident once per node)
  while Rss grows with N, so the flatten scales (9.1x@N=16, 17.3x@N=32) -- exactly the
  node-level model (verify+populate once, MAP_PRIVATE share N).
- This is the same architecture as sec 11 (node-level verify, then share); the only
  difference is WHEN/HOW the shared backing is populated (lazy uffd from a CAS vs
  eager full-base verify). The CAS here is in-process (freed before the rollup, since
  it stands in for a remote store); a real deployment fetches from the §8 CAS/packs.

NOTE: lazy_verify/go.mod uses a local `replace` to /Users/fkautz/src/terrapin-go
(branch terrapin-v0.3); uffd ioctl numbers and syscall nrs are for linux/arm64.

## 8. Status / next

- [x] Lima VM, kernel/userfaultfd verified
- [x] density primitives proven (section 1)
- [x] runsc sandbox runs (systrap)
- [x] gVisor cloned (`~/gvisor`), patch point located
- [x] build runsc from source (bazel 8.3.1 via bazelisk) -- 2489 actions, ~5 min;
      artifact `~/gvisor/bazel-bin/runsc/runsc_/runsc` (runsc version 928199eb9fb2)
      runs a sandbox (systrap). Dev loop ready: edit pgalloc -> bazel build
      //runsc:runsc -> test.

### Native build prerequisites (what gVisor's build container normally provides)

Building `//runsc:runsc` natively on this aarch64 VM (no Docker) needs, beyond
`gcc`/`go`/`git`/`make`:

```
sudo apt-get install -y \
  gcc-x86-64-linux-gnu g++-x86-64-linux-gnu \   # sysmsg + vdso genrules
  g++ \                                          # native C++ (cc1plus)
  clang llvm libbpf-dev                          # tools/xdp eBPF genrules
```

bazel is fetched via bazelisk (`bazelisk-linux-arm64`); the repo pins bazel 8.3.1
(`.bazelversion`). Build with capped resources to avoid OOM on 8 GiB:
`bazel build --jobs=2 --local_resources=memory=5000 //runsc:runsc`.
A transient `proxy.golang.org` TLS timeout during dep fetch is retryable (bazel
caches successful fetches).
- [x] real-workload checkpoint/restore proven (1 GiB warmed workload resumes with
      live state intact) and baseline measured (3 clones = 3196 MiB, ~N x base)
- [ ] implement the GVISOR-3 MAP_PRIVATE-base hook in pgalloc
- [ ] wire a CAS-backed base fd + userfaultfd populate; run the §16.1 N-sandbox
      acceptance test through runsc
