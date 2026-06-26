# Control API Requirement Waivers

A requirement listed here is deliberately excused from its own tagged test, with
an allowed `Reason` and a `Rationale`. A requirement is EITHER tested OR waived,
never both. Allowed reasons: `covered-by`, `not-implemented`,
`documented-deviation`, `deployment-guidance`, `foundational`.

Most waivers below are `covered-by`: the obligation IS exercised, but by a
multi-case test function (`TestValidation`, `TestListActors_PageSizeValidation`,
etc.) and trace-check attributes one requirement per test FUNCTION. Splitting
those table-driven tests into per-case functions would convert each of these
into a direct `// Verifies:` tag.

### REQ-API-001
- Reason: covered-by
- Rationale: GetActor empty-id rejection is exercised by TestValidation/GetActor ("missing id"); that function is attributed to REQ-API-006.

### REQ-API-003
- Reason: covered-by
- Rationale: CreateActor missing-namespace rejection is exercised by TestValidation/CreateActor ("missing namespace"); the function is attributed to REQ-API-006.

### REQ-API-004
- Reason: covered-by
- Rationale: CreateActor missing-template-name rejection is exercised by TestValidation/CreateActor ("missing template name"); the function is attributed to REQ-API-006.

### REQ-API-005
- Reason: covered-by
- Rationale: CreateActor missing-actor-id rejection is exercised by TestValidation/CreateActor ("missing actor id"); the function is attributed to REQ-API-006.

### REQ-API-007
- Reason: covered-by
- Rationale: CreateActor worker_selector label-count limit is exercised by TestValidation/CreateActor ("too many worker_selector match_labels"); the function is attributed to REQ-API-006.

### REQ-API-008
- Reason: covered-by
- Rationale: CreateActor worker_selector key validation is exercised by TestValidation/CreateActor ("invalid worker_selector label key"); the function is attributed to REQ-API-006.

### REQ-API-009
- Reason: covered-by
- Rationale: CreateActor worker_selector value validation is exercised by TestValidation/CreateActor ("invalid worker_selector label value"); the function is attributed to REQ-API-006.

### REQ-API-013
- Reason: covered-by
- Rationale: Initial version == 1 is asserted by TestCreateActor_Success, which is attributed to REQ-API-012 (initial STATUS_SUSPENDED) for the same created actor.

### REQ-API-014
- Reason: covered-by
- Rationale: UpdateActor missing-actor-id rejection is exercised by TestValidation/UpdateActor ("missing id"); the function is attributed to REQ-API-006.

### REQ-API-015
- Reason: covered-by
- Rationale: UpdateActor worker_selector validation is exercised by TestValidation/UpdateActor (key/value/count cases); the function is attributed to REQ-API-006.

### REQ-API-019
- Reason: covered-by
- Rationale: DeleteActor missing-actor-id rejection is exercised by TestValidation/DeleteActor ("missing id"); the function is attributed to REQ-API-006.

### REQ-API-020
- Reason: covered-by
- Rationale: DeleteActor actor_id format validation is exercised by TestValidation/DeleteActor (capitals/special-chars cases); the function is attributed to REQ-API-006.

### REQ-API-024
- Reason: covered-by
- Rationale: ResumeActor empty-id rejection is exercised by TestValidation/ResumeActor ("missing id"); the function is attributed to REQ-API-006.

### REQ-API-026
- Reason: covered-by
- Rationale: ResumeActor idempotency is exercised by TestResumeActor_ReleasesStaleWorkerWhenPoolBecomesIneligible (a re-resume of an already-active actor), which is attributed to REQ-API-030.

### REQ-API-032
- Reason: covered-by
- Rationale: SuspendActor empty-id rejection is exercised by TestValidation/SuspendActor ("missing id"); the function is attributed to REQ-API-006.

### REQ-API-042
- Reason: covered-by
- Rationale: ListActors negative-page_size rejection is exercised by TestListActors_PageSizeValidation alongside the >1000 case, which is attributed to REQ-API-043.
