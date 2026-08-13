# BridgeXtra Grok 4.6 Review Council Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only, provenance-gated Go harness that sends a well-grounded BridgeXtra candidate/source packet to concurrent Grok 4.6 specialist reviewers through OpenRouter and produces a GPT-5.6 Sol adjudication packet.

**Architecture:** Stable product/audit context and five role prompts live under `review-council/`. A focused Go CLI under `tools/review-council/` captures git provenance, builds a deterministic source bundle, invokes selected reviewers concurrently, rejects mismatched provenance, and writes local gitignored artifacts. The harness never mutates product code, database state, GitHub state, or external partner systems.

**Tech Stack:** Go 1.25 standard library (`flag`, `os/exec`, `net/http`, `httptest`, `crypto/sha256`, `encoding/json`, `sync`), local Git CLI, OpenRouter Chat Completions API, existing GitHub Actions quality workflow.

## Global Constraints

- Default reviewer model is exactly `x-ai/grok-4.6`.
- Use `OPENROUTER_API_KEY` only from environment; never persist it.
- Default endpoint is `https://openrouter.ai/api/v1/chat/completions`; permit `OPENROUTER_BASE_URL` override for tests/controlled routing.
- Council is read-only and never automatically applies findings or fixes.
- Full five-reviewer council is for high-risk tranches; `--roles` must allow smaller bounded reviews.
- Every reviewer receives stable product context, current audit status, exact candidate/source context, and exact provenance.
- A reviewer with mismatched/missing provenance is `INVALID_PROVENANCE` and excluded from adjudication.
- Source budget overflow fails closed; never silently truncate files.
- Do not fabricate MTN contract/feed/UAT evidence.
- Do not infer deployed runtime state from migration seeds.
- Generated `.review-council/` artifacts are gitignored.
- Existing BridgeXtra product runtime behavior must not change.

---

## File Structure

**Create:**
- `review-council/context/PRODUCT.md` — stable BridgeXtra product/business architecture and money-safety context.
- `review-council/context/ENGINEERING_GUARDRAILS.md` — audit/review rules and historical lessons.
- `review-council/context/CURRENT_STATUS.md` — mutable audit/tranche status.
- `review-council/roles/correctness.md`
- `review-council/roles/security.md`
- `review-council/roles/postgres-concurrency.md`
- `review-council/roles/architecture-recovery.md`
- `review-council/roles/premise-provenance.md`
- `review-council/ADJUDICATION.md`
- `tools/review-council/types.go` — data structures and role registry.
- `tools/review-council/git.go` — repository provenance capture.
- `tools/review-council/git_test.go`
- `tools/review-council/bundle.go` — deterministic source/context packet builder.
- `tools/review-council/bundle_test.go`
- `tools/review-council/openrouter.go` — HTTP client, retries, concurrency.
- `tools/review-council/openrouter_test.go`
- `tools/review-council/output.go` — provenance validation, artifact writing, adjudication packet.
- `tools/review-council/output_test.go`
- `tools/review-council/main.go` — CLI orchestration only.
- `docs/review-council/README.md` — operator usage and risk-tier guidance.

**Modify:**
- `.gitignore` — ignore `/.review-council/`.
- `.github/workflows/ci.yml` — run `go test ./tools/review-council` in quality gate.

---

### Task 1: Shared Product Context and Specialist Role Prompts

**Files:** all `review-council/**` Markdown files listed above.

**Interfaces:**
- Consumes: current BridgeXtra audit/product understanding from the approved design.
- Produces: stable Markdown files read by `buildPrompt(root, role, provenance, candidate, sourceBundle)` in Task 3.

- [ ] **Step 1: Write `PRODUCT.md` with stable product context**

It must cover: airtime/data digital credit; subscriber/account, advance/origination, fulfilment, funding pools, recovery, ledger, scoring, governed config, reconciliation, adapters, operations, portal; RECOVERY freshness as a money-ingress gate; monetary `dayConfirmed`; retain-and-harden strategy; MTN external dependency; simulator evidence limits.

- [ ] **Step 2: Write `ENGINEERING_GUARDRAILS.md`**

Include the historical lessons: quiet-day evidence must be positively attested; migration seed != deployed runtime; REJECTED summary can return normally; caller-originated values are not authoritative when durable DB facts exist; stale checkout invalidates review; comments are not proof; source/runtime/inference/external distinctions; no automatic rewrite proposals.

- [ ] **Step 3: Write `CURRENT_STATUS.md`**

Initial status must include: non-partner Critical/High closed; HIGH-016 closed; MED-004-A1 closed; A2 publisher primitive closed at `b5f8589`; A2-F3 next-tranche/pre-cutover; scheduler occurrence/fence, rollback/re-grant, mixed-fleet drain, privilege revoke and overdue observation open; BX-MED-016 dormant/activation blocker; MED-003 after MED-004; MTN external/dormant; MED-009 incident hardening separate and not to be conflated with MED-004.

