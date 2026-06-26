# Control API Requirements Catalog

Normative obligations of the gRPC `Control` service
(`cmd/ateapi/internal/controlapi/`). Series `API`. Each requirement is either
covered by a tagged test (`// Verifies: REQ-API-NNN`) or excused in
`waivers.md`. The generated matrix is `spec/traceability.md` (do not hand-edit).

Keywords follow RFC 2119. The `Section` field names the RPC the obligation
belongs to. "Error" notes the gRPC status code returned on violation.

## GetActor

### REQ-API-001 — GetActor requires actor_id
- Section: GetActor
- Keyword: MUST | Error: InvalidArgument
- Text: "GetActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-002 — GetActor returns NotFound for a missing actor
- Section: GetActor
- Keyword: MUST | Error: NotFound
- Text: "GetActor MUST return NotFound when no actor exists with the requested actor_id."

## CreateActor

### REQ-API-003 — CreateActor requires actor_template_namespace
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a request with an empty actor_template_namespace with InvalidArgument."

### REQ-API-004 — CreateActor requires actor_template_name
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a request with an empty actor_template_name with InvalidArgument."

### REQ-API-005 — CreateActor requires actor_id
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-006 — CreateActor validates actor_id format
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject an actor_id that is not a DNS-1123 label (1-63 chars, lowercase alphanumeric or '-', starting and ending alphanumeric) with InvalidArgument."

### REQ-API-007 — CreateActor bounds worker_selector label count
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a worker_selector with more than 10 match_labels entries with InvalidArgument."

### REQ-API-008 — CreateActor validates worker_selector label keys
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a worker_selector whose label keys are not valid Kubernetes qualified names with InvalidArgument."

### REQ-API-009 — CreateActor validates worker_selector label values
- Section: CreateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "CreateActor MUST reject a worker_selector whose label values are not valid Kubernetes label values with InvalidArgument."

### REQ-API-010 — CreateActor requires an existing template
- Section: CreateActor
- Keyword: MUST | Error: FailedPrecondition
- Text: "CreateActor MUST fail with FailedPrecondition when the referenced ActorTemplate does not exist."

### REQ-API-011 — CreateActor rejects a duplicate actor_id
- Section: CreateActor
- Keyword: MUST | Error: AlreadyExists
- Text: "CreateActor MUST fail with AlreadyExists when an actor with the same actor_id already exists."

### REQ-API-012 — CreateActor starts an actor SUSPENDED
- Section: CreateActor
- Keyword: MUST
- Text: "CreateActor MUST initialize a newly created actor to STATUS_SUSPENDED."

### REQ-API-013 — CreateActor initializes version to 1
- Section: CreateActor
- Keyword: MUST
- Text: "CreateActor MUST initialize a newly created actor's version to 1."

## UpdateActor

### REQ-API-014 — UpdateActor requires actor_id
- Section: UpdateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "UpdateActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-015 — UpdateActor validates worker_selector
- Section: UpdateActor
- Keyword: MUST | Error: InvalidArgument
- Text: "UpdateActor MUST validate the worker_selector (label count, key, and value rules) and reject an invalid one with InvalidArgument."

### REQ-API-016 — UpdateActor returns NotFound for a missing actor
- Section: UpdateActor
- Keyword: MUST | Error: NotFound
- Text: "UpdateActor MUST return NotFound when the target actor does not exist."

### REQ-API-017 — UpdateActor mutates only worker_selector
- Section: UpdateActor
- Keyword: MUST
- Text: "UpdateActor MUST treat actor_id, actor_template_namespace, actor_template_name, and status as immutable, updating only the worker_selector."

### REQ-API-018 — UpdateActor uses optimistic concurrency
- Section: UpdateActor
- Keyword: MUST
- Text: "UpdateActor MUST use version-based optimistic concurrency, retrying or aborting on a concurrent modification rather than overwriting it."

## DeleteActor

### REQ-API-019 — DeleteActor requires actor_id
- Section: DeleteActor
- Keyword: MUST | Error: InvalidArgument
- Text: "DeleteActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-020 — DeleteActor validates actor_id format
- Section: DeleteActor
- Keyword: MUST | Error: InvalidArgument
- Text: "DeleteActor MUST reject an actor_id that is not a valid DNS-1123 label with InvalidArgument."

