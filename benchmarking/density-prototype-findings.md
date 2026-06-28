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

## 4. Status / next

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
- [ ] implement the GVISOR-3 MAP_PRIVATE-base hook in pgalloc
- [ ] wire a CAS-backed base fd + userfaultfd populate; run the §16.1 N-sandbox
      acceptance test through runsc
