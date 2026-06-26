# ateredis Requirements Catalog (prototype)

Traceability catalog for the binary-protobuf record-encoding change in the
ateredis store (issue #307). Series `ATEREDIS`. Each requirement is either
covered by a tagged test (`// Verifies: REQ-ATEREDIS-NNN`) or excused in
`waivers.md`. The generated matrix is `spec/traceability.md` (do not hand-edit).

### REQ-ATEREDIS-001 — Actor records encoded as binary protobuf
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "Actor records MUST be written to and read from the Valkey keyspace as binary protobuf (proto.Marshal/proto.Unmarshal), not protojson."

### REQ-ATEREDIS-002 — Worker records encoded as binary protobuf
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "Worker records MUST be written to and read from the Valkey keyspace as binary protobuf, not protojson."

### REQ-ATEREDIS-003 — List operations round-trip binary records
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "ListActors and ListWorkers MUST decode stored records as binary protobuf and return them faithfully."

### REQ-ATEREDIS-004 — Reject identity-mismatched or empty stored records
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "Get and List operations MUST reject a stored value whose decoded identity does not match its key, including an empty value (which decodes to a zero-valued message under proto.Unmarshal without error)."

### REQ-ATEREDIS-005 — Reject non-protobuf stored values
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "A stored value that is not valid binary protobuf MUST fail to decode rather than being silently accepted."

### REQ-ATEREDIS-006 — Binary-safe distributed lock
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "Lock acquire/release MUST treat the lock token as opaque bytes (compared for equality, including an embedded NUL byte) and MUST be unaffected by the record-encoding change."

### REQ-ATEREDIS-007 — Optimistic-concurrency version check preserved
- Section: ateredis.go
- Keyword: MUST | Actor: Store
- Text: "Update operations MUST preserve version-based optimistic concurrency (WATCH/MULTI), returning a retry error on a stale expected version, unchanged by the encoding switch."

### REQ-ATEREDIS-008 — No migration of pre-existing protojson records
- Section: ateredis.go
- Keyword: MUST NOT | Actor: Store
- Text: "The change MUST NOT attempt to migrate or dual-read pre-existing protojson records; it is a hard cutover and the keyspace is flushed on deploy."