### REQ-API-021 — DeleteActor returns NotFound for a missing actor
- Section: DeleteActor
- Keyword: MUST | Error: NotFound
- Text: "DeleteActor MUST return NotFound when the target actor does not exist."

### REQ-API-022 — DeleteActor requires SUSPENDED state
- Section: DeleteActor
- Keyword: MUST | Error: FailedPrecondition
- Text: "DeleteActor MUST fail with FailedPrecondition unless the actor is in STATUS_SUSPENDED."

### REQ-API-023 — DeleteActor uses optimistic concurrency
- Section: DeleteActor
- Keyword: MUST
- Text: "DeleteActor MUST perform the delete under version-based optimistic concurrency to avoid racing a concurrent state change."

## ResumeActor

### REQ-API-024 — ResumeActor requires actor_id
- Section: ResumeActor
- Keyword: MUST | Error: InvalidArgument
- Text: "ResumeActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-025 — ResumeActor returns NotFound for a missing actor
- Section: ResumeActor
- Keyword: MUST | Error: NotFound
- Text: "ResumeActor MUST return NotFound when the target actor does not exist."

### REQ-API-026 — ResumeActor is idempotent
- Section: ResumeActor
- Keyword: MUST
- Text: "ResumeActor MUST be safe to call repeatedly, converging an already-resuming or running actor toward RUNNING without error."

### REQ-API-027 — ResumeActor requires an eligible pool
- Section: ResumeActor
- Keyword: MUST | Error: FailedPrecondition
- Text: "ResumeActor MUST fail with FailedPrecondition when no worker pool matches the template's SandboxClass and the conjunction of the template and actor selectors."

### REQ-API-028 — ResumeActor requires a free worker
- Section: ResumeActor
- Keyword: MUST | Error: FailedPrecondition
- Text: "ResumeActor MUST fail with FailedPrecondition when an eligible pool exists but no free worker is available."

### REQ-API-029 — ResumeActor serializes via a distributed lock
- Section: ResumeActor
- Keyword: MUST | Error: Aborted
- Text: "ResumeActor MUST acquire a per-actor distributed lock before mutating state and return Aborted when another operation already holds it."

### REQ-API-030 — ResumeActor releases a stale worker assignment
- Section: ResumeActor
- Keyword: MUST
- Text: "ResumeActor MUST release a previously assigned worker when its pool is no longer eligible, and re-assign from an eligible pool."

### REQ-API-031 — ResumeActor requires both selectors to match
- Section: ResumeActor
- Keyword: MUST
- Text: "ResumeActor MUST require that both the template's worker selector and the actor's worker selector match a pool (logical AND) for it to be eligible."

## SuspendActor

### REQ-API-032 — SuspendActor requires actor_id
- Section: SuspendActor
- Keyword: MUST | Error: InvalidArgument
- Text: "SuspendActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-033 — SuspendActor returns NotFound for a missing actor
- Section: SuspendActor
- Keyword: MUST | Error: NotFound
- Text: "SuspendActor MUST return NotFound when the target actor does not exist."

### REQ-API-034 — SuspendActor is idempotent
- Section: SuspendActor
- Keyword: MUST
- Text: "SuspendActor MUST be safe to call repeatedly, fast-forwarding when the actor is already SUSPENDING or SUSPENDED."

### REQ-API-035 — SuspendActor transitions only from RUNNING
- Section: SuspendActor
- Keyword: MUST
- Text: "SuspendActor MUST only drive a suspend from STATUS_RUNNING; from any other non-suspended state it is a no-op."

### REQ-API-036 — SuspendActor serializes via a distributed lock
- Section: SuspendActor
- Keyword: MUST | Error: Aborted
- Text: "SuspendActor MUST acquire a per-actor distributed lock and return Aborted when another operation already holds it."

## PauseActor

### REQ-API-037 — PauseActor requires actor_id
- Section: PauseActor
- Keyword: MUST | Error: InvalidArgument
- Text: "PauseActor MUST reject a request with an empty actor_id with InvalidArgument."

### REQ-API-038 — PauseActor returns NotFound for a missing actor
- Section: PauseActor
- Keyword: MUST | Error: NotFound
- Text: "PauseActor MUST return NotFound when the target actor does not exist."