- [ ] **Step 4: Write five role prompts**

Each role prompt must name its lens, what to attack, what to ignore, and the required finding schema. `premise-provenance.md` must explicitly reject stale SHA/migration/runtime assumptions.

- [ ] **Step 5: Write `ADJUDICATION.md`**

Require GPT-5.6 Sol to independently classify every finding as one of: `GENUINE_CURRENT`, `NEXT_TRANCHE`, `DORMANT`, `EXTERNAL`, `DUPLICATE`, `FALSE_POSITIVE`, `WRONG_PREMISE`; only genuine current findings proceed to RED/reproduction/fix.

- [ ] **Step 6: Commit the context layer**

```bash
git add review-council
git commit -m "docs: add BridgeXtra review council context and roles"
```

---

### Task 2: Provenance Capture and Role Registry

**Files:** `tools/review-council/types.go`, `git.go`, `git_test.go`.

**Interfaces:**

```go
type Role struct {
    Slug       string
    Name       string
    PromptPath string
}

type Provenance struct {
    RepoPath       string `json:"repo_path"`
    HeadSHA        string `json:"head_sha"`
    OriginMainSHA  string `json:"origin_main_sha"`
    MigrationHead  string `json:"migration_head"`
    GitStatus      string `json:"git_status"`
    GitStatusDetail string `json:"git_status_detail,omitempty"`
    BaseRef        string `json:"base_ref"`
    BaseSHA        string `json:"base_sha"`
    CandidateSHA256 string `json:"candidate_sha256"`
    SourceSHA256   string `json:"source_sha256"`
}

func selectedRoles(csv string) ([]Role, error)
func captureGitProvenance(ctx context.Context, root, baseRef string) (Provenance, error)
func detectMigrationHead(root string) (string, error)
```

- [ ] **Step 1: Write failing role-selection tests**

Test default/all-five order, selected subset order, unknown role rejection, and duplicate role de-duplication.

- [ ] **Step 2: Write failing migration-head tests**

Use `t.TempDir()` with `backend/migrations/0084_a.sql`, `0085_b.sql`, unrelated files; expect `0085_b.sql`. Test no migration files as error.

- [ ] **Step 3: Implement `Role`, role registry, and `selectedRoles`**

Registry slugs: `correctness`, `security`, `postgres-concurrency`, `architecture-recovery`, `premise-provenance`.

- [ ] **Step 4: Implement `detectMigrationHead`**

Parse the leading numeric prefix before `_`, choose greatest numeric version, and return the full filename.

- [ ] **Step 5: Implement `captureGitProvenance`**

Use `git -C <root>` for `rev-parse HEAD`, `rev-parse origin/main`, `status --porcelain`, and `rev-parse <baseRef>`. Missing `origin/main` becomes `UNAVAILABLE`; other failures are fatal.

- [ ] **Step 6: Run tests**

```bash
go test ./tools/review-council -run 'TestSelectedRoles|TestDetectMigrationHead|TestCaptureGitProvenance' -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add tools/review-council/types.go tools/review-council/git.go tools/review-council/git_test.go
git commit -m "feat(review-council): capture repository provenance"
```

---

### Task 3: Deterministic Source Bundle and Prompt Construction

**Files:** `tools/review-council/bundle.go`, `bundle_test.go`.

**Interfaces:**

```go
type BundleInput struct {
    Root           string
    CandidatePath  string
    BaseRef        string
    Includes       []string
    MaxBytes       int64
    Provenance     Provenance
}

type Bundle struct {
    Candidate string
    Diff      string
    Source    string
    Combined  string
    SHA256    string
}

func buildBundle(ctx context.Context, in BundleInput) (Bundle, error)
func buildPrompt(root string, role Role, p Provenance, b Bundle) (string, error)
```

- [ ] **Step 1: Write failing deterministic-order and line-number tests**

Create nested text files in unsorted order; expect lexicographically sorted path blocks and lines rendered as `L0001`, `L0002`, etc.

- [ ] **Step 2: Write failing binary/symlink safety tests**

A file containing NUL must be rejected. A symlink include must be rejected instead of followed outside the repository.

- [ ] **Step 3: Write failing byte-budget test**

Set `MaxBytes` below the combined candidate/diff/source size and assert a descriptive error naming the path/section that exceeded budget.

- [ ] **Step 4: Implement safe path collection**

Reject paths whose `filepath.Rel(root, path)` begins with `..`; skip `.git`, `.review-council`, `node_modules`, `.next`, `dist`, binary artifacts; sort files before reading.

- [ ] **Step 5: Implement candidate + git diff + line-numbered source bundle**

