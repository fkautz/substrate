# The Control-Plane State Store: Developer Guide

A reference for anyone working on or operating Agent Substrate's control-plane state store
(the Valkey cluster behind `ate-api-server`). It covers what's stored, the constraints that
shape the data model, the numbers that bound capacity, how we keep it healthy, its known
sharp edges, and the conditions under which we'd replace it. Code citations are `file:line`
so you can navigate.

> Scope: this is the **control-plane record store** only, meaning the live actor↔worker
> mapping. Snapshots live in object storage (GCS/S3); CRDs and config live in etcd via the
> kube-apiserver. Those are out of scope here except where they interact.

> Provenance: analyzed against `main` at commit
> `d0c7f572144e46ff3974d57dbebd9720c539fa60`
> (`gitoid:commit:sha1:d0c7f572144e46ff3974d57dbebd9720c539fa60`), 2026-06-22.
> All `file:line` citations are valid against that commit.
> Co-created using Claude Opus 4.8.

---

## TL;DR (read this if nothing else)

- The store holds three things in Valkey: **Actor records, Worker records, and short-lived
  workflow locks**. Nothing else. (`cmd/ateapi/internal/store/ateredis/ateredis.go`)
- It is the **system of record for live placement**, yet its contents are **re-derivable**
  from etcd plus node reality (the Pod→Worker syncer). Treat it as a *durable cache of
  derivable truth*, not an irreplaceable ledger.
- **Everything is in RAM.** Disk (AOF) is recovery-only; it does not extend capacity. Records
  have **no TTL**, so the keyspace only grows.
- Two constraints shape everything you build on it. First, Valkey cluster can't atomically
  touch keys in different hash slots, so you **cannot** update an Actor and its Worker in one
  transaction. Second, all access goes through the `store.Interface` seam, with optimistic
  concurrency via a per-record `version`.
- The north-star targets are concrete: **100ms p95 activation, 1 billion agents (active and
  idle), 1000 wakeups/sec** (`docs/architecture.md:142-155`). Measured cost is **~0.76 KB per
  actor**, so at the 1B target that's a multi-TB memory fleet. That is why capacity is the
  thing to watch.
