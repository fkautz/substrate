# Evidence Log: Verification of `state-store.md`

This document verifies every factual claim in [`state-store.md`](./state-store.md) against
primary source (code, manifests, committed docs) as of `main`, 2026-06-23. Each claim is
marked:

> Provenance: verified against `main` at commit
> `d0c7f572144e46ff3974d57dbebd9720c539fa60`
> (`gitoid:commit:sha1:d0c7f572144e46ff3974d57dbebd9720c539fa60`). The tracked (non-doc) code
> was clean against that commit, so every `file:line` citation resolves there.
> Co-created using Claude Opus 4.8.


- ✅ **Confirmed** — verified against cited (or corrected) source.
- ⚠️ **Confirmed with nuance** — true, but the citation or phrasing needs a caveat.
- ❌ **Incorrect** — claim or citation does not hold; fix needed.

## Summary of findings

The document is substantially accurate. Issues found:

1. ❌ **One citation error.** `state-store.md` cites the 30s lock TTL as
   `controlapi/workflow.go:95`. Line 95 is `runStep` (backoff logic). The 30s lock is actually
   at `workflow.go:156` (Resume), `:185` (Suspend), `:214` (Pause), constructed in
   `acquireActorLock` at `:234`. **Fix the citation.**
2. ⚠️ **One source-code bug surfaced** (not a doc error). The `ResumeActor` comment at
   `workflow.go:155` reads "Lock TTL is 7 seconds" but the code passes `30*time.Second`. The
   doc correctly says 30s; the *code comment* is stale. Worth fixing in code.
3. ⚠️ **"no container `resources`" nuance.** A naive `grep resources: valkey.yaml` hits line 144,
   but that block is the `volumeClaimTemplates` storage request, not the valkey container. The
   claim (no container CPU/memory requests/limits) is correct.
4. ⚠️ **miniredis "cluster commands" claim is inferential.** No code comment states it; the
   evidence is that tests wrap a single miniredis in a `ClusterClient`, and miniredis does not
   implement Redis Cluster. The risk claim holds; it is not a quoted comment.
5. ⚠️ **"no eviction policy" was imprecise; corrected.** Empirically, `maxmemory-policy` defaults
   to `noeviction`, so an eviction policy technically exists — but `maxmemory` is unset (0 =
   unlimited), so it never engages. The real gap is the missing `maxmemory` cap, and the failure
   mode is a **kernel OOM-kill of the shard**, not a graceful `noeviction` write-error. Docs
   updated to say this; Phase 0 reworded so "set `maxmemory`" is the key task.

Everything else is confirmed verbatim.

---

## TL;DR section

