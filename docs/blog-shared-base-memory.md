# Sharing guest memory across gVisor sandboxes: a copy-on-write base, and a platform-shaped surprise

*A write-up on making thousands of near-identical gVisor sandboxes share one physical copy
of their RAM: the questions that had to be answered in order, what the measurements said,
the bug that only an end-to-end test could surface, and the larger result that turned up
once real hardware was available.*

**The short version.** Through gVisor's actual `MemoryFile` save/restore path, shared-base
restore produced roughly 5× to 17× lower physical memory than full private restores, and
content-addressed verification did not change that. The prototype then crashed on a running
guest: it lines up with KVM's guest-memory path, but systrap maps guest memory through a
different memfd, so the restored workload died there even though the pgalloc tests passed.
Real `/dev/kvm` hardware later confirmed the other half: on KVM the same workload restores
over a shared base and runs, exactly as the code predicted. And a real agent, rather than a
synthetic one, surfaced a larger result than the one this work set out to find: skipping
cold start is worth more than sharing memory, and unlike the density win it holds on every
gVisor platform. The rest is how each of those came about.

## The problem: density is a memory wall

[Substrate](https://github.com/agent-substrate/substrate) runs many agent workloads as
gVisor sandboxes, and the workload shape is unusual. Hundreds or thousands of sandboxes
start out *almost identical* (same runtime, same loaded model code, same warmed heap), and
each then diverges a little as it does its own work. The mental model is "fork a warmed
process a thousand times," except each fork is a full gVisor sandbox restored from a
checkpoint.

gVisor (runsc) checkpoint/restore handles this well: warm a sandbox, checkpoint it, restore
N clones. But restore is a *full copy*. Each restored sandbox materializes its own private
copy of guest RAM. The baseline, measured directly, was three clones of a 512 MiB warmed
HTTP server restored from one checkpoint:

```
3 × ~557 MiB sentry RSS = 1674 MiB
```

Each number is the resident memory (RSS) of a runsc *sentry*, the per-sandbox userspace
kernel process. That is ~1× base *per clone*, and at a thousand clones RAM is the wall long
before CPU is.

The hypothesis is the obvious one: most of that 557 MiB is *identical* across clones (the
warmed base), and only a small slice differs per clone (the delta). Keep one physical copy
of the base, share it copy-on-write, and density becomes **delta-bound, not base-bound**.
The project does not hinge on this, but it would be a valuable property if it holds, so the
first step was to look for evidence before writing a line of gVisor.

I will call the improvement factor the **flatten**: the ratio of would-be private resident
memory (every clone a full copy) to shared physical memory (one base plus the per-clone
deltas). A 10× flatten means the clones consume roughly one-tenth the physical RAM they
would as full copies.

## Does the OS even do this?

The cheapest version of the question comes first, before any gVisor change. If N processes
`MAP_PRIVATE` the *same* file, does Linux keep one physical copy of the unwritten pages and
copy-on-write only on the first store?

A standalone C harness (`density_smoke.c`, no gVisor involved) answers it: one base file, N
processes each mapping it `MAP_PRIVATE`, reading all of it, and one writer scribbling over
half. Reading `/proc/self/smaps` for PSS (proportional set size, where shared pages count
as size/num_sharers):

```
256 MiB base, N=8:
  non-writers:  rss=256 MiB   pss= 34 MiB   private_dirty=  0 MiB
  writer (½):   rss=256 MiB   pss=144 MiB   private_dirty=128 MiB

1 GiB base:  N=8  → pss=137 MiB      N=64 → pss=16 MiB  (= base/64)
```

PSS divides cleanly by the number of sharers, and writes create private pages for the
writer only. The kernel primitive is therefore exactly what is needed: **resident base
pages are physically shared, and writes go copy-on-write.** The remaining work is to route
gVisor's guest memory through this primitive, not to invent a mechanism.

## How big is the delta, really?

Base sharing only matters if the per-clone delta is small. If a restored agent dirties most
of its pages in the first few seconds, sharing buys little and the effort is wasted. The
measurement had to come through the *real* gVisor memory, not a model.

gVisor backs guest RAM with a per-sandbox `memfd` (the `MemoryFile` in
`pkg/sentry/pgalloc`), and that memfd turns out to be laid out linearly in guest-physical
order. So a running sandbox's guest memory can be snapshotted just by reading
`/proc/<sentry>/fd/<memfd>` at two points in time and diffing it page by page, with no
gVisor rebuild.

A 512 MiB warmed Go HTTP server under runsc, snapshotted, driven through **5000 real
requests**, and snapshotted again:

```
base committed:                       514 MiB
delta after 5000 requests:   2205 pages = 8.6 MiB   (1.7% of base)
```

The large warmed read-mostly region showed **zero** changed pages after the request run;
the measured delta came from runtime and request state, not the warmed working set. (The
514 MiB is committed guest memory, slightly above the nominal 512 MiB heap.) As a sanity
check on the harness, the server was made to dirty a *known* 100 MiB and re-measured at
100.4 MiB, within 0.4%.

This is the *checkpoint delta*: pages whose final contents differ from the base. It is not
total write traffic or churn (a page dirtied and then restored to its original bytes is not
delta), which would matter for a different design but not for base/delta restore size.

So for a read-mostly service the per-clone delta is ~1.7%, and the flatten is worth
building. The caveat that stayed attached to this number: it is workload-dependent. A
write-heavy agent has a larger delta, and density scales as `(RAM - base) / delta`.

## Building it into gVisor: the base/delta split

The plan: a restored sandbox loads **only its delta** and gets the base from a shared,
read-only base image mapped copy-on-write. That requires changing both halves of gVisor's
save/restore.

**The save side.** `MemoryFile.SaveTo` walks its memory-accounting tree and writes every
committed non-zero page into a "pages file." It now takes a base image and, for each
committed page, compares it to the base; pages identical to the base are *excluded* from
the pages file and recorded in a `baseBacked` set in the checkpoint metadata. The
comparison folds into the existing per-page scan, so there is no second walk of guest
memory, though each candidate page now also does a base read and compare. With no base
supplied, behavior is byte-for-byte identical to upstream.

Zero pages need one subtlety. Upstream omits all-zero committed pages, since restore would
see zero there anyway. A base overlay breaks that reasoning: an all-zero page sitting over
a *non-zero* base page is a real delta, and omitting it would make restore wrongly fall
through to the base. So inside the base range the deciding rule is "differs from the base,"
not "is non-zero"; an all-zero page that differs from its base block is saved as delta.

**The save/restore symmetry.** One property shaped the whole design: the pages file is
*packed in walk order*. A page's position in the file is its scan order, not its memory
offset, so the restore side has to walk the same structure in the same order to line the
offsets back up. The base/delta split therefore has to be a **symmetric skip**: both save
and load skip exactly the `baseBacked` set, recorded once in the metadata so both sides
agree.

**The load side.** `LoadFrom` maps the chunks and overlays `[0, baseBytes)` `MAP_PRIVATE`
from the base image, which is the `density_smoke` primitive now living inside gVisor. The
delta pages are applied *over* that overlay (copy-on-write), and the `baseBacked` pages are
never loaded, because the overlay already shows them.

One worry evaporated on inspection. gVisor's async page loader reads the pages file into
guest memory, and the concern was that it wrote to the memfd file descriptor directly,
bypassing the `MAP_PRIVATE` overlay. The code (`FDReader`) reads straight into the *mapping
memory* via the iovecs the loader builds from the chunk mapping. Because the overlay is
established before those iovecs are built, an async delta load writes into the private
overlay and copy-on-writes it, exactly as a userspace write would. No loader rewrite was
needed.

Each piece has an in-tree test: a base-backed page reads base content, a delta-written page
reads delta content, the base file stays immutable, and a full `SaveTo → LoadFrom`
round-trip over both the synchronous and the async (runsc-style) paths reproduces memory
byte-for-byte.

## The flatten, measured through the real code

With the split working, the payoff is measurable through the *actual* `SaveTo`/`LoadFrom`
code: build one base, save a delta-only checkpoint against it, restore N `MemoryFile`s over
the one shared base, and read `smaps`. One measurement gives both terms. Summed `Rss` is
the intentionally pessimistic accounting, counting the same shared-clean base pages once
per clone (the would-be no-sharing cost); summed `Pss` divides those pages across sharers
and is the better proxy for physical resident memory. `Rss/Pss` is therefore a useful
flatten estimate, not a perfect model of all overhead:

| N | base / delta | flatten (Rss/Pss) | ideal | Pss (shared) | Rss (no-share) |
|---|---|---|---|---|---|
| 8 | 128 / 8 MiB | **4.9×** | 5.7× | 215 MiB | 1057 MiB |
| 16 | 128 / 4 MiB | **10.1×** | 11.0× | 206 MiB | 2068 MiB |
| 32 | 64 / 2 MiB | **15.0×** | 16.5× | 137 MiB | 2061 MiB |

That is about 90% of the theoretical `N·(base+delta) / (base + N·delta)`; the gap is a
fixed Go-runtime and page-table overhead that amortizes as the base grows. Density is
delta-bound, as hoped, and now through gVisor's real memory path rather than a toy.

## Can you trust the base?

A shared base becomes a security question the moment it comes from anywhere other than the
sandbox itself: a node-local cache, a peer, an object store. A base page should not be
mapped into a sandbox unless it is certain to be the page it claims to be. The rule I want
is **verify-before-expose**: no byte reaches the guest without being checked first.

The check is content-addressing. The base is split into 2 MiB blocks, and each block gets a
canonical Git-style SHA-256 identifier of its contents (computed with a tool called
Terrapin, the same object-ID construction Git uses). Change a block's bytes by one bit and
its identifier changes. The base ships with a manifest of expected identifiers; before a
block is exposed, its identifier is recomputed and compared.

The architectural point is *where* this verification belongs. The naive placement, a
per-sandbox userfaultfd handler that fetches and verifies each page as the sandbox faults
it, defeats the purpose: each sandbox would fault and populate its *own* private copy, and
nothing would be shared. The correct placement is **node-level**: verify the shared base
**once per node** before any sandbox maps it, then let sandboxes `MAP_PRIVATE`-share the
already-verified copy. The property is "amortized once per block per node," and it falls
out of the mechanism.

Two things sit outside the hash, and a complete security story has to name them. The
manifest itself must be trusted checkpoint metadata: signed, pinned, or otherwise
authenticated by the control plane, because content addressing proves a block matches the
manifest but not that the manifest is the right one. And the verified node-local base must
stay immutable after verification: sealed, an immutable content-addressed cache entry, or
otherwise guaranteed that the exact verified file descriptor is the one sandboxes later
map. Trust terminates at the manifest and the sealed base, not at the bytes alone.

The implementation went two ways.

**Eager.** Verify every block of the base against the manifest, then share. Verification
does not change the flatten (it gates trust, it does not add resident pages), and the cost
is paid once per node, independent of clone count:

```
N=16: flatten 8.0×   N=32: flatten 15.6×   verify: 35 ms / 64 MiB (~1.9 GB/s), once per node
```

**Lazy / remote.** The transport case. A userfaultfd MISSING handler over a node-shared
backing fetches each absent block from a content-addressed store on first access, verifies
it, and installs it (`UFFDIO_COPY`); known-zero blocks install via `UFFDIO_ZEROPAGE` with
no fetch. With one block deliberately corrupted in the store:

```
16 blocks (1 known-zero, 1 tampered):
  fetches=15  verifies=15  zero-installs=1  rejects=1
  re-touching every block added 0 fetches  → once per node
  tampered block → rejected by verify, never exposed
  flatten: N=16 → 9.1×,  N=32 → 17.3×
```

A single flipped byte changes the block's identifier, so it is caught and refused at the
fault. Verification does not move the memory flatten; it only adds a one-time, node-local
CPU and latency cost.

Standalone costs worth noting: verifying a 2 MiB block is ~0.9 ms (2.33 GB/s), which is
*cheaper* than the page-fault populate it rides on (1.18 ms), so a full 1 GiB base verifies
in ~0.43 s, once per node; capturing a base from a live sandbox runs at ~3.7 GB/s.

At this point every layer was proven in-tree: share a base, copy-on-write a delta over it,
compute the delta, exclude it on save, overlay it on restore, verify it eagerly or lazily,
and a measured ~10× flatten at N=16. The next step was to wire it into runsc and restore a
real workload over a base.

## Then it crashed

The restore-side plumbing came first. `runsc restore` picks up a `base.img` from the image
directory and threads it as a file descriptor through the sandbox, then the boot
controller, then `kernel_restore`, down to the main `MemoryFile`'s `LoadFrom`. runsc built.
A small stateful workload (a counter plus a checksummed heap) checkpointed at tick 4, then
restored two ways:

```
restore WITHOUT base.img:  tick 5,6,7,8,9   checksum=ok      ✅  (regression clean)
restore WITH    base.img:  container stopped, no output       ❌  (workload crashed)
```

The debug log showed the base image threaded through, the restore completing cleanly, every
timer running to "end Restore," and then nothing. No tick. Restore had succeeded flawlessly,
and the guest was dead. The clean restore-without-base was the useful control here: it ruled
out an ordinary restore regression and pinned the failure squarely on the base-overlay path.

## Who actually maps the guest's memory?

The unit tests all passed, and the flatten was real. So what was different about a *running
guest*?

The answer: **the guest does not execute against the mapping being shared.** The overlay,
the flatten, and the unit tests all operate on the *sentry's* view of guest memory,
`MemoryFile.MapInternal`, the chunk mapping the sentry uses to read and write guest pages
for syscalls. But the guest *runs* against memory the **platform** sets up, which is a
different code path.

`platform.AddressSpace.MapFile`, the call that installs guest memory into the guest's
address space, has two very different implementations:

- **systrap** (`subprocess.go`): `MapFile` does `mmap(MAP_SHARED, f.DataFD(fr), fr.Start)`.
  It maps the guest straight from the per-sandbox **memfd** file descriptor, into a
  **separate stub process**. The stub is `clone`d with `CLONE_FILES | SIGCHLD` and *no*
  `CLONE_VM`, so the sentry and the stub are distinct address spaces that share guest RAM
  only through `MAP_SHARED` of the memfd.

The overlay was on the sentry's chunk mapping. The async loader had copied the delta into
*that* mapping (copy-on-write), which left the memfd itself holey for the base range. The
stub, mapping the memfd, saw zeros where the program's code and heap should be, and the
workload crashed on the first instruction it tried to run from a base page. The unit tests
passed precisely because they read through `MapInternal`, the sentry view, which *did* have
the data.

This is the kind of bug only an end-to-end test surfaces. Every layer was individually
correct; the layering was wrong.

## The resolution: it is platform-shaped

The natural next thought, "move the overlay down to `MapFile` or `DataFD`," runs into
systrap. The sentry and stub are separate address spaces, and both need a coherent view of
guest RAM, which is why systrap maps the per-sandbox memfd `MAP_SHARED` into the stub. If
both processes instead mapped a shared base `MAP_PRIVATE`, the first write to a base-backed
page could create *different* private copies in the sentry and the stub. The two layouts,
side by side:

```
KVM:      guest page tables → sentry MapInternal → MAP_PRIVATE base + COW delta
systrap:  stub process      → MAP_SHARED memfd
          sentry            → MapInternal overlay   (guest never sees this)
```

Cross-sandbox base sharing, intra-sandbox sentry/stub coherence, and per-page base/delta
composition are not all available from plain mmap on systrap. The realistic options there
are KSM (let the host dedup identical pages across sandboxes, which is simple but
opportunistic and content-blind, so no verification integration) or a deeper coherent
shared-base plus delta-overlay mechanism.

KVM is different. Its `MapFile` maps the guest from `MapInternal`, the sentry mapping the
base overlay modifies:

```go
bs, err := f.MapInternal(fr, ...)   // the SENTRY's mapping
// ...map bs into the guest's page tables...
```

There is no separate stub with an independent `MAP_SHARED` memfd view, and there is one
process per sandbox, so `MAP_PRIVATE` base plus copy-on-write is coherent and shares across
sandboxes via the page cache. The design lines up with KVM's memory path: the measured ~10×
flatten is at the layer KVM consumes. What remained was the end-to-end KVM validation,
which the Apple-silicon test setup could not run because it provided no `/dev/kvm`, leaving
systrap as the only available platform.

So the verdict was not "the prototype is broken." It was "the prototype targets the
platform that matters for density (KVM), and it was tested on the fallback platform where
it does not apply." The pgalloc unit tests read through `MapInternal`, exactly the path KVM
uses for guest memory, so they are valid coverage of the real target.

## Getting to real KVM

That is where the story sat: the design lines up with KVM, but no `/dev/kvm` was anywhere
in reach to prove it on a running guest. Leaving a load-bearing claim resting on a code read
is an uncomfortable place to stop, so the next move was to go find hardware.

The answer was Google Cloud. A GCE instance with nested virtualization enabled exposes a
real `/dev/kvm`, and because Google's nested virtualization is genuine Linux KVM (the
platform gVisor's KVM backend is built for), gVisor runs there without the host-resetting
crashes that nesting KVM under Apple's or VMware's hypervisors produced. The same patched
runsc, built on that instance, could finally run the test the earlier work could not.

First, `runsc --platform=kvm` ran a sandbox at all, and the host stayed up. Then the test
that mattered: the same small stateful workload, checkpointed and restored two ways, this
time on KVM:

```
restore WITHOUT base.img (--platform=kvm):  ticks continue, checksum=ok   (regression clean)
restore WITH    base.img (--platform=kvm):  ticks continue, checksum=ok   ✅
```

The workload that crashed on systrap resumed cleanly on KVM, memory intact, with the debug
log confirming the base image was threaded in and the overlay applied. The platform-shaped
hypothesis was not a hedge. KVM maps the guest from `MapInternal`, exactly the mapping the
overlay modifies, so the guest sees the shared base and the copy-on-write delta, while the
same restore on systrap still dies because its stub reads a different memfd. Predicted from
the code, confirmed on hardware.

The checkpoint side, missing until now, also came together. `runsc checkpoint --shared-base`
exports the base and writes a delta-only checkpoint. For a freshly warmed clone the delta
is zero, so the pages file comes out empty and all of guest RAM lands in the shared base
image. With both sides in place, the flatten is measurable through the real runsc CLI on a
live KVM guest rather than through the pgalloc tests: eight real sandboxes restored over one
shared 256 MiB base gave a 5.7× flatten in summed sentry PSS.

That is lower than the ~10× the pgalloc tests reported, and the gap is the subject of the
next section.

## A real agent, and a humbler flatten

Everything so far used workloads built to be measurable: a Go HTTP server, a C program with
a checksummed heap. A real one was needed to trust the result. The choice was an agent on
Google's Agent Development Kit (google-adk) with the LLM endpoint mocked out, so it drives
the full framework (sessions, the runner, the model interface) with no network.
Checkpointed and restored under KVM, it came back correctly: live Python, asyncio, and grpc
all intact.

The density flatten for the real agent was modest: about 2.9× across eight clones over an
80 MiB base. Two honest reasons, and both matter more than the number.

First, the flatten only ever applies to memory that is actually resident. Inflating the
base by having the agent allocate a gigabyte at startup did nothing: memory the clones
never touch after restore is never faulted in, so it costs no physical RAM and there is
nothing to share. Density scales with the shared resident working set, not with allocated
size.

Second, and this is the ceiling that had not been measured before, each sandbox carries a
fixed floor of about 20 MiB that cannot be shared: roughly 17 MiB of sentry and 3 MiB of
gofer, about half of it live Go runtime across those two Go processes. It is not garbage
the collector can reclaim (capping the GC moved nothing); it is threads, goroutine stacks,
and runtime structure, on top of a full guest kernel. The sentry is around 277,000 lines of
Go implementing 645 syscalls, which is not a thing you shrink. The density math is
`(base + floor) / (base/N + floor)`, and an 80 MiB base on a 20 MiB floor cannot flatten
far. On a 15 GiB, four-core box about 300 of these sandboxes fit before memory ran out. The
flatten is real, but it pays off only when the shared resident base is large next to that
floor, which for a small agent it is not.

A lukewarm verdict on density would have been a reasonable place to stop. The same agent had
a better result to offer, and it had nothing to do with memory.

## The win I was not looking for

Cold-starting the agent under gVisor takes about eleven seconds, almost all of it CPU:
importing roughly 1,400 Python modules, most of the time spent inside the Gemini SDK
constructing its Pydantic type hierarchy as the modules load. It is not fetching anything
(the sandbox has no network and the packages are pre-installed) and not compiling bytecode
(every `.pyc` is already present); it is executing that much initialization code, amplified
by gVisor's per-syscall cost on a file-heavy import.

Restoring from a snapshot skips all of it, mapping the already-initialized image and
resuming:

```
                 cold start (import + init)      restore from snapshot
 wall            11.4 s                          0.6 s
 CPU              7.1 s                          ~0.5 s
```

"Sub-second restore" is exactly the kind of claim that hides a fault-in stall, so it was
worth trying to break. Three checks.

*Is it actually warm?* Yes. The restored agent serves its first real request in about 0.6
seconds and its second in 29 milliseconds, steady at 20. There is no multi-second thrash.
(One instrument read three seconds for the first request, which was alarming right up until
the cause turned out to be self-inflicted: a wall-clock timer left running across the
checkpoint freeze, dutifully counting the time the process spent stopped. The external clock
and the 29 ms second request set the record straight.)

*Is it correct?* Bit-exact. Every post-restore response verified through the full agent
path, and an in-memory accumulator the agent kept came back with precisely the value it
would have had if it had never stopped.

*Is it cheap to park?* Checkpointing takes 0.11 seconds, and with a shared base each parked
agent costs about 248 KiB (its kernel state plus a near-zero delta) instead of the ~81 MiB
of a full checkpoint. A pool of ten thousand parked agents is a couple of gigabytes rather
than most of a terabyte.

The caveat is burst. A single restore is sub-second, but fifty at once on four cores take
about 18 seconds and a hundred take forty-plus, because each restore plus its first request
is roughly 1.5 seconds of CPU and they contend for the cores. Restore is not instant
scale-up. It is about four times cheaper per start than a cold start, at every scale, which
is the narrower and more defensible claim: provision cores for the burst rate, and each
start costs a quarter of what it did.

## The part that runs anywhere

The twist ties back to the platform-shaped surprise. The density flatten is KVM-only,
because it depends on the base overlay reaching the guest, and that only happens on KVM.
Skipping the cold start does not use the overlay at all; it needs only checkpoint and
restore, which work on every gVisor platform. The same cold-versus-restore test on systrap,
the no-`/dev/kvm` platform most gVisor deployments actually use:

```
 systrap    cold 4.05 s / 1.99 s CPU   →   restore 0.18 s / 0.26 s CPU   (correct)
```

Faster than KVM here, in fact, because gVisor's KVM backend pays nested-virtualization
overhead on the rented cloud host; on bare metal that gap narrows. The exact number is not
the point. The point is that the conditional, KVM-only feature this work set out to build
sits right next to an unconditional, works-everywhere result that turned up alongside it:
restore an agent instead of cold-starting it, and each start is warm, correct, and several
times cheaper, on any platform.

## What I learned

- **The kernel primitive and the economics check out.** Linux shares `MAP_PRIVATE` file
  pages copy-on-write (PSS ≈ base/N), and a read-mostly agent's per-clone delta is ~1.7% of
  its RAM. Density is delta-bound.
- **The flatten is real, and it was the smaller prize.** Through gVisor's save/restore path
  it scales 5× to 17× with clone count at the pgalloc layer; on a live KVM guest through the
  runsc CLI it is lower (about 5.7× at eight clones, 2.9× for a real 80 MiB agent) because
  of a per-sandbox floor the pgalloc tests never see.
- **That floor is the real density ceiling, not the language or the mechanism.** About 20
  MiB per sandbox, half of it live Go runtime across two processes, on top of a full guest
  kernel, none of it shareable and little of it reducible. Base sharing wins only when the
  shared resident base is large next to that floor.
- **Verify-before-expose composes without hurting density.** Content-addressed verification
  of the shared base, once per node against trusted metadata, adds no per-sandbox resident
  memory and does not move the flatten; tampering is caught before exposure.
- **`memmap.File` / `MapFile` / `DataFD` hides a load-bearing platform difference.** KVM
  maps the guest from the sentry's `MapInternal`; systrap maps it from the memfd FD into a
  separate stub. A memory feature living in `pgalloc` is implicitly betting on which one the
  guest uses. This one was right for KVM (confirmed on hardware) and wrong for systrap,
  exactly as the code predicted.
- **End-to-end tests, and real workloads, find what unit tests cannot.** Nine green unit
  tests found a measured flatten. One real checkpoint/restore found the platform bug. And
  one real agent showed that the memory density this work set out to find was a conditional
  bonus, while the startup saving it was not looking for was the robust, universal result.

## What's next

- **Density on a large active base.** The flatten is modest for a small agent and should be
  large for one that keeps a big model or index resident and reads it every turn. That is
  the measurement that would make the density case, and it has not been run end to end.
- **A systrap density story.** The latency result already works on systrap; the memory
  result does not. Kernel-samepage-merging on the per-sandbox memfds is the pragmatic,
  verification-free option; a coherent shared-base-plus-delta across the sentry and stub is
  the explicit one.
- **The floor itself.** The one tractable piece of the 20 MiB floor is the gofer, which
  directfs already sidelines at runtime but still runs as a whole second Go process. Reaping
  it is worth more than any GC tuning.
- **Base-aware accounting.** Resident base pages should be counted once per node, not once
  per sandbox.

The mechanism is sound where density matters, and it is proven on the platform that
matters. But the result worth leading with is the one this work tripped over while looking
for something else: for a fleet of near-identical agents, the cheapest thing to do is not
start them at all, and restore is how you avoid it. Sharing their memory, the thing this
whole exercise set out to do, turns out to be the bonus on top.
