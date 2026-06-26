# ateredis Requirement Waivers (prototype)

A requirement listed here is deliberately excused from a tagged test, with an
allowed `Reason` and a `Rationale`. A requirement is EITHER tested OR waived,
never both. Allowed reasons: `covered-by`, `not-implemented`,
`documented-deviation`, `deployment-guidance`, `foundational`.

### REQ-ATEREDIS-008
- Reason: deployment-guidance
- Rationale: Hard cutover by design (issue #307, pre-alpha, no production data). protojson and binary protobuf cannot decode each other, so there is no migration/dual-read path to test; the dev keyspace is flushed on deploy. See docs/dev/valkey-direct-access.md ("Deploying the binary-protobuf cutover").
