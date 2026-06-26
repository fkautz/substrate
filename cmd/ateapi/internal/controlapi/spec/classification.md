# Control API Requirement Classification

Per-requirement axis: `wire-observable` (verifiable from the gRPC request/
response or error — black-box, proven by the gRPC-driven tests in
functional_test.go) vs `not-observable` (internal behaviour proven by
white-box unit tests on internal functions, e.g. the workload-spec builder and
the worker syncer). Every catalog requirement appears exactly once. Under
`-strict`, a `wire-observable` requirement must carry a black-box test;
`not-observable` forbids one.

### REQ-API-001
- Class: wire-observable

### REQ-API-002
- Class: wire-observable

### REQ-API-003
- Class: wire-observable

### REQ-API-004
- Class: wire-observable

### REQ-API-005
- Class: wire-observable

### REQ-API-006
- Class: wire-observable

### REQ-API-007
- Class: wire-observable

### REQ-API-008
- Class: wire-observable

### REQ-API-009
- Class: wire-observable

### REQ-API-010
- Class: wire-observable

### REQ-API-011
- Class: wire-observable

### REQ-API-012
- Class: wire-observable

### REQ-API-013
- Class: wire-observable

### REQ-API-014
- Class: wire-observable

### REQ-API-015
- Class: wire-observable

### REQ-API-016
- Class: wire-observable

### REQ-API-017
- Class: wire-observable

### REQ-API-018
- Class: not-observable
- Reason: The version check is internal; UpdateActor retries transparently on a concurrent modification, so the optimistic-concurrency mechanism is not directly observable from a single RPC and is verified at the store layer.

### REQ-API-019
- Class: wire-observable

### REQ-API-020
- Class: wire-observable

### REQ-API-021
- Class: wire-observable

### REQ-API-022
- Class: wire-observable

### REQ-API-023
- Class: not-observable
- Reason: The compare-and-delete version check is internal to the store; there is no single-RPC-observable signal, so it is verified at the store layer.

### REQ-API-024
- Class: wire-observable

### REQ-API-025
- Class: wire-observable

### REQ-API-026
- Class: wire-observable

### REQ-API-027
- Class: wire-observable

### REQ-API-028
- Class: wire-observable

### REQ-API-029
- Class: wire-observable

### REQ-API-030
- Class: wire-observable

### REQ-API-031
- Class: wire-observable

### REQ-API-032
- Class: wire-observable

### REQ-API-033
- Class: wire-observable

### REQ-API-034
- Class: wire-observable

### REQ-API-035
- Class: wire-observable

### REQ-API-036
- Class: wire-observable

### REQ-API-037
- Class: wire-observable

### REQ-API-038
- Class: wire-observable

### REQ-API-039
- Class: wire-observable

### REQ-API-040
- Class: wire-observable

### REQ-API-041
- Class: wire-observable

### REQ-API-042
- Class: wire-observable

### REQ-API-043
- Class: wire-observable

### REQ-API-044
- Class: wire-observable

### REQ-API-045
- Class: wire-observable

### REQ-API-046
- Class: wire-observable

### REQ-API-047
- Class: wire-observable

### REQ-API-048
- Class: not-observable
- Reason: Required-secret-missing failure is raised while building the workload spec, verified by a white-box unit test on the builder rather than through the RPC surface.

### REQ-API-049
- Class: not-observable
- Reason: Optional-secret skipping happens inside the workload-spec builder and is verified by a white-box unit test on the builder.

### REQ-API-050
- Class: not-observable
- Reason: Unsupported-valueFrom rejection happens inside the workload-spec builder and is verified by a white-box unit test on the builder.

### REQ-API-051
- Class: not-observable
- Reason: The syncer's clearing of an actor's binding on worker deletion is internal reconciliation, verified by a white-box unit test on the syncer.

### REQ-API-052
- Class: wire-observable

### REQ-API-053
- Class: wire-observable

### REQ-API-054
- Class: wire-observable

### REQ-API-055
- Class: wire-observable

### REQ-API-056
- Class: not-observable
- Reason: Worker-lifecycle reconciliation into the store is internal, verified by a white-box unit test on the syncer.