- The current deployment leaves **`maxmemory` unset (0/unlimited), no resource limits, no PDB,
  and no anti-affinity** (`manifests/ate-install/valkey.yaml`). With no `maxmemory`, the default
  `noeviction` policy never engages, so growth ends in a **kernel OOM-kill of the shard**, not a
  catchable write-error. Fixing that is the near-term work (see
  [Avoiding the capacity wall](#avoiding-the-capacity-wall)).

---

## What is stored

All three live in the one Valkey cluster in `ate-system`, accessed only via the
`store.Interface` (`cmd/ateapi/internal/store/store.go:41-86`).

### Actor records
- **Key:** `actor:<actor-id>` (`ateredis.go:82-84`)
- **Value:** JSON-serialized `ateapipb.Actor` proto.
- **Holds:** `version` (optimistic-concurrency counter), `status`
  (SUSPENDED/RESUMING/RUNNING/SUSPENDING/PAUSING/PAUSED), template ref
  (`actor_template_namespace`/`name`), current placement
  (`ateom_pod_namespace`/`name`/`ip`/`uid`, `worker_pool_name`), snapshot pointers
  (`latest_snapshot_info`, `in_progress_snapshot`), and `worker_selector`.
- **No TTL.** Removed only by `DeleteActor`, which succeeds only when SUSPENDED.

### Worker records
- **Key:** `worker:<namespace>:<pool>:<pod>` (`ateredis.go:86-88`)
- **Value:** JSON-serialized `ateapipb.Worker` proto.
- **Holds:** `version`, immutable identity (`worker_namespace`/`pool`/`pod`, `ip`, `node_name`,
  `pod_uid`), and the mutable assignment (`actor_id`/`actor_namespace`/`actor_template`; an
  empty `actor_id` means free).
- **Re-derivable:** kept in sync from Kubernetes Pods by the WorkerPool syncer
  (`cmd/ateapi/internal/controlapi/syncer.go`). This is why the store is a cache, not a ledger.
- **No TTL.**

### Workflow locks
- **Key:** caller-supplied per-actor lock. **Value:** a unique token (UUID).
- `SET NX` plus TTL to acquire, compare-and-delete Lua to release (`ateredis.go:569-591`); used
  to serialize per-actor resume/suspend workflows with a **30s TTL**
  (`controlapi/workflow.go:156`, via `acquireActorLock` at `:234`).
- **TTL'd**, unlike the records. See [the sharp edge on locks](#known-sharp-edges).

---

## How it's deployed

`manifests/ate-install/valkey.yaml`: a 6-replica StatefulSet of `valkey/valkey:8.0`,
`--cluster-replicas 1` (so **3 primary shards plus 3 replicas**), mTLS via projected pod
certificates, `appendonly yes` (AOF), `cluster-node-timeout 5000`, a 1Gi PVC per node, and a
one-shot init Job that hardcodes the 6-node list.

Client setup is in `cmd/ateapi/main.go` (`connectRedis`, TLS, optional Google IAM auth). Local
dev points at `valkey-cluster.ate-system.svc:6379` (`hack/install-ate.sh`). For interactive
access, see [`dev/valkey-direct-access.md`](./dev/valkey-direct-access.md).

**Config gaps to be aware of** (addressed in [Avoiding the capacity wall](#avoiding-the-capacity-wall)):
`maxmemory` is unset (0/unlimited), so the default `noeviction` policy never engages; no
container `resources`; no PodDisruptionBudget; no pod anti-affinity; and `appendfsync` is left
at the default `everysec`.

---

## Constraints that shape the data model

If you add a feature that stores or mutates control-plane state, these are the rules of the
road. They are not incidental; they already dictate the current schema.

1. **No cross-slot atomicity (the big one).** In Valkey cluster, a single action cannot touch
   keys that hash to different slots. This is a **cluster** property and it covers *every*
   multi-key mechanism: the `WATCH`/`MULTI` transactions the store actually uses (`UpdateActor`
   `ateredis.go:310`, `DeleteActor` `:268`, `UpdateWorker` `:205`) and Lua scripts alike. So you
   **cannot** atomically mark an actor scheduled and its worker busy (`ateredis.go:30-35`).
   Multi-object invariants must be ordered carefully and repaired by reconciliation, not assumed
   atomic.
2. **In-RAM, no TTL on records.** Capacity is bounded by memory, and the keyspace grows until
   something deletes it. Anything you add to a record is paid for once per actor in the
   population.
3. **Optimistic concurrency only.** Updates are **version-checked writes**: the write lands
   only if the record's `version` still matches what you read (a compare-and-swap on the
   `version` field, not content-addressable storage), else `ErrPersistenceRetry` and the
   workflow step retries (`UpdateActor` `ateredis.go:303-359`). There is no pessimistic locking
   in the store beyond the advisory workflow lock.

> **A note on Lua.** The store uses Lua in exactly one place: the lock-release
> compare-and-delete script (`ateredis.go:578-586`). Everything else is `WATCH`/`MULTI`. Two
> consequences. First, a Lua-specific restriction (a script must pre-declare every key it
> touches, so it can't follow a value to a second key) shaped the schema: worker status is
> **denormalized into the Actor** to keep Lua manipulation possible (`ateredis.go:25-28`).
> But since CRUD doesn't use Lua today, treat that as a kept-open design option, not a live
> constraint (the cross-slot rule above justifies the same denormalization regardless). Second,
> the source notes Lua may not be ACID under power failure (`ateredis.go:37-39`); that matters
> only for the lone lock-release script, whose worst case is a lock that outlives its delete and
> then expires via TTL anyway.

### Worked example: the worker claim
The single most important operation, claiming a free worker and binding the actor to it, is
constrained by rule 1, so `AssignWorkerStep` does it in **two separate version-checked writes**:
a full `ListWorkers()` scan to find a free one (`workflow_resume.go:135`), `UpdateWorker` to
claim (`:178`), then `UpdateActor` to bind (`:189`). If the process dies between the two writes the
worker is claimed but the actor doesn't know, which is exactly why the repair block at
`workflow_resume.go:142-161` exists. Understanding this pattern is essential before you touch
the resume path.

---

## The numbers that matter

### North-star targets (`docs/architecture.md:142-155`)
| Metric | Target |
|---|---|
| **Activation latency** (wakeup event to can-receive-traffic) | **100ms at p95** |
| **Scale** (agents, **active and idle**, per cluster) | **1 billion** |
| **Throughput** (wakeups/sec per cluster) | **1000/sec** |

"Active **and idle**" is the key phrase: the idle/suspended long tail counts toward the 1B,
and it all lives in RAM.

### Measured footprint (benchmark, 2026-06-23, commit `d0c7f57`)
1M protojson-encoded `ateapipb.Actor` records loaded into Valkey measured **~0.76 KB resident
per actor** (704 B by `MEMORY USAGE`, 721 B by `used_memory`, 762 B by RSS), benchmarked against
`main` at commit `d0c7f572144e46ff3974d57dbebd9720c539fa60` (the same commit as the provenance
block above). Two caveats push production slightly higher: the benchmark used `libc` malloc (prod
ships jemalloc, closer to 750–850 B) and was standalone (prod doubles it for the replica).
**Re-run this as the proto evolves** (and record the new commit), since it drives the runway
formula.

### Capacity, with replication and headroom (about 5x)
| Actors | Raw | Provisioned RAM (~5x) | ~$/mo¹ |
|---|---|---|---|
| 10 M | 7.6 GB | ~38 GB | ~$190–380 |
| 100 M | 76 GB | ~380 GB | ~$1.9k–3.8k |
| 1 B (north star) | 760 GB | ~3.8 TB | ~$19k–38k |

¹ Cost basis is self-hosted memory-optimized VM RAM, about $5–10/GB-month per *provisioned*
GB (replicas and headroom are already in the 5x column), matching the in-cluster Valkey
StatefulSet this system actually runs. A *managed* HA service would instead run about
$22–62/GB-month of *usable* data (AWS ElastiCache Valkey reserved cheapest at ~$22, GCP
Memorystore Standard ~$39, Azure Premium ~$62; figures from 2026 provider pricing), several
times higher, so the relative argument only strengthens if this ever moves to a managed
service. The 1–5 ms latency of an in-region
disk-based store would be noise against the 100ms budget, which is relevant to the
[replacement question](#replacing-valkey).

---

## Avoiding the capacity wall

The failure mode to design against: with `maxmemory` unset and records having no TTL, the
keyspace grows until a shard exhausts pod RAM and the **kernel OOM-kills it, stalling every
resume/suspend**, with no warning. (The default `noeviction` policy never engages because
there is no `maxmemory` cap to trip it, so you don't even get a catchable write-error first.)
Setting `maxmemory` is what converts that kernel kill into a monitored, catchable condition;
the goal is to turn the silent OOM into a **monitored alert with weeks of lead time**.

**Runway is the number to dashboard:**
```
runway_days = (Σ usable_maxmemory_per_shard − used) / (per_record_bytes × actors_added_per_day)
```
with `per_record_bytes ≈ 0.76 KB`.

### Hardening checklist (cheapest and highest-leverage first)

**Phase 0, stop silent OOM (config-only):**
- [ ] Set `maxmemory` to ~60–70% of the pod memory limit. This is the key one: without it,
      growth is a kernel OOM-kill; with it, the default `noeviction` turns the limit into a
      catchable write-error instead.
- [ ] Confirm `maxmemory-policy noeviction` (it's the default, but set it explicitly: records
      are authoritative, so fail loud, never evict).
- [ ] Add container `resources` requests = limits (Guaranteed QoS).
- [ ] Make `Create/Update` surface a clean "retry later" on OOM (treat like `ErrPersistenceRetry`).

**Phase 1, see it coming:**
- [ ] Export `used_memory`/`rss`/`maxmemory`/`dbsize`/`mem_fragmentation_ratio` per shard.
- [ ] Dashboard "days-to-wall" from the runway formula.
- [ ] Alert: warn at 60% of `maxmemory`, page at 80%, and on any `noeviction` write-failure.
- [ ] Track per-record size as a live metric, not a constant.

**Phase 2, bound growth:** guarantee `Delete*` reclaims and orphaned workers are reaped
(`syncer.go`); define a retention/GC policy for terminal actors (no TTL today); trim per-record
bytes where cheap.

**Phase 3, make scaling routine:** adopt a Valkey operator (the current init Job is fixed at 6
nodes); write the reshard runbook; make `ListActors` tolerant of resharding (its page tokens
embed a shard hash); pre-provision ahead of the alert.

**Phase 4, harden availability:** add a PodDisruptionBudget (`maxUnavailable: 1`); add pod
anti-affinity / topology spread so primary and replica never co-locate; add `priorityClassName`;
revisit the 1Gi PVC and the `appendfsync` RPO.

**Phase 5, prove it:** load-test to `maxmemory` and assert a clean write error (not a crash);
integration-test failover against a real cluster (unit tests use `miniredis`, which can't run
cluster commands, per `storetest.go`); chaos-drill a primary kill or node drain.

The 80/20: **Phases 0 and 1 (eight tasks) are what actually avoid the wall.** The rest extends
runway and makes growth boring.

---

## Known sharp edges

Things not to assume when building on the store:

- **No multi-key transactions** (constraint 1). Any invariant spanning two records is
  eventually-consistent and reconciled, never atomic.
- **`ListActors`/`ListWorkers` are O(keyspace) scans** across shards, and `ListWorkers` is on
  the resume hot path today (`workflow_resume.go:135`). There are **no secondary indexes**, so
  a new query pattern means a scan or another hand-maintained denormalization.
- **The workflow lock is not failover-safe.** `SET NX` plus TTL on a single instance can let
  two workflows believe they hold the same actor's lock across a failover or a GC pause past
  the 30s TTL. **Correctness must come from the actor's `version` fence, not the lock.** Treat
  the lock as a contention optimization, not a safety mechanism.
- **`ListActors` page tokens embed a SHA-256 shard hash**, so a reshard or failover can
  invalidate in-flight iteration. Consumers should restart iteration, not error.
- **Tests don't exercise cluster semantics.** `miniredis` can't run cluster commands, so
  cross-slot and failover behavior, the riskiest parts, are not covered by unit tests.

---

## Whether (and when) to revisit the store

Valkey is the right tool for what it does now: a fast, mutable, point-lookup index of live
placement on the resume hot path, deliberately kept off the slower kube-apiserver. We are not
replacing it reactively. But the constraints above (no cross-key atomicity, authoritative state
in RAM, no indexes, RAM-priced cold tail) are structural, not tunable, so it is worth keeping
an eventual alternative in view.

**An open question worth exploring (if and when triggered):** is it worth investigating the
performance and cost dynamics of a **transactional, SQL-dialect store** behind the same
`store.Interface` seam? A PostgreSQL-compatible dialect is one candidate worth measuring,
backable by an engine such as **CockroachDB**, **YugabyteDB**, or **FoundationDB** (with a
SQL/records layer) for portability across clouds and on-prem. The properties to evaluate
against today's pain points: whether the worker claim collapses into **one transaction**
(e.g., `SELECT … FOR UPDATE SKIP LOCKED`) instead of a scan plus two separate version-checked
writes; whether a row `version`
can serve as a portable fencing token in place of a separate lock; and whether moving the
mostly-idle population from RAM pricing to disk pricing actually pays off once the added
latency (single-digit ms, likely noise against the 100ms budget) and operational cost are
accounted for. Migration risk is bounded because the records are re-derivable, which makes such
an exploration cheap to prototype. This is a question to study, not a settled direction.

**A second direction, hot/cold tiering** (raised with supporting benchmarks in
[#12](https://github.com/agent-substrate/substrate/issues/12)): rather than replace Valkey,
shrink what it holds. The idea is a **hybrid persistence model** where only the **hot set**, the
entities on the current resume hot path, stays resident in the "fast memory" Valkey cache, while
the mostly-idle long tail is demoted to a cheaper disk-backed tier and faulted back in on wakeup.
This attacks the cost driver named above directly: the RAM-priced cold tail is most of the 1B
population (the "active **and** idle" target), so tiering is where the memory bill actually lives.
It also composes with the SQL question above, since the cold tier could be that same transactional
store. The properties to measure: the hit rate of the hot set under realistic wakeup patterns, the
p95 cost of a cold-fault against the 100ms activation budget, and whether promotion/eviction stays
correct under the same `version` fence. Like the SQL question, this is a direction to study, not a
settled one.

**Triggers that would make this exploration worth starting** (instrument both):
1. The **first** runway/capacity alert that can't be answered by cheaply adding a shard, or
2. The **second** feature that needs an atomic multi-object invariant (locality index,
   snapshot-GC refcounts, storage tiering).

At one such invariant it's a managed defect; at two it's a pattern, and a transactional
alternative becomes worth seriously evaluating. Until then: harden Valkey, watch the runway,
keep state re-derivable.

---

## Where the code is

| Concern | Location |
|---|---|
| Store contract | `cmd/ateapi/internal/store/store.go` |
| Valkey implementation | `cmd/ateapi/internal/store/ateredis/ateredis.go` |
| Client / connection | `cmd/ateapi/main.go` (`connectRedis`) |
| Worker sync (re-derivability) | `cmd/ateapi/internal/controlapi/syncer.go` |
| Resume / claim workflow | `cmd/ateapi/internal/controlapi/workflow_resume.go` |
| Workflow lock usage | `cmd/ateapi/internal/controlapi/workflow.go` |
| Deployment | `manifests/ate-install/valkey.yaml` |
| Test harness (miniredis) | `cmd/ateapi/internal/store/storetest/storetest.go` |
| Interactive access | `docs/dev/valkey-direct-access.md` |
| North-star targets | `docs/architecture.md:142-155` |