| Claim | Verdict | Evidence |
|---|---|---|
| Store holds exactly three things: Actor records, Worker records, workflow locks | ✅ | `ateredis.go:83` (`actor:`), `:87` (`worker:`), `workflow.go:235` (`lock:actor:`). No other key prefixes in `ateredis.go`. |
| System of record, but re-derivable from etcd + node reality via Pod→Worker syncer | ✅ | `syncer.go:96` `syncWorkerToStore`, `:117` `CreateWorker`, `:74`/`:106` `DeleteWorker`, `:162` `releaseActorOnDeadWorker` — workers are created/deleted from Pod events. |
| Everything in RAM; AOF recovery-only; records have no TTL | ✅ | `valkey.yaml:38` `appendonly yes` (recovery journal); records written via `SetNX(..., 0)` = no expiry: `ateredis.go:140` (actor), `:164` (worker). In-RAM is the Valkey engine model. |
| Cannot update an Actor and its Worker in one transaction (cross-slot) | ✅ | `ateredis.go:30-35`: "not possible for a single 'action' to touch keys that hash to … different cluster slots … not possible to atomically mark an actor as scheduled … and the worker as busy." |
| All access via `store.Interface`; optimistic concurrency via per-record `version` | ✅ | `store.go:41-86` (interface); `:48`,`:63` document "optimistic concurrency check … ErrPersistenceRetry on version mismatch." |
| North star: 100ms p95, 1B active+idle, 1000/s | ✅ | `architecture.md:149` (100ms p95), `:150-152` (1 billion, "active and idle"), `:153-155` (1000/second). Quoted exactly. |
| ~0.76 KB per actor (measured) | ✅ | Benchmark, this session (see [Benchmark reproduction](#benchmark-reproduction)). |
| Deployment leaves maxmemory unset, no resource limits, PDB, anti-affinity | ✅ / ⚠️ | `valkey.yaml`: `maxmemory` 0×, PDB 0× (none in all of `manifests/`), `affinity` 0×; container has no `resources` (the only `resources:` is the PVC at `:139-146`). Empirically `maxmemory`→0, `maxmemory-policy`→`noeviction` (default), `appendfsync`→`everysec` (default), verified by loading the directives into `valkey-server` and reading them back. |

## "What is stored"

| Claim | Verdict | Evidence |
|---|---|---|
| Actor key `actor:<actor-id>` | ✅ | `ateredis.go:82-84` `return "actor:" + id`. |
| Actor value is JSON-serialized proto; holds version/status/template/placement/snapshot/selector | ✅ | proto `ateapipb.Actor` (`pkg/proto/ateapipb/ateapi.proto:86-145`); store comment `ateredis.go:17-19` "stored as … JSON-serialized objects." |
| `DeleteActor` succeeds only when SUSPENDED | ✅ | `ateredis.go:282` `if currentActor.GetStatus() != ateapipb.Actor_STATUS_SUSPENDED`. |
| Worker key `worker:<namespace>:<pool>:<pod>` | ✅ | `ateredis.go:86-88`. |
| Empty `actor_id` means free; synced from Pods; no TTL | ✅ | assignment fields in `ateapipb.Worker`; sync in `syncer.go:117`; `SetNX(...,0)` at `:164`. |
| Locks: per-actor key, UUID value, `SET NX`+TTL, compare-and-delete Lua | ✅ | key `workflow.go:235`; value `:236` `uuid.New()`; `AcquireLock` `ateredis.go:570` `SetNX(ctx, key, value, ttl)`; Lua `:579-580` `if redis.call("get"…)==ARGV[1] then return redis.call("del"…)`. |
| Lock has a 30s TTL | ✅ (citation corrected) | `workflow.go:156`/`:185`/`:214` pass `30*time.Second`. Doc citation fixed from the erroneous `:95` to `:156` (via `acquireActorLock` `:234`). |

## "How it's deployed"

| Claim | Verdict | Evidence |
|---|---|---|
| 6-replica StatefulSet of `valkey/valkey:8.0` | ✅ | `valkey.yaml:78` `replicas: 6`, `:90` `image: valkey/valkey:8.0`. |
| `--cluster-replicas 1` ⇒ 3 primary + 3 replica | ✅ | `valkey.yaml:203` `--cluster-replicas 1` over 6 nodes. |
| mTLS via projected pod certificates | ✅ | `valkey.yaml:28-32` (tls-* directives), `:124-130` (`podCertificate` projected volume). |
| `appendonly yes`, `cluster-node-timeout 5000`, 1Gi PVC | ✅ | `:38`, `:37`, `:146` `storage: 1Gi`. |
| One-shot init Job hardcodes the 6-node list | ✅ | `valkey.yaml:175-204` (`for i in 0 1 2 3 4 5` building the node list, single `--cluster create`). |
| Client setup in `main.go` `connectRedis`, TLS, optional Google IAM auth | ✅ | `main.go:206` `func connectRedis`, `:236` `redis.NewClusterClient`, `:56` `--redis-use-iam-auth`. |
| Local dev address `valkey-cluster.ate-system.svc:6379` | ✅ | `hack/install-ate.sh:176`. |
| `dev/valkey-direct-access.md` exists | ✅ | file present. |
| Gaps: no maxmemory/maxmemory-policy/container resources/PDB/anti-affinity; appendfsync default everysec | ✅ / ⚠️ | none of those strings appear in `valkey.yaml`; the only `resources:` is the PVC (`:139-146`); no `appendfsync` directive ⇒ Valkey default `everysec`. |

## "Constraints that shape the data model"

| Claim | Verdict | Evidence |
|---|---|---|
| No cross-slot atomicity (cite `:30-35`) | ✅ | `ateredis.go:30-35`, quoted above. |
| Lua must pre-declare keys; worker status denormalized into Actor (cite `:25-28`) | ✅ | `ateredis.go:25-28`: "a lua script must predeclare all keys … This is why we store the worker status inline in the Actor." |
| In-RAM, no TTL on records | ✅ | `SetNX(...,0)` `ateredis.go:140`,`:164`. |
| Optimistic concurrency: version-checked write on `version`; `UpdateActor` `:303-359`; mismatch → `ErrPersistenceRetry` | ✅ | `UpdateActor` at `:303`; version check `:324-325` `if currentActor.GetVersion() != expectedVersion { return store.ErrPersistenceRetry }`; via `Watch` `:310`. |
| Lua may not be ACID under power failure (cite `:37-39`) | ✅ | `ateredis.go:37-39`: "Redis Lua is not ACID --- power failure … may leave us with half of the effects." |
| Worker claim = two version-checked writes: scan `:135`, claim `:178`, bind `:189`, repair `:142-161` | ✅ | `workflow_resume.go:135` `ListWorkers`, `:178` `UpdateWorker` (claim), `:189` `UpdateActor` (bind), repair block `:142-161` (stale-release `UpdateWorker` at `:158`). |

## "The numbers that matter"

| Claim | Verdict | Evidence |
|---|---|---|
| Activation latency target 100ms p95 (definition: wakeup → can receive traffic) | ✅ | `architecture.md:147-149` exactly. |
| Scale target 1 billion, "active and idle" | ✅ | `architecture.md:150-152` exactly. |
| Throughput target 1000/sec | ✅ | `architecture.md:153-155` exactly. |
| Measured 704 B `MEMORY USAGE`, 721 B `used_memory`, 762 B RSS; libc malloc; standalone | ✅ | This session's benchmark; see below. |
| Capacity table (10M/100M/1B → ~38GB/380GB/3.8TB at ~5×) | ✅ | Arithmetic from 0.76 KB × replication × headroom; consistent with measured per-record cost. |

## "Known sharp edges"

| Claim | Verdict | Evidence |
|---|---|---|
| `ListActors`/`ListWorkers` are O(keyspace) scans; no secondary indexes | ✅ | `ateredis.go:367` `master.Scan(ctx, 0, "worker:*", 0)` per shard via `ForEachMaster` `:366`; `ListActors:429` SCAN-based. No index structures in the package. |
| `ListWorkers` is on the resume hot path | ✅ | `workflow_resume.go:135` calls it inside `AssignWorkerStep.Execute`. |
| Workflow lock not failover-safe; correctness must come from the `version` fence | ✅ | single-instance `SetNX` `ateredis.go:570`; TTL is best-effort (release comment `workflow.go` "the lock TTL is the safety net"); the version-checked write is the actual guard (`:324-325`). |
| `ListActors` page tokens embed a SHA-256 shard hash | ✅ | `ateredis.go:44` `crypto/sha256`, `:425` `h := sha256.Sum256([]byte(addr))` inside page-token encoding. |
| Tests don't exercise cluster semantics; miniredis can't run cluster commands | ⚠️ | Tests construct `redis.NewClusterClient` over a single `miniredis` (`storetest.go:36` + `:31` `miniredis.Run()`; also `functional_test.go:252`). No code *comment* asserts the limitation; it is a known property of miniredis (no Redis Cluster impl), so cross-slot/failover paths are genuinely unexercised. Claim true; citation is inferential, not a quote. |

## "Replacing Valkey"

The replacement direction (portable Postgres-dialect store, `FOR UPDATE SKIP LOCKED` claim,
version-as-fencing-token, Cockroach/Yugabyte scale-out, re-derivable migration) is a **design
recommendation**, not a claim about existing code, so it is not "verifiable" against source.
Its *premises* are the verified facts above (cross-slot limitation, RAM cost, no indexes,
re-derivability). The `store.Interface` seam that makes the swap mechanical is real
(`store.go:41-86`).

## "Where the code is" table

All paths confirmed to exist and match described purpose: `store.go`, `ateredis.go`,
`main.go` (`connectRedis` `:206`), `syncer.go`, `workflow_resume.go`, `workflow.go`,
`valkey.yaml`, `storetest.go`, `docs/dev/valkey-direct-access.md`, `architecture.md:142-155`.

---

## Benchmark reproduction

The ~0.76 KB/record figure is reproducible:

1. `valkey-server --port 6399 --save '' --appendonly no --daemonize yes`
2. Load 1,000,000 protojson-faithful `ateapipb.Actor` records (key `actor:<uuid>`, value
   ~568 B JSON with camelCase fields, int64-as-string, enum names, realistic `gs://` snapshot
   URIs and selectors) via `valkey-cli --pipe`.
3. Measure: `valkey-cli -p 6399 info memory` (`used_memory`, `used_memory_rss`) and
   `MEMORY USAGE` on sample keys, minus an empty-DB baseline.

Result (this session): baseline 1.22 MB; after 1M keys `used_memory` 722,760,496 B ⇒
**721 B/record** (`used_memory`), **762 B/record** (RSS, frag 1.06), **704 B** per
`MEMORY USAGE`. Caveats recorded in the doc: Homebrew build uses `libc` malloc (prod
`valkey/valkey:8.0` ships jemalloc, expect ~750–850 B) and the run was standalone (prod
doubles for the replica).

---

## Corrections applied to `state-store.md`

1. ✅ Lock-TTL citation changed from `controlapi/workflow.go:95` to `controlapi/workflow.go:156`
   (via `acquireActorLock` `:234`).
2. ✅ "no eviction policy" reworded throughout to "`maxmemory` unset (0/unlimited); default
   `noeviction` never engages; growth ends in a kernel OOM-kill," and Phase 0 reworded so
   "set `maxmemory`" is the key task.

Still open, in source (separate from the docs): the stale `ResumeActor` comment at
`workflow.go:155` ("Lock TTL is 7 seconds" → 30 seconds).
