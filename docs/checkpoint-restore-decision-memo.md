# Decision memo: gVisor checkpoint/restore for agent FaaS

**Question.** For a substrate that runs many near-identical, warmed agents in gVisor
sandboxes, does snapshot/restore (with a shared copy-on-write base) buy enough to justify
building on it? I prototyped the mechanism into gVisor and measured it end to end on a real
`/dev/kvm` host with a real Python agent (google-adk, mock LLM). This memo is the honest
readout.

## TL;DR

There are two distinct wins with very different reach:

1. **Fast, cheap starts (universal, robust).** Restoring an agent from a snapshot skips its
   entire cold start. For a real ADK agent that is roughly 4x to 8x less CPU per start and a
   sub-second warm restore, it is correct (state comes back bit-exact), it works on any gVisor
   platform (KVM and systrap, no `/dev/kvm` required), and parking an agent is fast (0.11s) and
   cheap (~248 KB each). The one caveat: a burst of concurrent restores is CPU-throughput-bound,
   so it scales with cores, not magically.

2. **Higher memory density (conditional, KVM-only).** Sharing one copy-on-write base across
   clones flattens resident memory by roughly 3x to 6x, but only for the resident hot working
   set, and it is floored by the roughly 20 MB per-sandbox overhead that cannot be shared. It
   requires the KVM platform.

**Recommendation.** Build on win #1. It is the strong, broadly applicable result and it directly
enables scale-to-zero and burst scale-up. Treat win #2 as a bonus that pays off specifically for
KVM deployments running agents with large, actively-used shared working sets. Do not gate the
project on the density number.

## What I built

A base/delta extension to gVisor's page allocator plus the checkpoint/restore CLI plumbing:

- `pkg/sentry/pgalloc`: a shared base file mapped copy-on-write under each sandbox's guest
  memory, delta computation, base-aware save/decommit. (`benchmarking/gvisor3-s1.pgalloc.patch`)
- `runsc checkpoint --shared-base`: produces a shared `base.img` plus a delta-only checkpoint.
  (`benchmarking/c1b-checkpoint-plumbing.patch`)
- `runsc restore` over a `base.img`: clones share the base, each keeps only its delta.
  (`benchmarking/c1-restore-plumbing.patch`)

All validated end to end through the real `runsc` CLI on a GCE nested-virtualization instance
with working `/dev/kvm`. Details and the full requirement ledger live in
`benchmarking/density-prototype-findings.md` and `benchmarking/gvisor-changes-for-production.md`.

## Win 1: fast, cheap starts

The cold start of a Python agent is dominated by importing the framework. For google-adk that is
about 1,391 modules and heavy module-init (most of the time is the Gemini SDK building its
Pydantic type hierarchy), amplified by gVisor's per-syscall cost on a file-heavy import. It is
not network fetching (packages are pre-installed and the sandbox has no network) and not bytecode
compilation (every `.pyc` is present). Restore skips all of it by mapping the already-initialized
memory image.

Measured (real ADK agent, serving-to-serving):

| platform | cold start | restore | per-start CPU: cold vs restore |
| --- | --- | --- | --- |
| KVM | 11.4s wall / 7.1s CPU | 0.6s | 7.1s vs ~1.5s |
| systrap | 4.05s wall / 1.99s CPU | 0.18s | 1.99s vs 0.26s |

Stress tests aimed at breaking it:

- **Warm, not a facade.** The restored agent serves its first real request in ~0.6s, the second
  in ~29 ms, steady at ~20 ms. There is no multi-second fault-in stall. (An in-agent timer read
  3,085 ms for the first request, but that was an artifact of the timer spanning the checkpoint
  freeze; the external clock and the 29 ms second request disproved a real stall.)
- **Correct.** Every post-restore response verified through the full agent path, and heap state
  came back bit-exact (a running accumulator continued exactly across the restore).
- **Platform-independent.** The win holds on systrap, which needs no `/dev/kvm`, so it applies to
  essentially every gVisor deployment (unlike the density win).
- **Cheap to park.** `checkpoint` takes 0.11s, and with a shared base each parked agent costs
  about 248 KB (kernel state plus a near-zero delta) versus about 81 MB for a full checkpoint.
  A pool of 10,000 parked agents is roughly 2.5 GB instead of roughly 810 GB, about 300x less.