Run `git diff --no-ext-diff <baseRef>..HEAD`; include candidate text and explicitly selected full source files. Hash `Combined` with SHA-256.

- [ ] **Step 6: Implement `buildPrompt`**

Read `PRODUCT.md`, `ENGINEERING_GUARDRAILS.md`, `CURRENT_STATUS.md`, and role prompt. Inject the exact provenance block and reviewer output schema before the source bundle.

- [ ] **Step 7: Run tests**

```bash
go test ./tools/review-council -run 'TestBuildBundle|TestBuildPrompt|TestSourceBudget|TestBinary|TestSymlink' -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add tools/review-council/bundle.go tools/review-council/bundle_test.go
git commit -m "feat(review-council): build deterministic source packets"
```

---

### Task 4: OpenRouter Client, Retry Policy, and Concurrent Council Execution

**Files:** `tools/review-council/openrouter.go`, `openrouter_test.go`.

**Interfaces:**

```go
type OpenRouterClient struct {
    Endpoint  string
    APIKey    string
    Model     string
    HTTP      *http.Client
    MaxTokens int
    Retries   int
    Sleep     func(time.Duration)
}

type ReviewerResult struct {
    Role            Role   `json:"role"`
    Content         string `json:"content,omitempty"`
    ValidProvenance bool   `json:"valid_provenance"`
    Error           string `json:"error,omitempty"`
}

func (c *OpenRouterClient) Review(ctx context.Context, systemPrompt, userPrompt string) (string, error)
func runCouncil(ctx context.Context, c *OpenRouterClient, root string, roles []Role, p Provenance, b Bundle) []ReviewerResult
```

- [ ] **Step 1: Write `httptest.Server` success test**

Assert request uses POST, Bearer auth, exact model, non-streaming JSON, low temperature, and parses `choices[0].message.content`.

- [ ] **Step 2: Write 429 retry-then-success test**

Server returns 429 once then 200. Inject `Sleep` as no-op and assert exactly two requests.

- [ ] **Step 3: Write 401 no-retry test**

Server always returns 401. Assert one request only and error contains status.

- [ ] **Step 4: Write malformed/empty response test**

Return 200 without assistant content; expect error.

- [ ] **Step 5: Implement client using only standard library**

Request body:

```json
{
  "model": "x-ai/grok-4.6",
  "messages": [
    {"role":"system","content":"<role instructions>"},
    {"role":"user","content":"<BridgeXtra context + provenance + candidate + source>"}
  ],
  "temperature": 0.1,
  "max_tokens": 9000,
  "stream": false
}
```

Retry only HTTP 429 and 5xx, maximum two retries. Never include API key in returned errors.

- [ ] **Step 6: Implement concurrent `runCouncil`**

Launch one goroutine per selected role, collect all results, and return them in requested role order. A single reviewer failure must not cancel siblings.

- [ ] **Step 7: Run tests**

```bash
go test ./tools/review-council -run 'TestOpenRouter|TestRunCouncil' -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add tools/review-council/openrouter.go tools/review-council/openrouter_test.go
git commit -m "feat(review-council): run concurrent Grok reviewers"
```

---

### Task 5: Provenance Validation and GPT-5.6 Adjudication Packet

**Files:** `tools/review-council/output.go`, `output_test.go`.

**Interfaces:**

```go
type RunManifest struct {
    RunID      string           `json:"run_id"`
    Model      string           `json:"model"`
    Provenance Provenance       `json:"provenance"`
    Roles      []string         `json:"roles"`
    Results    []ReviewerResult `json:"results"`
    DryRun     bool             `json:"dry_run"`
}

func validateReviewerProvenance(content string, p Provenance) error
func writeRunArtifacts(outDir string, model string, p Provenance, b Bundle, prompts map[string]string, results []ReviewerResult, dryRun bool) error
func buildAdjudicationPacket(p Provenance, results []ReviewerResult, adjudicationRules string) string
```

- [ ] **Step 1: Write failing exact provenance validation tests**

Valid response must begin with exact `repoPath`, `headSHA`, `originMainSHA`, `migrationHead`, `gitStatus`, `premisesVerified: YES`. Test wrong SHA, migration, status, missing field, and `premisesVerified: NO` as failures.

- [ ] **Step 2: Write failing packet exclusion test**

Create one valid reviewer and one invalid reviewer; adjudication packet must include only the valid finding body and a `Missing/invalid lenses` section naming the invalid role.

- [ ] **Step 3: Implement validation using first-occurrence `key: value` parsing**

Split on the first colon only so Windows paths such as `C:\Users\...` remain valid values.

- [ ] **Step 4: Implement artifact writer**

