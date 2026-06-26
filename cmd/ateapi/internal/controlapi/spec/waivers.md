# Control API Requirement Waivers

A requirement listed here is deliberately excused from its own tagged test, with
an allowed `Reason` and a `Rationale`. A requirement is EITHER tested OR waived,
never both. Allowed reasons: `covered-by`, `not-implemented`,
`documented-deviation`, `deployment-guidance`, `foundational`.

The two waivers below are optimistic-concurrency obligations that the handler
delegates entirely to the store layer. There is no deterministic RPC-level seam
to induce the race (the handler retries transparently), so the guarantee is
verified once at the store layer rather than through the Control API.

### REQ-API-018
- Reason: covered-by
- Rationale: UpdateActor delegates the version check to store.UpdateActor, whose version-based optimistic concurrency is unit-tested at the store layer (ateredis TestUpdateActor_Conflict). The handler retries transparently on ErrPersistenceRetry, so there is no deterministic RPC-level seam to induce the race; the obligation is verified once at the store layer.

### REQ-API-023
- Reason: covered-by
- Rationale: DeleteActor delegates its compare-and-delete to store.DeleteActor, whose WATCH/MULTI optimistic concurrency is exercised at the store layer (ateredis DeleteActor under miniredis). There is no deterministic RPC-level seam to induce the delete race; the obligation is verified at the store layer.