The one real caveat, from the burst test: N concurrent restores are CPU-throughput-bound. On 4
vCPUs, 8 restores all serve under 2s, 50 take about 18s, and 100 take about 42s (with a few
stalling). Each restore plus its first request costs roughly 1.5s of CPU, so burst wall time is
about N times 1.5s divided by cores. The honest claim is therefore "about 4x cheaper CPU per
start and about 4x more start-throughput per core, at all scales," not "spin up 100 agents in two
seconds." Provision cores for the burst rate; restore makes each start much cheaper, not instant.

## Win 2: higher memory density

With a shared base, N clones share one resident copy of the common memory and keep only their
private delta. Measured flatten (sum of would-be-private RSS over actually-resident PSS):

- Synthetic 256 MB touch-all base, N=8: 5.7x.
- Density ceiling test, 128 MB base: about 3.9x, and roughly 300 sandboxes fit on a 15 GB / 4
  vCPU box (comfortable at 150 to 200 by load).
- Real ADK agent, ~80 MB base, N=8: 2.88x.

Two honest limits surfaced:

- **It only helps the resident hot working set.** Padding the agent to a 1 GB allocation did not
  raise the flatten, because memory that is never touched after restore is never faulted resident,
  so it costs nothing and there is nothing to share. Density scales with the shared *resident*
  set, not allocated size.
- **The per-sandbox floor is the ceiling.** The marginal cost of each sandbox is about 20 MB
  (about 17 MB sentry plus about 3 MB gofer), and roughly half of that is live Go runtime across
  two Go processes. It is not GC-tunable (structural: threads, goroutine stacks, runtime arenas),
  and the sentry is a full kernel (about 277k lines, 645 syscalls), so it is largely irreducible.
  The runsc binary itself is shared across all sandboxes, so it is a one-time cost, not marginal.
  Rewriting the sentry in Rust would shave roughly 4 MB of the runtime slice and nothing of the
  kernel working set, which does not justify a multi-year rewrite; the one tractable floor win is
  reaping the directfs gofer (about 3 MB), which directfs already sidelines at runtime.

CPython reference counting, which normally dirties shared pages and breaks copy-on-write, did not
hurt here: Python 3.12 immortal objects held the per-clone delta to about 4 MB. The mechanism is
sound; the flatten is modest because an 80 MB base is small relative to the 20 MB floor. Density
pays off when the shared resident base is large relative to that floor.

Note: the base/delta overlay is guest-correct on KVM (the platform maps the guest from the same
sentry mapping the overlay modifies) but not on systrap (a separate stub maps a per-sandbox memfd
the overlay never touches). So density is KVM-only for now; systrap would need a different
approach (kernel-samepage-merging on the memfds).

## What would change the conclusion

- If most agents are short-lived and cold-start-bound, win #1 is decisive and this is worth
  building. That is the expected agent-FaaS shape.
- If agents are long-lived and rarely restart, win #1 matters less and the case rests on density,
  which is the weaker, KVM-only result.
- If agents carry a large, actively-used shared working set (a loaded model or index read every
  turn), density becomes large and both wins compound. I did not measure that end to end; it is
  the one open experiment that would strengthen the density case.

## Bottom line

Snapshot/restore with a shared base is worth building for this substrate, primarily for fast and
cheap starts, which is a robust, platform-independent, correctness-verified win that directly
enables scale-to-zero and burst scale-up. The memory-density win is real but conditional (KVM,
large resident shared base) and should be treated as upside rather than the justification. The
per-sandbox floor, not the language or the sharing mechanism, is the fundamental density limit.

## Reproducibility

- Patches: `benchmarking/gvisor3-s1.pgalloc.patch`, `benchmarking/c1-restore-plumbing.patch`,
  `benchmarking/c1b-checkpoint-plumbing.patch`.
- Full measurements and method: `benchmarking/density-prototype-findings.md` (sections 9 to 13f).
- Production requirement ledger: `benchmarking/gvisor-changes-for-production.md`.
- Environment: GCE `n2-standard-4`, nested virtualization enabled, Ubuntu 24.04, real `/dev/kvm`;
  gVisor at commit `928199eb9`; agent is google-adk 2.3.0 on Python 3.12 with a mock LLM.
