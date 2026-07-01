# Pre-Alpha Demo Priorities

**Status: strategy note.** The top-5 highest-value efforts for getting Agent
Substrate to a compelling pre-alpha demo. Grounded in [`spec.md`](spec.md) (and
its gap register), [`threat-model.md`](threat-model.md) (`T-NN`/`GAP-NNN`), and
[`roadmap.md`](roadmap.md).

## Framing

The bet: the demo that sells this system is

> **A stateful agent that costs nothing while idle, wakes in under a second with
> full memory + filesystem intact, multiplexed hundreds-to-a-handful-of-nodes,
> and can branch its own state.**

Everything below ladders up to that sentence. Per the roadmap's #1 priority
("pinning down architectural decisions that influence the rest"), the picks are
weighted by **demo impact × architectural irreversibility**, and they
**deliberately leave the security and reliability white space** (see
[Deferred](#what-were-deliberately-deferring)).

---

## The top 5

### 1. Lock the snapshot / state-data model (the "State Root") — highest leverage

The foundation under everything else, and the hardest thing to change later: the
format is encoded across the snapshot blobs, the `Actor` record
(`latest_snapshot_info` oneof), and atelet's image movement. Today a snapshot is
a **monolithic `checkpoint.img`** (full memory + fs), EXTERNAL-to-blob or
LOCAL-node-pinned (spec §5.1) — no layering, no incrementality, no tiering
(GAP-059). The roadmap flags the open decisions: *"clarify what data is retained
across which events,"* *"distinct lifecycle for rootfs and memory vs. working
space — needs API surface of where to mount,"* and the run-mode menu (clean-OCI /
golden / persist-rootfs / persist-rootfs+memory).

- **Decision to lock now:** a layered, content-addressed, lineage-aware model —
  immutable base (golden/rootfs) + memory image + mutable working-data volume,
  each independently addressable, with a stable parent pointer.
- **First cut:** define it as the snapshot manifest + `SnapshotInfo` schema, and
  make resume tier the memory image (local SSD/peer → blob).
- **Demo payoff:** the sub-second wake (roadmap priority #2).
- **Why now:** get this wrong and forking, tiering, incremental snapshots, and
  run-modes all become a v2 rewrite.

### 2. Make the on-demand activation path solid and warm-cached — the demo's beating heart

"Curl a cold URL → it wakes → serves you" *is* the demo, and it's currently
half-wired: the resumer's 15s detached timeout exceeds the 5s ext_proc timeout so
a slow first hit fails at Envoy (GAP-035), the forwarded worker port is hardcoded
to 80 (GAP-034), the router fetches ActorTemplates and discards them (GAP-036),
and **there is no caching of the resolved worker IP — every inbound request
triggers a fresh `ResumeActor`** (spec §6.3).

- **Decision to lock now:** the data-plane↔control-plane contract for warm vs.
  cold (where activation state lives — a router hot cache vs. the control plane).
  This is the literal embodiment of the "keep the k8s scheduler out of the hot
  path" thesis.
- **First cut:** hold/stream the request across resume without tripping the
  timeout; cache actor→workerIP so warm requests bypass the control plane; make
  the forwarded port configurable.
- **Demo payoff:** cold wake impresses once; warm round-trips make it feel like a
  product.

### 3. Worker autoscaling / elastic warm pools — proves the thesis on stage

The whole value prop is density and reclaiming idle resources, but `replicas` is
static and there is no autoscaler (GAP-057). Without elasticity you can
*describe* the efficiency story but not *show* it.

- **Decision to lock now:** the scaling signal + control loop (free-worker
  watermark / pending-resume depth) and the fungible-pool placement model — a
  high-leverage scheduler decision.
- **First cut:** a watermark autoscaler that keeps N warm workers and scales the
  pool Deployment.
- **Demo payoff:** "200 idle agents on 5 nodes; I spike traffic, the pool grows,
  idle ones cost ~nothing." The slide that justifies the project's existence.

### 4. Actor forking/cloning from a checkpoint — the differentiator

Roadmap: *"branch a new logical actor from an existing checkpoint (the 'State
Root') to support complex agent reasoning paths."* The most *memorable* demo
moment, and uniquely agentic — fork an agent mid-conversation into N speculative
reasoning branches, or A/B a prompt change against live state. No serverless-
container platform does this well.

- **Decision to lock now:** fork semantics — copy-on-write working data over a
  shared immutable base, new actor identity with a parent-snapshot pointer (this
  is exactly why #1 lands first).
- **First cut:** `create actor --from <actor@snapshot>` that boots a child from a
  parent's latest checkpoint (a copy is fine for v1; CoW is the optimization).
- **Demo payoff:** "watch me branch this agent's brain three ways."

### 5. A2A calling + MCP / framework hosting — "agents as callable services"

For a demo *to people* (devs/partners), the integration narrative lands: host an
MCP server as an actor that suspends when idle and wakes on a tool call; let
agent A call agent B by name. The fabric is most of the way there — every actor
is addressable at `<id>.actors.resources.substrate.ate.dev` and resolves through
the router (spec §3, §6), and the in-pod masquerade currently permits the egress
(good enough for a trusted demo).

- **Decision to lock now:** the A2A addressing/identity contract (auth deferred
  per the white space, but the *naming + in-band routing* is the leverage).
- **First cut:** prove actor→actor resume-on-call over the existing DNS mesh, plus
  one concrete integration (MCP hosting is the timely pick).
- **Demo payoff:** a mesh of agents that wake each other on demand — the platform
  feels like an ecosystem, not just a runtime.

---

## What we're deliberately deferring

This is the white space, called out honestly rather than left to look solved.

- **AuthN/authz and network egress policy.** The threat model's headline (KC-1)
  is that an untrusted actor can reach the cluster with **no gVisor escape**,
  because there is no authn/authz (GAP-002) and no NetworkPolicy (GAP-050).
  Deferring this is acceptable for a demo **only if you run trusted workloads in
  an isolated cluster.** Say that out loud.
- **Snapshot signing / integrity** (T-14), **HA / multi-replica** (GAP-001), and
  **full control-plane sharding to 1B actors** (GAP-060).
  - Nuance on the last: the *encoding/key-scheme* decisions for the store (the
    binary-protobuf record work, plus hash-tag sharding) are cheap to get right
    now and expensive later — **pin the key design even though the scale won't be
    demoed.**

## Honorable mentions (cheap-to-pin decisions, not demo-built)

- **Sandbox-runtime abstraction (gVisor ↔ microVM).** Runtime modularity is
  roadmap #6 and microVM is actively in flight. Keep the `ateom` interface clean
  now so the isolation upgrade is a swap, not a fork.
- **The actor "run modes" menu** — really a sub-decision of #1; choosing which of
  the four modes to support (and phasing them) constrains the snapshot model, so
  resolve it alongside #1.

## Suggested demo storyline

A single narrative that exercises all five:

1. A stateful coding agent / MCP server is **idle and suspended** to object
   storage (costs ~nothing). *(#1)*
2. `curl` its URL → it **wakes in <1s** with full memory + filesystem intact and
   continues. *(#2)*
3. Show **200 of them on 5 nodes**; spike traffic; the **pool autoscales**. *(#3)*
4. **Fork** an agent's state to branch its reasoning. *(#4)*
5. **Agent A calls agent B** by name; B wakes on demand. *(#5)*