Write `manifest.json`, `provenance.txt`, `source-bundle.txt`, prompt files, reviewer files, and `adjudication-packet.md`. Do not persist HTTP headers or the API key.

- [ ] **Step 5: Add secret non-persistence test**

Use a sentinel API key string and recursively inspect generated artifacts; sentinel must never appear.

- [ ] **Step 6: Run tests**

```bash
go test ./tools/review-council -run 'TestValidateReviewerProvenance|TestBuildAdjudicationPacket|TestArtifactsDoNotPersistAPIKey' -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add tools/review-council/output.go tools/review-council/output_test.go
git commit -m "feat(review-council): gate provenance and build adjudication packet"
```

---

### Task 6: CLI Wiring, Dry Run, Operator Documentation, and CI Gate

**Files:** `tools/review-council/main.go`, `docs/review-council/README.md`, `.gitignore`, `.github/workflows/ci.yml`.

**Interfaces:** command line described in the design spec.

- [ ] **Step 1: Implement repeatable `--include` flag and CLI validation**

Require `--candidate`; default `--base HEAD^`, `--roles all`, `--model` from `REVIEW_COUNCIL_MODEL` then `x-ai/grok-4.6`, `--max-source-bytes 600000`, default output under `.review-council/runs/`.

- [ ] **Step 2: Implement dry-run behavior**

Dry run captures provenance, builds source/prompts and writes artifacts but never constructs/sends an authenticated OpenRouter request. It must work without `OPENROUTER_API_KEY`.

- [ ] **Step 3: Implement live behavior**

Require `OPENROUTER_API_KEY`; create client with endpoint from `OPENROUTER_BASE_URL` or default; run selected roles concurrently; validate each response; write packet. Return non-zero if any requested lens failed or had invalid provenance while still preserving successful outputs.

- [ ] **Step 4: Update `.gitignore`**

Append:

```gitignore
# local adversarial review council output
/.review-council/
```

- [ ] **Step 5: Write operator README**

Document full council, bounded-review examples, dry run, environment variables, source-inclusion strategy, generated outputs, GPT-5.6 handoff, and explicit warning that Grok findings are advisory until adjudicated and reproduced.

- [ ] **Step 6: Add CI quality step**

Under the existing `quality` job after Go setup/tidy checks, run:

```yaml
- name: Review council harness tests
  run: go test ./tools/review-council
```

No API key is required because tests use `httptest.Server`.

- [ ] **Step 7: Run complete harness tests**

```bash
go test -race ./tools/review-council
```

Expected: PASS.

- [ ] **Step 8: Run a real local dry run against the current MED-004 surface**

Create a temporary candidate file outside committed product source or under local scratch and run:

```bash
go run ./tools/review-council \
  --dry-run \
  --candidate <candidate-file> \
  --base HEAD^ \
  --include backend/internal/usecase/recon \
  --include backend/internal/repo \
  --include backend/migrations/0085_med004a2_recovery_qualification_publisher.sql
```

Verify generated prompts contain the exact current `HEAD`, `origin/main`, migration head, shared BridgeXtra context, role lens, candidate, and line-numbered source.

- [ ] **Step 9: Run existing quality checks relevant to the new Go tool**

```bash
gofmt -w tools/review-council/*.go
go test -race ./tools/review-council
go vet ./tools/review-council
go build ./tools/review-council
```

Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
git add tools/review-council/main.go docs/review-council/README.md .gitignore .github/workflows/ci.yml
git commit -m "feat: wire BridgeXtra Grok 4.6 review council"
```

---

### Task 7: Repository-Level Verification

**Files:** no new files unless verification exposes a defect.

- [ ] **Step 1: Run repository quality gates locally where available**

```bash
go test -race ./tools/review-council
go vet ./...
go build ./...
```

- [ ] **Step 2: Push to `main` and inspect GitHub Actions**

Expected gates: `quality`, `backend`, `portal`, `portal-e2e`, `secret-scan`, `dependency-advisories`, and `dependency-review` all SUCCESS under the current push-enabled workflow.

- [ ] **Step 3: If CI exposes a harness defect, reproduce with a targeted RED test first, fix only that defect, rerun targeted tests, then one final full CI pass.**

- [ ] **Step 4: Record final setup SHA and council usage command in the handback.**

---

## Plan Self-Review

- Spec coverage: all design sections map to Tasks 1–7, including provenance, source budgets, concurrent reviewers, role selection, dry run, invalid-provenance exclusion, secret handling, GPT adjudication, docs, and CI.
- Placeholder scan: no TBD/TODO/implement-later placeholders.
- Type consistency: `Role`, `Provenance`, `Bundle`, `OpenRouterClient`, `ReviewerResult`, and `RunManifest` signatures are consistent across tasks.
- Scope: no product-runtime or database behavior changes; council remains an isolated tooling subsystem.