### REQ-API-039 — PauseActor is idempotent
- Section: PauseActor
- Keyword: MUST
- Text: "PauseActor MUST be safe to call repeatedly, fast-forwarding when the actor is already PAUSING or PAUSED."

### REQ-API-040 — PauseActor transitions only from RUNNING
- Section: PauseActor
- Keyword: MUST
- Text: "PauseActor MUST only drive a pause from STATUS_RUNNING; from any other non-paused state it is a no-op."

### REQ-API-041 — PauseActor serializes via a distributed lock
- Section: PauseActor
- Keyword: MUST | Error: Aborted
- Text: "PauseActor MUST acquire a per-actor distributed lock and return Aborted when another operation already holds it."

## ListActors

### REQ-API-042 — ListActors rejects a negative page_size
- Section: ListActors
- Keyword: MUST | Error: InvalidArgument
- Text: "ListActors MUST reject a request with page_size < 0 with InvalidArgument."

### REQ-API-043 — ListActors bounds page_size
- Section: ListActors
- Keyword: MUST | Error: InvalidArgument
- Text: "ListActors MUST reject a request with page_size > 1000 with InvalidArgument."

### REQ-API-044 — ListActors defaults page_size
- Section: ListActors
- Keyword: MUST
- Text: "ListActors MUST treat a page_size of 0 as the default page size of 1000."

### REQ-API-045 — ListActors paginates with an opaque token
- Section: ListActors
- Keyword: MUST
- Text: "ListActors MUST honor an opaque page_token to continue a listing and MUST return a next_page_token when more results remain."

## ListWorkers

### REQ-API-046 — ListWorkers lists all workers
- Section: ListWorkers
- Keyword: MUST
- Text: "ListWorkers MUST accept a request with no filters and return all registered workers."

## DebugClear

### REQ-API-047 — DebugClear clears all state
- Section: DebugClear
- Keyword: MUST
- Text: "DebugClear MUST remove all actor and worker state from the store."

## Workload spec (resume-time resolution)

### REQ-API-048 — Required missing secret fails resume
- Section: WorkloadSpec
- Keyword: MUST | Error: FailedPrecondition
- Text: "Building a workload spec MUST fail with FailedPrecondition when a required env secretKeyRef references a secret or key that does not exist."

### REQ-API-049 — Optional missing secret is skipped
- Section: WorkloadSpec
- Keyword: MUST
- Text: "Building a workload spec MUST skip an env entry whose secretKeyRef is marked optional and whose secret or key is missing, rather than failing."

### REQ-API-050 — Unsupported valueFrom source fails
- Section: WorkloadSpec
- Keyword: MUST | Error: FailedPrecondition
- Text: "Building a workload spec MUST fail with FailedPrecondition when an env valueFrom uses an unsupported source type."

## Worker syncer

### REQ-API-051 — Deleting a bound worker clears the actor binding
- Section: Syncer
- Keyword: MUST
- Text: "When a worker that is bound to an actor is deleted, the syncer MUST clear that actor's worker binding so the actor can be re-scheduled."

## Success-path behaviours

### REQ-API-052 — GetActor returns the stored actor
- Section: GetActor
- Keyword: MUST
- Text: "GetActor MUST return the stored actor (with its current status and fields) for an existing actor_id."

### REQ-API-053 — ListActors returns all actors
- Section: ListActors
- Keyword: MUST
- Text: "ListActors MUST return all stored actors within a single page when the page size accommodates them."

### REQ-API-054 — ResumeActor assigns a worker and transitions to RUNNING
- Section: ResumeActor
- Keyword: MUST
- Text: "ResumeActor MUST assign a free worker from an eligible pool and transition the actor to STATUS_RUNNING, including when several eligible pools exist."

### REQ-API-055 — Workload spec resolves env valueFrom from secrets
- Section: WorkloadSpec
- Keyword: MUST
- Text: "Building a workload spec MUST resolve an env entry's valueFrom secretKeyRef from the referenced Kubernetes secret, caching secret lookups across entries."

### REQ-API-056 — Syncer reconciles the worker lifecycle
- Section: Syncer
- Keyword: MUST
- Text: "The syncer MUST reconcile worker add/update/delete events into the worker store and the bound actor's state."
