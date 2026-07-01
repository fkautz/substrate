# Sharing guest memory across gVisor sandboxes: a copy-on-write base, and a platform-shaped surprise

*A field report on making thousands of near-identical gVisor sandboxes share one
physical copy of their RAM: what I tried, the questions that kept surfacing, what I
measured, the bug that only an end-to-end test could find, and, once I finally got the
hardware, the win I was not looking for.*

## The problem: density is a memory wall

I contribute to [Substrate](https://github.com/agent-substrate/substrate), a
system for running many agent workloads as gVisor sandboxes. The shape of the
workload is unusual: you start hundreds or thousands of sandboxes that are
*almost identical* (same runtime, same loaded model code, same warmed heap),
and each one then diverges a little as it does its own work. Think "fork a
warmed process a thousand times," but each fork is a full gVisor sandbox
restored from a checkpoint.

gVisor (runsc) checkpoint/restore works well for this: you warm a sandbox, checkpoint
it, and restore N clones. But restore is a *full copy*. Each restored sandbox
materializes its own private copy of guest RAM. I measured the baseline directly: three
clones of a 512 MiB warmed HTTP server, restored from one checkpoint, cost

```
3 × ~557 MiB sentry RSS = 1674 MiB
```

Each of those numbers is the resident memory (RSS) of a runsc *sentry*, the per-sandbox
userspace kernel process. That's ~1× base *per clone*; at a thousand clones, RAM is the
wall long before CPU is.

The hypothesis is the obvious one: most of that 557 MiB is *identical* across clones (the
warmed base), and only a small slice differs per clone (the delta). If you could keep one
physical copy of the base and share it copy-on-write, density would become **delta-bound,
not base-bound**. The project doesn't hinge on this, but it would be an amazing property
if it works, so I went looking for evidence before writing a line of gVisor.

I'll call the improvement factor the **flatten**: the ratio between would-be private
resident memory (every clone a full copy) and shared physical memory (one base plus the
per-clone deltas). A 10× flatten means the clones consume roughly one-tenth the physical
RAM they would have as full copies.

**The result, upfront.** Through gVisor's actual `MemoryFile` save/restore path, shared
base restore produced roughly 5× to 17× lower physical memory than full private restores
in the tested configurations, and content-addressed verification did not change the
flatten. Then it crashed. The prototype lines up with KVM's guest-memory path, but systrap
maps guest memory through a different memfd path, so the restored workload died there even
though the pgalloc tests passed. Later I got a machine with real `/dev/kvm` and confirmed
the other half: on KVM the same workload restores over a shared base and runs, exactly as
the code predicted. And testing a real agent instead of a toy, I found a bigger win than
the one I set out for, a startup saving that works on every platform. The rest of this post
is how I got to each of those.

## Question 1: does the OS even do this?

Before touching gVisor, I asked the cheapest possible version of the question. If N
processes `MAP_PRIVATE` the *same* file, does Linux keep one physical copy of the unwritten
pages and copy-on-write only on the first store?

A standalone C harness (`density_smoke.c`, no gVisor involved): one base file, N
processes each mapping it `MAP_PRIVATE`, reading all of it, and one writer scribbling over
half. I read `/proc/self/smaps` and looked at PSS (proportional set size, where shared
pages are counted as size/num_sharers):

```
256 MiB base, N=8:
  non-writers:  rss=256 MiB   pss= 34 MiB   private_dirty=  0 MiB
  writer (½):   rss=256 MiB   pss=144 MiB   private_dirty=128 MiB

1 GiB base:  N=8  → pss=137 MiB      N=64 → pss=16 MiB  (= base/64)
```

PSS divides cleanly by the number of sharers; writes create private pages for the writer
only. So the kernel primitive is exactly what's needed: **resident base pages are
physically shared, writes go copy-on-write.** The remaining work is to route gVisor's
guest memory through this primitive, not to invent a mechanism.

## Question 2: how big is the delta, really?

Base sharing only matters if the per-clone delta is small. If a restored agent dirties
most of its pages in the first few seconds, sharing buys little and the rest of the effort
is wasted. So I measured it, and I wanted the measurement to come through the *real*
gVisor memory, not a model.

gVisor backs guest RAM with a per-sandbox `memfd` (the `MemoryFile` in
`pkg/sentry/pgalloc`). It turns out that memfd is laid out linearly in guest-physical
order, so I could snapshot a running sandbox's guest memory just by reading
`/proc/<sentry>/fd/<memfd>` at two points in time and diffing it page by page. No gVisor
rebuild required.

I ran a 512 MiB warmed Go HTTP server under runsc, snapshotted, drove **5000 real
requests**, snapshotted again:

```
base committed:                       514 MiB
delta after 5000 requests:   2205 pages = 8.6 MiB   (1.7% of base)
```

The large warmed read-mostly region itself showed **zero** changed pages after the request
run; the measured delta came from runtime and request state, not from the warmed working
set. (The 514 MiB is committed guest memory, a little above the nominal 512 MiB heap.) To
make sure the harness wasn't lying, I had the server dirty a *known* 100 MiB and
re-measured: it reported 100.4 MiB, within 0.4%.

This measures the *checkpoint delta*: pages whose final contents differ from the base. It
is not total write traffic or churn (a page dirtied and then restored to its original
bytes is not delta), which would matter for a different design but not for base/delta
restore size.

So for a read-mostly service the per-clone delta is ~1.7%. The flatten is worth building.
(The honest caveat I kept attached to this number: it's workload-dependent. A
write-heavy agent has a bigger delta; density scales as `(RAM - base) / delta`.)

## Building it into gVisor: the base/delta split

The plan: a restored sandbox should load **only its delta** and get the base from a
shared, read-only base image mapped copy-on-write. That means changing both halves of
gVisor's save/restore.

**The save side.** `MemoryFile.SaveTo` walks its memory-accounting tree and writes every
committed non-zero page into a "pages file." I taught it to take a base image and, for
each committed page, compare it to the base; pages identical to the base are *excluded*
from the pages file and recorded in a `baseBacked` set in the checkpoint metadata. The
comparison folds into the existing per-page scan, so there's no second walk of guest
memory, though each candidate page now also does a base read and compare. With no base
supplied, behavior is byte-for-byte identical to upstream.

One subtlety about zero pages. Upstream omits all-zero committed pages, because restore
would see zero there anyway. With a base overlay that reasoning breaks: an all-zero page
sitting over a *non-zero* base page is a real delta, and omitting it would make restore
wrongly fall through to the base. So inside the base range the deciding rule is "differs
from the base," not "is non-zero": an all-zero page that differs from its base block is
saved as delta.

**The save/restore symmetry.** One finding shaped the whole design: the pages file is
*packed in walk order*. A page's position in the file is its scan order, not its memory
offset. So the restore side has to walk the same structure in the same order to line the
offsets back up. The base/delta split therefore has to be a **symmetric skip**: both save
and load skip exactly the `baseBacked` set, recorded once in the metadata so both sides
agree.

**The load side.** `LoadFrom` maps the chunks, and I overlay `[0, baseBytes)`
`MAP_PRIVATE` from the base image: the `density_smoke` primitive, now inside gVisor. Then
the delta pages are applied *over* that overlay (copy-on-write), and the `baseBacked`
pages are simply never loaded, because the overlay already shows them.

Here a worry evaporated. gVisor's async page loader reads the pages file into the guest
memory; I feared it wrote to the memfd file descriptor directly, which would bypass the
`MAP_PRIVATE` overlay. Reading the code (`FDReader`), it actually reads straight into the
*mapping memory* via the iovecs the loader builds from the chunk mapping. Since the overlay
is established before those iovecs are built, an async delta load writes into the
private overlay and copy-on-writes it, exactly as a userspace write would. No loader
rewrite was needed.

I verified each piece with in-tree tests: a base-backed page reads base content; a
delta-written page reads delta content; the base file stays immutable; a full
`SaveTo → LoadFrom` round-trip over both the synchronous and the async (runsc-style) paths
reproduces memory byte-for-byte.

## The flatten, measured through the real code

With the split working, I measured the payoff through the *actual* `SaveTo`/`LoadFrom`
code: build one base, save a delta-only checkpoint against it, restore N `MemoryFile`s over
the one shared base, and read `smaps`. The elegant part is that one measurement gives
both terms. Summed `Rss` is the intentionally pessimistic accounting: it counts the same
shared-clean base pages once per clone (the would-be no-sharing cost). Summed `Pss`
divides those pages across sharers and is the better proxy for physical resident memory.
So `Rss/Pss` is a useful flatten estimate, not a perfect model of all overhead:

| N | base / delta | flatten (Rss/Pss) | ideal | Pss (shared) | Rss (no-share) |
|---|---|---|---|---|---|
| 8 | 128 / 8 MiB | **4.9×** | 5.7× | 215 MiB | 1057 MiB |
| 16 | 128 / 4 MiB | **10.1×** | 11.0× | 206 MiB | 2068 MiB |
| 32 | 64 / 2 MiB | **15.0×** | 16.5× | 137 MiB | 2061 MiB |

About 90% of the theoretical `N·(base+delta) / (base + N·delta)`; the gap is a fixed
Go-runtime plus page-table overhead that amortizes as the base grows. Density is
delta-bound, exactly as hoped, and now through gVisor's real memory path, not a toy.

## Question 3: can you trust the base?

A shared base is a security question the moment it comes from anywhere other than the
sandbox itself: a node-local cache, a peer, an object store. You do not want to map a
base page into a sandbox unless you are certain it's the page you think it is. The rule I
want is **verify-before-expose**: no byte reaches the guest without being checked first.

The checking is content-addressing. I split the base into 2 MiB blocks and give each block
a canonical Git-style SHA-256 identifier of its contents (computed with a tool called
Terrapin, the same object-ID construction Git uses). If a block's bytes change by even one
bit, its identifier changes. The base ships with a manifest of expected identifiers; before a block
is exposed, you recompute its identifier and compare.

The important architectural realization is *where* this verification belongs. The naive
idea, a per-sandbox userfaultfd handler that fetches and verifies each page as the sandbox
faults it, defeats the whole point, because each sandbox would fault and populate its
*own* private copy, and there'd be nothing shared. The right placement is **node-level**:
verify the shared base **once per node** before any sandbox maps it, then let sandboxes
`MAP_PRIVATE`-share the already-verified copy. The spec phrase for this is "amortized once
per block per node," and it falls right out of the mechanism.

Two things sit outside the hash, and a complete security story has to name them. The
manifest itself must be trusted checkpoint metadata: signed, pinned, or otherwise
authenticated by the control plane. Content addressing proves a block matches the
manifest; it does not prove the manifest is the right one. And the verified node-local
base must stay immutable after verification: sealed, an immutable content-addressed cache
entry, or otherwise guaranteed that the exact verified file descriptor is the one
sandboxes later map. Trust terminates at the manifest and the sealed base, not at the
bytes alone.

I built it two ways:

**Eager.** Verify every block of the base against the manifest, then share. The flatten is
unchanged by adding verification (it gates trust, it doesn't add resident pages), and the
verify cost is paid once per node, independent of clone count:

```
N=16: flatten 8.0×   N=32: flatten 15.6×   verify: 35 ms / 64 MiB (~1.9 GB/s), once per node
```

**Lazy / remote.** The transport case. A userfaultfd MISSING handler over a node-shared
backing fetches each absent block from a content-addressed store on first access,
verifies it, and installs it (`UFFDIO_COPY`); known-zero blocks install via
`UFFDIO_ZEROPAGE` with no fetch. I deliberately corrupted one block in the store:

```
16 blocks (1 known-zero, 1 tampered):
  fetches=15  verifies=15  zero-installs=1  rejects=1
  re-touching every block added 0 fetches  → once per node
  tampered block → rejected by verify, never exposed
  flatten: N=16 → 9.1×,  N=32 → 17.3×
```

A single flipped byte changes the block's identifier, so it's caught and refused at the
fault. Verification does not move the memory flatten; it only adds a one-time, node-local
CPU and latency cost.

Standalone costs worth noting: verifying a 2 MiB block is ~0.9 ms (2.33 GB/s), which
is *cheaper* than the page-fault populate it rides on (1.18 ms), so a full 1 GiB base
verifies in ~0.43 s, once per node; capturing a base from a live sandbox runs at
~3.7 GB/s.

At this point things were looking good. Every layer was proven in-tree: share a base,
copy-on-write a delta over it, compute the delta, exclude it on save, overlay it on
restore, verify it eagerly or lazily, and a measured ~10× flatten at N=16. So I wired it
into runsc and restored a real workload over a base.

## The plot twist: it crashed

I added the restore-side plumbing. `runsc restore` picks up a `base.img` from the image
directory and threads it as a file descriptor through the sandbox, then the boot
controller, then `kernel_restore`, down to the main `MemoryFile`'s `LoadFrom`. runsc built.
I checkpointed a small stateful workload (a counter plus a checksummed heap) at tick 4,
then restored it two ways:

```
restore WITHOUT base.img:  tick 5,6,7,8,9   checksum=ok      ✅  (regression clean)
restore WITH    base.img:  container stopped, no output       ❌  (workload crashed)
```

The debug log was almost taunting: it showed the base image being threaded through and the
restore completing cleanly, every timer running to "end Restore," and then the workload
simply never produced a tick. Restore succeeded; the guest was dead. The clean
restore-without-base mattered here: it ruled out an ordinary restore regression and
isolated the failure to the base-overlay path, not the new plumbing in general.

## The investigation: who actually maps the guest's memory?

The unit tests all passed. The flatten was real. So what was different about a *running
guest*?

The answer is that **the guest doesn't execute against the mapping I'd been sharing.**
Everything I built (the overlay, the flatten, the unit tests) operates on the *sentry's*
view of guest memory: `MemoryFile.MapInternal`, the chunk mapping the sentry uses to read
and write guest pages for syscalls. But the guest *runs* against memory the **platform**
sets up, and that's a different code path.

I followed `platform.AddressSpace.MapFile`, the call that installs guest memory into the
guest's address space, and found two very different implementations:

- **systrap** (`subprocess.go`): `MapFile` does
  `mmap(MAP_SHARED, f.DataFD(fr), fr.Start)`. It maps the guest straight from the
  per-sandbox **memfd** file descriptor, into a **separate stub process**. The stub is
  `clone`d with `CLONE_FILES | SIGCHLD` and *no* `CLONE_VM`, so the sentry and the stub are
  distinct address spaces that share guest RAM only through `MAP_SHARED` of the memfd.

My overlay was on the sentry's chunk mapping. The async loader had copied the delta into
*that* mapping (copy-on-write), which left the memfd itself holey for the base range. The
stub, mapping the memfd, saw zeros where the program's code and heap should be, and the
workload crashed on the first instruction it tried to run from a base page. The unit tests
passed precisely because they read through `MapInternal`, the sentry view, which *did* have
the data.

This is the kind of bug only an end-to-end test surfaces. Every layer was individually
correct; the layering was wrong.

## The resolution: it's platform-shaped

The natural next thought is "move the overlay down to `MapFile` or `DataFD`." But systrap
makes that hard. The sentry and stub are separate address spaces, and both need a coherent
view of guest RAM, which is why systrap maps the per-sandbox memfd `MAP_SHARED` into the
stub. If both processes instead mapped a shared base `MAP_PRIVATE`, the first write to a
base-backed page could create *different* private copies in the sentry and the stub. The
two layouts, side by side:

```
KVM:      guest page tables → sentry MapInternal → MAP_PRIVATE base + COW delta
systrap:  stub process      → MAP_SHARED memfd
          sentry            → MapInternal overlay   (guest never sees this)
```

Cross-sandbox base sharing, intra-sandbox sentry/stub coherence, and per-page base/delta
composition are not all available from plain mmap in systrap. The realistic options there
are KSM (let the host dedup identical pages across sandboxes, which is simple but
opportunistic and content-blind, so no verification integration) or a deeper coherent
shared-base plus delta-overlay mechanism.

KVM is different. Its `MapFile` maps the guest from `MapInternal`, the sentry mapping the
base overlay modifies:

```go
bs, err := f.MapInternal(fr, ...)   // the SENTRY's mapping
// ...map bs into the guest's page tables...
```

There is no separate stub with an independent `MAP_SHARED` memfd view, and one process per
sandbox, so `MAP_PRIVATE` base plus copy-on-write is coherent and shares across sandboxes
via the page cache. In other words, the design lines up with KVM's memory path: the
measured ~10× flatten is at the layer KVM consumes. What remains is the end-to-end KVM
validation, which my Apple-silicon test setup could not run because it provided no
`/dev/kvm`, leaving systrap as the only available platform. That validation is: produce
the base image on the checkpoint side, restore a live workload on `/dev/kvm` hardware, and
confirm the guest sees the same bytes the pgalloc tests see.

So the verdict isn't "the prototype is broken." It's "the prototype targets the platform
that matters for density (KVM), and it was tested on the fallback platform where it doesn't
apply." The pgalloc unit tests read through `MapInternal`, exactly the path KVM uses for
guest memory, so they're valid coverage of the real target.

## Getting to real KVM

That is where the story sat: the design lines up with KVM, but my Apple-silicon setup had
no `/dev/kvm`, so I could not prove it on a running guest. It bothered me enough to go find
hardware.

The answer was Google Cloud. A GCE instance with nested virtualization enabled exposes a
real `/dev/kvm`, and because Google's nested virtualization is genuine Linux KVM, the
platform gVisor's KVM backend is built for, gVisor runs there without the host-resetting
crashes I hit trying to nest KVM under Apple's or VMware's hypervisors. I built the same
patched runsc on that instance and ran the test the story above could not.

First, `runsc --platform=kvm` ran a sandbox at all, and the host stayed up. Then the one
that mattered. I checkpointed the same small stateful workload and restored it two ways,
this time on KVM:

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

I also finished the half the story above was missing: the checkpoint side.
`runsc checkpoint --shared-base` now exports the base and writes a delta-only checkpoint.
For a freshly warmed clone the delta is zero, so the pages file comes out empty and all of
guest RAM lands in the shared base image. With both sides in place I could measure the
flatten through the real runsc CLI on a live KVM guest instead of through the pgalloc
tests: eight real sandboxes restored over one shared 256 MiB base gave a 5.7× flatten in
summed sentry PSS.

That is lower than the ~10× the pgalloc tests reported, and the gap is the whole next
section.

## A real agent, and a humbler flatten

Everything so far used workloads I built to be measurable: a Go HTTP server, a C program
with a checksummed heap. To trust the result I needed a real one, so I ran an agent on
Google's Agent Development Kit (google-adk) with the LLM endpoint mocked out, so it drives
the full framework (sessions, the runner, the model interface) with no network, and
checkpointed and restored it under KVM. Live Python, asyncio, and grpc all came back
correctly.

The density flatten for the real agent was modest: about 2.9× across eight clones over an
80 MiB base. Two honest reasons, and both matter more than the number.

First, the flatten only ever applies to memory that is actually resident. I tried to
inflate the base by having the agent allocate a gigabyte at startup, and it did nothing:
memory the clones never touch after restore is never faulted in, so it costs no physical
RAM and there is nothing to share. Density scales with the shared resident working set, not
with allocated size.

Second, and this is the ceiling I had not measured before, each sandbox carries a fixed
floor of about 20 MiB that cannot be shared: roughly 17 MiB of sentry and 3 MiB of gofer,
about half of it live Go runtime across those two Go processes. It is not garbage the
collector can reclaim (I tried capping it and nothing moved); it is threads, goroutine
stacks, and runtime structure, on top of a full guest kernel. The sentry is around 277,000
lines of Go implementing 645 syscalls, which is not a thing you shrink. So the density math
is `(base + floor) / (base/N + floor)`, and an 80 MiB base on a 20 MiB floor cannot flatten
far. On a 15 GiB, four-core box I could pack about 300 of these sandboxes before memory ran
out. The flatten is real, but it pays off only when the shared resident base is large next
to that floor, which for a small agent it is not.

I could have stopped there with a lukewarm verdict on density. Instead the same agent
handed me the actual result.

## The win I was not looking for

Cold-starting the agent under gVisor takes about eleven seconds, almost all of it CPU:
importing roughly 1,400 Python modules, most of the time inside the Gemini SDK constructing
its Pydantic type hierarchy as the modules load. It is not fetching anything (the sandbox
has no network and the packages are pre-installed) and not compiling bytecode (every `.pyc`
is already there); it is executing that much initialization code, amplified by gVisor's
per-syscall cost on a file-heavy import.

Restoring from a snapshot skips all of it. It maps the already-initialized image and
resumes:

```
                 cold start (import + init)      restore from snapshot
 wall            11.4 s                          0.6 s
 CPU              7.1 s                          ~0.5 s
```

"Sub-second restore" is exactly the kind of claim that hides a fault-in stall, so I went to
break it. Three checks.

*Is it actually warm?* Yes. The restored agent serves its first real request in about 0.6
seconds and its second in 29 milliseconds, steady at 20. There is no multi-second thrash.
(One of my own instruments read three seconds for the first request and briefly scared me.
It was an artifact: a wall-clock timer that had been running across the checkpoint freeze.
The external clock and the 29 ms second request said otherwise.)

*Is it correct?* Bit-exact. Every post-restore response verified through the full agent
path, and an in-memory accumulator I had the agent keep came back with precisely the value
it would have had if it had never stopped.

*Is it cheap to park?* Checkpointing takes 0.11 seconds, and with a shared base each parked
agent costs about 248 KiB (its kernel state plus a near-zero delta) instead of the ~81 MiB
of a full checkpoint. A pool of ten thousand parked agents is a couple of gigabytes instead
of most of a terabyte.

The one caveat is burst. A single restore is sub-second, but fifty at once on four cores
take about 18 seconds and a hundred take forty-plus, because each restore plus its first
request is roughly 1.5 seconds of CPU and they contend for the cores. So restore is not
instant scale-up. It is about four times cheaper per start than a cold start, at every
scale, which is a narrower and more defensible claim: you provision cores for your burst
rate, and each start costs a quarter of what it did.

## The part that runs anywhere

Here is the twist that ties back to the platform-shaped surprise. The density flatten is
KVM-only, because it depends on the base overlay reaching the guest, and that only happens
on KVM. But skipping the cold start does not use the overlay at all. It needs only
checkpoint and restore, which work on every gVisor platform. So I ran the same
cold-versus-restore test on systrap, the no-`/dev/kvm` platform most gVisor deployments
actually use:

```
 systrap    cold 4.05 s / 1.99 s CPU   →   restore 0.18 s / 0.26 s CPU   (correct)
```

Faster than KVM here, in fact, because gVisor's KVM backend pays nested-virtualization
overhead on the rented cloud host; on bare metal that gap narrows. The exact number is not
the point. The point is that the conditional, KVM-only feature I set out to build turned
out to sit right next to an unconditional, works-everywhere win I found by accident:
restore an agent instead of cold-starting it, and each start is warm, correct, and several
times cheaper, with no special platform required.

## What I learned

- **The kernel primitive and the economics check out.** Linux shares `MAP_PRIVATE` file
  pages copy-on-write (PSS ≈ base/N), and a read-mostly agent's per-clone delta is ~1.7%
  of its RAM. Density is delta-bound.
- **The flatten is real, and it turned out to be the smaller prize.** Through gVisor's
  save/restore path it scales 5× to 17× with clone count at the pgalloc layer, and on a
  live KVM guest through the runsc CLI it is lower (about 5.7× at eight clones, 2.9× for a
  real 80 MiB agent) because of a per-sandbox floor the pgalloc tests never see.
- **That floor is the real density ceiling, not the language or the mechanism.** About 20
  MiB per sandbox, half of it live Go runtime across two processes, on top of a full guest
  kernel, none of it shareable and little of it reducible. Base sharing wins only when the
  shared resident base is large next to that floor.
- **Verify-before-expose composes without hurting density.** Content-addressed verification
  of the shared base, done once per node against trusted metadata, adds no per-sandbox
  resident memory and doesn't move the flatten; tampering is caught before exposure.
- **`memmap.File` / `MapFile` / `DataFD` hides a load-bearing platform difference.** KVM
  maps the guest from the sentry's `MapInternal`; systrap maps it from the memfd FD into a
  separate stub. A memory feature that lives in `pgalloc` is implicitly making a bet about
  which of those the guest uses. Mine was right for KVM (confirmed on real hardware) and
  wrong for systrap, exactly as the code predicted.
- **End-to-end tests, and real workloads, find what unit tests cannot.** Nine green unit
  tests found a measured flatten. One real checkpoint/restore found the platform bug. And
  one real agent found that the memory density I was chasing was a conditional bonus, while
  the startup saving I was not chasing was the robust, universal result.

## What's next

- **Density on a large active base.** The flatten is modest for a small agent, and should
  be large for one that keeps a big model or index resident and reads it every turn. That
  is the one measurement that would make the density case, and I have not run it end to end.
- **A systrap density story.** The latency win already works on systrap; the memory win
  does not. Kernel-samepage-merging on the per-sandbox memfds is the pragmatic,
  verification-free option; a coherent shared-base-plus-delta across the sentry and stub is
  the explicit one.
- **The floor itself.** The one tractable piece of the 20 MiB floor is the gofer, which
  directfs already sidelines at runtime but still runs as a whole second Go process.
  Reaping it is worth more than any GC tuning.
- **Base-aware accounting.** Resident base pages should be counted once per node, not once
  per sandbox.

The mechanism is sound where density matters, and I proved it on the platform that matters.
But the lesson I did not expect is the one I would lead with now: for a fleet of
near-identical agents, the cheapest thing you can do is not start them at all, and restore
is how you avoid it. Sharing their memory is a bonus on top.
