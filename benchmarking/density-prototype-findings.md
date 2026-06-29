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

## 7. Status / next

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
