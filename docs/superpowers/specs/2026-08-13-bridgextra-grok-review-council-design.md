# BridgeXtra Grok 4.6 Review Council — Design

Date: 2026-08-13
Status: Approved for implementation by user instruction to set up the council
Baseline at design start: `fc7bb21d40f60523fd879ba8807f47c8eeabe7a5`

## 1. Purpose

Create a reusable, read-only adversarial review council for BridgeXtra that gives multiple concurrent Grok 4.6 reviewers enough product, architecture, audit, and source context to produce useful findings, while preserving GPT-5.6 Sol as the final adjudicator.

The council exists to improve review coverage on high-risk architecture, money-safety, database, privilege, migration, and recovery changes without reintroducing slow multi-round review on every small patch.

## 2. Operating Model

The default full council has five independent reviewers, all using `x-ai/grok-4.6` through OpenRouter:

1. **Correctness & Invariants** — follows business/money invariants end-to-end.
2. **Security & Privilege** — attacks trust boundaries, roles, RLS, caller-controlled inputs, raw SQL escape paths, and cutover privilege.
3. **PostgreSQL & Concurrency** — attacks MVCC, isolation, row locks, timestamps, lease/reclaim, fence races, and migration semantics.
4. **Architecture & Failure Recovery** — attacks crash boundaries, retries, partial success, idempotency, zombie workers, observability, and rollback.
5. **Premise & Provenance** — validates repo path, SHA, migration head, runtime-vs-source claims, test non-vacuity, stale-checkout contamination, and inference quality.

All five receive the same shared BridgeXtra context and candidate source bundle, plus a role-specific lens.

Outputs are collected into one adjudication packet. The harness does not automatically accept or fix findings. GPT-5.6 Sol (or another explicitly configured adjudicator) classifies each finding as `GENUINE_CURRENT`, `NEXT_TRANCHE`, `DORMANT`, `EXTERNAL`, `DUPLICATE`, `FALSE_POSITIVE`, or `WRONG_PREMISE`.

Only adjudicated genuine findings proceed to reproduction/RED test/fix.

## 3. Risk-Tiered Use

The council must not become mandatory ceremony for every edit.

- **Full five-reviewer council:** scheduler/fencing, money-gate changes, privilege cutovers, ledger/recovery/idempotency changes, production historical migrations, MED-003 ingestion architecture, and production/live-money readiness declarations.
- **One or two reviewers:** bounded verified defects where the invariant is already frozen.
- **No remote council required:** mechanical/documentation-only changes whose correctness is fully covered by existing deterministic tests.

The CLI therefore supports selecting roles rather than always running all five.

## 4. Repository Layout

```text
review-council/
  context/
    PRODUCT.md
    ENGINEERING_GUARDRAILS.md
    CURRENT_STATUS.md
  roles/
    correctness.md
    security.md
    postgres-concurrency.md
    architecture-recovery.md
    premise-provenance.md
  ADJUDICATION.md

tools/review-council/
  main.go
  main_test.go

docs/review-council/
  README.md

.review-council/          # generated local run output; gitignored
```

`CURRENT_STATUS.md` is intentionally separate from the stable product context so it can be updated as findings/tranches close without rewriting the product brief.

## 5. CLI Contract

Primary command:

```bash
go run ./tools/review-council \
  --candidate path/to/candidate.md \
  --base <base-sha> \
  --include backend/internal/usecase/recon \
  --include backend/internal/repo \
  --include backend/migrations/0085_med004a2_recovery_qualification_publisher.sql
```

Key flags:

- `--candidate <file>` — required tranche/change description, frozen invariants, and questions to falsify.
- `--base <ref>` — diff base; defaults to `HEAD^` when omitted.
- `--include <path>` — repeatable file or directory whose text source is bundled with line numbers.
- `--roles <csv>` — subset of reviewers; default is all five.
- `--model <slug>` — default `x-ai/grok-4.6`; environment override allowed.
- `--max-source-bytes <n>` — deterministic source budget; fail closed rather than silently truncate a file halfway.
- `--dry-run` — generate prompts, provenance, and source bundle without calling OpenRouter.
- `--out <dir>` — optional output root; default `.review-council/runs/<timestamp>-<shortsha>`.

