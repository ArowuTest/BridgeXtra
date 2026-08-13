# BridgeXtra Engineering Review Guardrails

## Evidence Discipline

Review the pinned source packet and exact provenance first. A clever finding against the wrong tree is inadmissible.

Always distinguish:

- **repository fact** — directly proven by current source/migrations/tests;
- **runtime/deployment fact** — requires deployed config, DSN, role, service, or environment evidence;
- **inference** — supported reasoning that must be labelled as inference;
- **external dependency** — partner/legal/commercial/UAT evidence outside this repository.

Do not promote one category into another.

## Provenance Gate

Every review must echo the supplied `repoPath`, `headSHA`, `originMainSHA`, `migrationHead`, and `gitStatus` exactly and state `premisesVerified: YES`.

A stale worktree, wrong SHA, migration head that predates the candidate, or unverified runtime assumption invalidates the lens until reproduced against the pinned candidate.

## Historical Lessons That Must Shape Review

1. **Unavailable evidence is not a quiet day.** A source adapter must not convert unavailable/missing/unauthenticated evidence into a successful zero. Zero is evidence only when the source contract positively attests zero.
2. **Repository seed != runtime state.** `enabled:false` in a migration proves repository-default dormancy only. It does not prove a deployed operator never activated a config.
3. **Normal return != safe outcome.** HIGH-016 existed because a REJECTED Summary returned normally and one call path treated it as qualifying.
4. **Correct origin != trusted boundary.** A Go field may originally have come from the database and still be untrusted when a downstream publisher accepts it from the caller instead of re-reading durable authority. This is why A2 stopped trusting caller `EvidenceAt`.
5. **Money confirmation uses money.** Row counts cannot replace matched monetary control totals.
6. **Comments/design prose are not executable proof.** Verify code, SQL, tests, roles/grants, and actual call sites.
7. **Migration history matters.** A historical row predating a later integrity algorithm does not automatically prove it was immutable for its entire life. Evaluate which controls existed at the time.
8. **Lease expiry is not itself revocation.** An expired holder is reclaimable; the monotonic fence transfer on reclaim revokes the old owner. Race ordering matters.
9. **Serialization is not ownership fencing.** `FOR UPDATE` can serialize writers without proving the writer still owns the scheduler occurrence.
10. **Database clocks have semantics.** PostgreSQL `now()` is transaction-start time; `statement_timestamp()`/write-boundary ordering may matter for concurrent policy activation.

## Scope / Classification Discipline

A new valid finding does not automatically expand the current tranche. Classify it as one of:

- `GENUINE_CURRENT`
- `NEXT_TRANCHE`
- `DORMANT`
- `EXTERNAL`
- `DUPLICATE`
- `FALSE_POSITIVE`
- `WRONG_PREMISE`

Only `GENUINE_CURRENT` blocks the candidate under review. A `NEXT_TRANCHE` item must remain tracked and must block the later tranche where its invariant becomes live.

## Fix Philosophy

- Retain sound architecture; do not propose rewrites merely because another design is aesthetically preferable.
- Prefer the smallest boundary that closes the verified failure mode.
- Every genuine money/control finding should have a reachable path and a falsification/reproduction strategy.
- Mutation-grade tests are preferred where they can prove the test would fail if the key guard were removed.
- Final acceptance still requires the repository's deterministic tests/race/DB/CI gates. LLM review never substitutes for them.

## Closed-Finding Discipline

Do not reopen a closed finding from historical prose alone. Reopen only when current source contains a concrete regression or the earlier closure premise is demonstrably false against the pinned SHA.

## External Partner Discipline

Do not fabricate MTN-specific behavior. Mark missing partner evidence `EXTERNAL`/`DORMANT` and review only the canonical/simulator behavior that the repository actually implements.
