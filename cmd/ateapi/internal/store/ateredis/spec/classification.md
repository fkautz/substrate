# ateredis Requirement Classification (prototype)

Per-requirement axis: `wire-observable` (verifiable from the stored bytes /
behaviour against a real Valkey cluster — black-box) vs `not-observable`
(internal store behaviour — unit / white-box). Every catalog requirement appears
exactly once. Under `-strict`, a `wire-observable` requirement must carry a
real-cluster (black-box) test; `not-observable` forbids one.

### REQ-ATEREDIS-001
- Class: wire-observable

### REQ-ATEREDIS-002
- Class: wire-observable

### REQ-ATEREDIS-003
- Class: wire-observable

### REQ-ATEREDIS-004
- Class: wire-observable

### REQ-ATEREDIS-005
- Class: not-observable
- Reason: Decode-failure handling for non-protobuf bytes is internal store behaviour, verified at the unit layer with crafted values via miniredis; the real-cluster corrupt-value test attributes its black-box coverage to REQ-ATEREDIS-004.

### REQ-ATEREDIS-006
- Class: wire-observable

### REQ-ATEREDIS-007
- Class: not-observable
- Reason: Version-based WATCH/MULTI optimistic concurrency is internal to the store and unaffected by the encoding change; verified at the unit layer.

### REQ-ATEREDIS-008
- Class: not-observable
- Reason: A deliberate non-behaviour (no migration path) — an operational/deployment property with nothing to assert in code.