Environment:

- `OPENROUTER_API_KEY` — required for non-dry-run. Never written to disk or logs.
- `REVIEW_COUNCIL_MODEL` — optional model override.
- `OPENROUTER_BASE_URL` — optional endpoint override for controlled testing; default `https://openrouter.ai/api/v1/chat/completions`.

No product/database credentials are consumed by the council tool.

## 6. Provenance Capture

Before sending any model request, the harness captures from local git:

- absolute repo path;
- `HEAD` SHA;
- `origin/main` SHA when available;
- `git status --porcelain` result (`CLEAN` or `DIRTY`, plus detail in manifest);
- highest numbered `backend/migrations/*.sql` migration;
- candidate file hash;
- source-bundle hash;
- base ref and resolved base SHA.

The exact provenance block is injected into every reviewer prompt.

Each reviewer must echo these exact fields at the start of its response:

```text
repoPath: ...
headSHA: ...
originMainSHA: ...
migrationHead: ...
gitStatus: CLEAN|DIRTY
premisesVerified: YES
```

The harness validates the echoed values. A reviewer with missing or mismatched provenance is marked `INVALID_PROVENANCE` and excluded from the adjudication packet until re-run.

This directly prevents the stale-worktree contamination previously seen during MED-004 review.

## 7. Source Bundling

Remote reviewers cannot inspect the local repository themselves, so source context must be explicit.

The harness creates a deterministic source packet containing:

1. the candidate description;
2. `git diff --no-ext-diff <base>..HEAD`;
3. every explicitly included text file/directory, recursively and sorted;
4. stable shared context documents;
5. runtime provenance.

Included full source files are line-numbered so findings can cite exact paths and lines.

Excluded automatically:

- `.git/`;
- `.review-council/`;
- `node_modules/`;
- binary files (NUL-byte detection);
- generated package/build artifacts.

If the configured byte budget would be exceeded, the run fails with the paths that caused the overflow. It must not silently truncate the review surface.

## 8. Reviewer Output Contract

After the provenance block, each reviewer must either return `NO_GENUINE_FINDINGS` or one/more findings with this structure:

```text
## <finding-id>
severity: CRITICAL|HIGH|MEDIUM|LOW
classificationCandidate: GENUINE_CURRENT|NEXT_TRANCHE|DORMANT|EXTERNAL|DUPLICATE|FALSE_POSITIVE|WRONG_PREMISE
premise: ...
sourceEvidence: <path:line-range plus explanation>
reachablePath: ...
failureMode: ...
moneySafetyImpact: ...
reproduction: ...
expectedREDTest: ...
suggestedFixBoundary: ...
```

Reviewers are explicitly told:

- do not propose broad rewrites when the existing architecture can satisfy the invariant;
- do not reopen closed findings without a current-source regression;
- distinguish repository fact, runtime/deployment fact, inference, and external dependency;
- never infer deployed runtime state from migration seeds;
- never invent MTN schemas/contracts/UAT evidence;
- comments/design prose are not executable proof;
- a plausible finding is not automatically a genuine finding.

## 9. Shared BridgeXtra Context

The stable product brief will explain:

- BridgeXtra is a telco digital-credit platform for airtime/data advances;
- advance, fulfilment, funding-pool, recovery, ledger, scoring, reconciliation, governed config, operations, portal, and adapter responsibilities;
- recovery reconciliation freshness directly controls whether recovery money ingress may remain live;
- `dayConfirmed` uses monetary totals deliberately, not row counts only;
- the system is being retained and hardened, not rewritten;
- known closed Critical/High findings and why they mattered;
- real MTN contract/feed is not available and must not be fabricated;
- simulator/canonical adapter is engineering/demo evidence only, not partner UAT.

The mutable current-status brief will initially record:

