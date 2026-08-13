# BridgeXtra Grok 4.6 Review Council

This tool runs independent Grok 4.6 adversarial reviewers against a pinned BridgeXtra candidate and produces a packet for GPT-5.6 Sol adjudication.

It is **read-only**. It does not modify GitHub, PostgreSQL, Neon, Docker, product code, or partner systems. Findings are advisory until independently adjudicated and reproduced.

## Prerequisites

- Run from inside the BridgeXtra git repository.
- Go toolchain matching the repository.
- `OPENROUTER_API_KEY` for live runs.
- Network access to OpenRouter for live runs.

Default model: `x-ai/grok-4.6`.

## Full Council

Use the full five-lens council for scheduler/fencing, money-gate changes, privilege cutovers, historical production migrations, ledger/recovery/idempotency changes, MED-003 architecture, and production-readiness review.

```bash
go run ./tools/review-council \
  --candidate build/candidate_MED004_scheduler.md \
  --base <parent-or-design-baseline-sha> \
  --include backend/internal/usecase/recon \
  --include backend/internal/repo \
  --include backend/cmd/worker \
  --include backend/migrations
```

The tool launches these reviewers concurrently:

- correctness
- security
- postgres-concurrency
- architecture-recovery
- premise-provenance

## Bounded Review

For a small verified defect, select only the relevant lenses:

```bash
go run ./tools/review-council \
  --candidate build/candidate_bounded_fix.md \
  --base HEAD^ \
  --roles correctness,postgres-concurrency \
  --include backend/internal/usecase/recon
```

## Dry Run

A dry run requires no API key and performs no network requests:

```bash
go run ./tools/review-council \
  --dry-run \
  --candidate build/candidate_MED004_scheduler.md \
  --base HEAD^ \
  --include backend/internal/usecase/recon
```

Inspect the generated prompt files to confirm reviewers receive the intended source/context before spending model calls.

## Candidate File

The candidate file should state:

- tranche/finding being reviewed;
- exact intended behavior/invariants;
- what was changed;
- what remains deliberately out of scope;
- tests/evidence already available;
- specific claims the reviewers should try to falsify.

Do not put secrets in the candidate.

## Source Selection

Remote reviewers cannot browse the local repo. `--include` therefore controls the full source files sent in addition to the git diff.

Prefer the smallest complete review surface: the changed package plus the repositories/migrations/config/worker paths needed to prove the invariant. The tool line-numbers included source and fails closed if the configured source budget is exceeded.

Default source budget: 600,000 bytes. Override with `--max-source-bytes` only when the candidate genuinely needs a larger complete surface.

## Provenance Gate

Each prompt contains:

- absolute repo path;
- HEAD SHA;
- origin/main SHA when available;
- migration head;
- CLEAN/DIRTY status;
- base ref/base SHA;
- candidate/source hashes.

A reviewer must echo the provenance exactly and state `premisesVerified: YES`. Mismatched provenance is excluded from the adjudication packet.

## Environment Variables

- `OPENROUTER_API_KEY` — required for live runs; never persisted.
- `REVIEW_COUNCIL_MODEL` — optional model override; default `x-ai/grok-4.6`.
- `OPENROUTER_BASE_URL` — optional endpoint override; default `https://openrouter.ai/api/v1/chat/completions`.

## Output

Default output:

```text
.review-council/runs/<timestamp>-<shortsha>/
```

It contains provenance, source bundle, per-role prompts, reviewer outputs, manifest, and `adjudication-packet.md`.

`.review-council/` is gitignored.

## GPT-5.6 Sol Handoff

Send/upload `adjudication-packet.md` to the GPT-5.6 Sol reviewer. The adjudicator independently classifies each finding as current, next-tranche, dormant, external, duplicate, false positive, or wrong premise.

Do not auto-fix Grok output. Genuine current findings should first receive a concrete reproduction/RED test, then the narrowest fix, then the normal BridgeXtra Docker/Postgres/race/CI gates.
