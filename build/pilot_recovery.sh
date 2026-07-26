#!/usr/bin/env bash
# build/pilot_recovery.sh — demonstrable end-to-end RECOVERY-recon simulator pilot.
#
# Drives the REAL cmd binaries (migrate, simseed, worker) against a fresh local
# Postgres to prove the whole operator sequence for the airtime-lending money-
# assurance backbone:
#
#   seed synthetic day -> FOUR-EYES arm (self-approve rejected) -> recon (clean,
#   layer goes LIVE) -> inject a break -> recon (break CAUGHT, exit 1).
#
# This is real-infra smoke of the actual operator entrypoints — a higher-fidelity
# complement to the in-process integration seam (backend/internal/usecase/recon/
# recon_integration_seam_test.go), which proves the same story adversarially in Go.
#
# Prereqs: Docker, and a running Postgres container (default name/port below match
# the local dev container). The golang:1.25 image is used to `go run` the binaries
# so no host Go toolchain is required. Nothing is written outside a throwaway
# `telco_credit_pilot` database (dropped+recreated each run) — non-destructive.
#
# Env overrides:
#   TCP_PILOT_PGC       Postgres container name        (default: telco-credit-postgres)
#   TCP_PILOT_HOSTPORT  host port -> container 5432     (default: 5434; local dev = 5434)
#   TCP_PILOT_DAY       Lagos business day to seed      (default: yesterday, UTC)
set -uo pipefail

PGC="${TCP_PILOT_PGC:-telco-credit-postgres}"
PORT="${TCP_PILOT_HOSTPORT:-5434}"
DB=telco_credit_pilot
HP="host.docker.internal:${PORT}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RDAY="${TCP_PILOT_DAY:-$(date -u -d 'yesterday' +%Y-%m-%d 2>/dev/null || date -u -v-1d +%Y-%m-%d)}"

ADMIN_DSN="postgres://postgres:devlocal@${HP}/${DB}"
APP_DSN="postgres://tcp_app:devlocal_app@${HP}/${DB}"
WORKER_DSN="postgres://tcp_worker:devlocal_worker@${HP}/${DB}"

# gorun <-e ENV...> <sh -c command> — run a go cmd in the golang container on the
# same host-network path the test suite uses.
gorun() {
  MSYS_NO_PATHCONV=1 docker run --rm --add-host=host.docker.internal:host-gateway \
    $1 \
    -v "${REPO}":/app -v tcp-gomodcache:/go/pkg/mod -v tcp-gobuildcache:/root/.cache/go-build \
    -w /app golang:1.25 sh -c "$2"
}
psql()  { docker exec -e PGPASSWORD=devlocal "${PGC}" psql -U postgres -d "${1}" -tAc "${2}" 2>&1; }
psqlm() { docker exec -e PGPASSWORD=devlocal "${PGC}" psql -U postgres -c "${1}" 2>&1; }

echo "================ STEP 0 — fresh pilot database ================"
psqlm "DROP DATABASE IF EXISTS ${DB} WITH (FORCE);"
psqlm "CREATE DATABASE ${DB};"

echo; echo "================ STEP 1 — migrate (schema + roles + SIM_NG seed) ================"
gorun "-e TCP_ADMIN_DSN=${ADMIN_DSN}" 'go run ./backend/cmd/migrate' 2>&1 | grep -iE "migration|applied|error|panic" | tail -6
psql "${DB}" "SELECT telco_id||' status='||status||' synthetic='||is_synthetic FROM telcos;"
# Isolate the RECOVERY layer: suspend the fulfilment programme. Fulfilment recon
# fetches from the M1 telco simulator (out of scope for this RECOVERY pilot); the
# RECOVERY recon runs per-telco, independent of programmes.
psql "${DB}" "UPDATE programmes SET status='SUSPENDED' WHERE programme_id='prg_sim_airtime01';" >/dev/null