- all non-partner Critical/High source findings closed;
- HIGH-016 closed;
- MED-004-A1 closed;
- MED-004-A2 durable/re-verifying publisher primitive closed at `b5f8589`;
- A2-F3 registered NEXT-TRANCHE/PRE-CUTOVER;
- real scheduler occurrence/fence integration still open;
- rollback/re-grant, mixed-fleet drain, privilege revoke, and overdue observation still open;
- BX-MED-016 scoring scheduler fencing is DORMANT/ACTIVATION BLOCKER unless deployed runtime proves enabled;
- MED-003 remains after MED-004;
- MTN partner/UAT dependency remains external/dormant;
- MED-009 incident hardening remains a separate bounded follow-up until closed.

## 10. OpenRouter Invocation

The implementation uses Go standard library HTTP rather than adding a provider SDK dependency.

Each reviewer is one independent `POST /api/v1/chat/completions` request with:

- `Authorization: Bearer $OPENROUTER_API_KEY`;
- model default `x-ai/grok-4.6`;
- low temperature for audit consistency;
- bounded output tokens;
- non-streaming response;
- per-request timeout;
- limited retry on HTTP 429 and 5xx only.

The five reviewer requests execute concurrently with independent contexts. One reviewer failure does not discard successful reviewer outputs; the manifest records failure and the adjudication packet states which lenses are missing.

Secrets are never included in request/response artifacts.

## 11. Output Artifacts

Each run writes:

```text
.review-council/runs/<run-id>/
  manifest.json
  provenance.txt
  source-bundle.txt
  prompt-correctness.txt
  prompt-security.txt
  prompt-postgres-concurrency.txt
  prompt-architecture-recovery.txt
  prompt-premise-provenance.txt
  reviewer-correctness.md
  reviewer-security.md
  reviewer-postgres-concurrency.md
  reviewer-architecture-recovery.md
  reviewer-premise-provenance.md
  adjudication-packet.md
```

`adjudication-packet.md` contains only valid-provenance reviewer outputs and explicit instructions for GPT-5.6 Sol adjudication.

## 12. Safety and Failure Behaviour

- Missing API key in live mode: fail before generating network requests.
- Dirty working tree: allowed but prominently recorded; reviewers are instructed to treat uncommitted source as part of the candidate and not conflate it with `HEAD`.
- Missing `origin/main`: record `UNAVAILABLE`; do not invent it.
- Source budget exceeded: fail closed.
- Model returns wrong provenance: exclude that reviewer.
- 401/403: no retry.
- 429/5xx: bounded retry.
- malformed OpenRouter response: preserve raw response metadata without secrets and mark reviewer failed.
- council findings never mutate source automatically.

## 13. Testing

Unit tests must cover at minimum:

1. migration-head detection;
2. deterministic file ordering;
3. binary-file rejection;
4. byte-budget fail-closed behaviour;
5. provenance echo validation including mismatch rejection;
6. role selection;
7. prompt includes shared context + candidate + provenance + role lens;
8. API key is never present in persisted request/prompt artifacts;
9. OpenRouter response parsing using `httptest.Server`;
10. 429 retry then success;
11. 401 no-retry;
12. dry-run performs zero network calls;
13. adjudication packet excludes invalid-provenance reviewers and identifies missing lenses.

CI should run `go test ./tools/review-council` in addition to normal project quality gates.

## 14. Non-Goals

This tranche does not:

- give Grok write access to GitHub, PostgreSQL, Neon, Docker, or production;
- automatically apply fixes;
- replace GPT-5.6 Sol adjudication;
- replace deterministic race/mutation/DB tests;
- make a five-model council mandatory for small patches;
- fabricate external MTN evidence;
- change BridgeXtra product runtime behaviour.

## 15. Acceptance Criteria

The setup is complete when:

1. a dry run on a pinned BridgeXtra candidate creates deterministic reviewer prompts containing verified provenance and full shared project context;
2. a live run with `OPENROUTER_API_KEY` can launch selected Grok 4.6 reviewers concurrently and collect outputs;
3. invalid-provenance reviewer output is automatically rejected from adjudication;
4. generated artifacts are gitignored and contain no API key;
5. the adjudication packet is directly usable in the GPT-5.6 Sol review workflow;
6. harness unit tests are green;
7. existing BridgeXtra CI remains green.
