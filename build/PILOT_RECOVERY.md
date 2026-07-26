# RECOVERY-recon end-to-end simulator pilot

The **demonstrable end-to-end pilot** for the airtime-lending money-assurance
backbone: it drives the **real operator binaries** (`cmd/migrate`, `cmd/simseed`,
`cmd/worker`) against a fresh local Postgres and proves the whole armable-money-door
sequence — seed → four-eyes arm → reconcile (goes live) → inject a break → break
caught. This is real-infra smoke of the actual entrypoints; the Go integration seam
(`backend/internal/usecase/recon/recon_integration_seam_test.go`) proves the same
story adversarially in-process against DB truth.

## Run it

```bash
# needs Docker + a running Postgres container (local dev container)
bash build/pilot_recovery.sh
```

Non-destructive: everything runs in a throwaway `telco_credit_pilot` database that is
dropped and recreated each run. Overrides: `TCP_PILOT_PGC` (container name),
`TCP_PILOT_HOSTPORT` (host port, default 5434), `TCP_PILOT_DAY` (Lagos business day).

## What each step proves

| Step | Command (via the real binaries) | Proves |
|---|---|---|
| 1 | `cmd/migrate` | schema + roles + `SIM_NG` (ACTIVE, `is_synthetic=true`) |
| 2 | `cmd/simseed -recovery-day` | deterministic synthetic day: platform `wh:` recovery events **and** the matching per-subscriber telco EOD feed rows, from one ground truth |
| 3 | `cmd/worker -recon-arm-propose` + `-recon-arm-approve` | **four-eyes** money-door: maker proposes, **self-approve is rejected** (two-person rule), a **distinct** checker approves → armed but **not live** (`last_recon_at` NULL) |
| 4 | `cmd/worker -recon` | the clean seeded day reconciles (`matched=10`, no breaks), exit 0, and **freshness advances → the layer goes LIVE** (`last_recon_at` stamped) — the sole satisfier of the S2 recharge-webhook gate |
| 5–6 | delete one EOD feed row → `cmd/worker -recon` | a booked recovery the feed no longer confirms is **caught** as `BREAK_MISSING_TELCO`; recon **exits 1** (operator-alertable, V2-REC-012); the break is **persisted, never auto-resolved** |

The fulfilment programme (`prg_sim_airtime01`) is suspended for the pilot to isolate
the RECOVERY layer — fulfilment recon fetches from the M1 telco simulator, which is
out of scope here; the RECOVERY recon runs per-telco, independent of programmes.

## Evidence (captured run, 2026-07-26)

```
STEP 1 — migrate
  migrations applied: 57
  SIM_NG status=ACTIVE synthetic=true

STEP 2 — seed synthetic RECOVERY day (2026-07-25)
  simseed: recovery day 2026-07-25 done — 15 event(s) + 10 feed row(s) created, control total 3257096 minor
  wh_events=15 subscribers=10
  eod_feed_rows=10 total_minor=3257096

STEP 3 — four-eyes arm
  maker proposed arm request: rar_01KYFXV5J6GVXPQGSHX1DF85S0
  self-approve  -> ERROR "recon: an arm request checker must differ from its proposer (two-person rule)"
  checker@pilot -> INFO  "recovery arm approved (checker) — live on next confirmed recon"
  armed=yes last_recon_at=NULL(not-live)

STEP 4 — recon: clean day -> LIVE
  recon-recovery SIM_NG period=[2026-07-24T23:00:00Z,2026-07-25T23:00:00Z) matched=10 missing_platform=0 missing_telco=0 amount_mismatch=0
  INFO "reconciliation clean across all active telcos/programmes"
  >>> recon exit code = 0
  last_recon_at=2026-07-26 19:19:07.611197+00        <- LIVE

STEP 5 — inject break: DELETE 1 EOD feed row (10 -> 9)

STEP 6 — recon: break CAUGHT
  ERROR "reconciliation breaks found — operator attention required (V2-REC-012)" breaks=1 matched=9
  recon-recovery SIM_NG period=[2026-07-24T23:00:00Z,2026-07-25T23:00:00Z) matched=9 missing_platform=0 missing_telco=1 amount_mismatch=0
  >>> recon exit code = 1
  open_recovery_breaks=1   break_type=BREAK_MISSING_TELCO
```

`clean_recon_exit=0` and `break_recon_exit=1` — the money door arms only under
two-actor control, goes live only on a confirmed feed, and any divergence between the
platform's booked recoveries and the telco feed is caught and surfaced, never
auto-resolved.