echo; echo "================ STEP 2 — seed synthetic cohort + one clean RECOVERY day (${RDAY}) ================"
gorun "-e TCP_SEED_ALLOW=1 -e TCP_SIMSEED_DSN=${APP_DSN}" \
  "go run ./backend/cmd/simseed -seed pilot -subscribers 10 -recovery-day ${RDAY} -recovery-count 10" 2>&1 | grep -iE "simseed:|error|panic" | tail -4
psql "${DB}" "SELECT 'wh_events='||count(*)||' subscribers='||count(DISTINCT msisdn_token) FROM recovery_events WHERE source_event_id LIKE 'wh:%';"
psql "${DB}" "SELECT 'eod_feed_rows='||count(*)||' total_minor='||COALESCE(SUM(recovery_deducted_minor),0) FROM recovery_eod_feed WHERE business_date=DATE '${RDAY}';"

echo; echo "================ STEP 3 — FOUR-EYES arm the RECOVERY money door ================"
REQ=$(gorun "-e TCP_WORKER_DSN=${WORKER_DSN} -e TCP_APP_DSN=${APP_DSN}" \
  "go run ./backend/cmd/worker -recon-arm-propose SIM_NG -actor maker@pilot -reason pilot_arm -arm-reversal-basis NET_SAME_DAY -arm-date-basis occurred_at_lagos_date" 2>/dev/null | grep -oE 'rar_[A-Za-z0-9]+' | tail -1)
echo "   maker proposed arm request: ${REQ}"
echo "-- self-approve MUST be rejected (two-person rule):"
gorun "-e TCP_WORKER_DSN=${WORKER_DSN} -e TCP_APP_DSN=${APP_DSN}" \
  "go run ./backend/cmd/worker -recon-arm-approve ${REQ} -actor maker@pilot" 2>&1 | grep -iE "two-person|differ|reject|err" | tail -2
echo "-- distinct checker approves:"
gorun "-e TCP_WORKER_DSN=${WORKER_DSN} -e TCP_APP_DSN=${APP_DSN}" \
  "go run ./backend/cmd/worker -recon-arm-approve ${REQ} -actor checker@pilot" 2>&1 | grep -iE "armed|approve" | tail -2
psql "${DB}" "SELECT 'armed=yes last_recon_at='||COALESCE(last_recon_at::text,'NULL (not-live yet)') FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer='RECOVERY';"

echo; echo "================ STEP 4 — recon: clean day reconciles, layer goes LIVE ================"
gorun "-e TCP_WORKER_DSN=${WORKER_DSN} -e TCP_APP_DSN=${APP_DSN}" "go run ./backend/cmd/worker -recon" > /tmp/pilot_recon1.txt 2>&1
echo "   >>> recon exit = $?  (0 = clean)"
grep -iE "recon-recovery|reconciliation clean" /tmp/pilot_recon1.txt | tail -6
psql "${DB}" "SELECT 'last_recon_at='||COALESCE(last_recon_at::text,'NULL') FROM recon_layer_arming WHERE telco_id='SIM_NG' AND layer='RECOVERY';"

echo; echo "================ STEP 5 — inject a break: drop one telco EOD feed row ================"
psql "${DB}" "DELETE FROM recovery_eod_feed WHERE ctid = (SELECT ctid FROM recovery_eod_feed WHERE business_date=DATE '${RDAY}' ORDER BY msisdn_token LIMIT 1);"

echo; echo "================ STEP 6 — re-run recon: the break is CAUGHT ================"
gorun "-e TCP_WORKER_DSN=${WORKER_DSN} -e TCP_APP_DSN=${APP_DSN}" "go run ./backend/cmd/worker -recon" > /tmp/pilot_recon2.txt 2>&1
echo "   >>> recon exit = $?  (non-zero = break detected)"
grep -iE "recon-recovery|breaks found" /tmp/pilot_recon2.txt | tail -4
psql "${DB}" "SELECT 'open_recovery_breaks='||count(*)||' type='||COALESCE(max(i.status),'-') FROM recon_items i JOIN recon_runs r ON r.run_id=i.run_id WHERE r.layer='RECOVERY' AND r.state='ACTIVE' AND i.status LIKE 'BREAK_%' AND i.resolved_at IS NULL;"

echo; echo "================ PILOT COMPLETE ================"
