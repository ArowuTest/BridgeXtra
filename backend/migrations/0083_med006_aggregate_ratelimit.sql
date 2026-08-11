-- 0083_med006_aggregate_ratelimit.sql — BX-MED-006 (reopened): the cross-replica aggregate.
--
-- THE FINDING. ratelimit.New is called once per API process (cmd/api/main.go -> handler/
-- ratelimit.go), so the per-telco `channel` quota of 600 req/min is granted INDEPENDENTLY by every
-- replica: N replicas serve N x 600 to one partner. The in-process limiter is correct and stays
-- (defence in depth) — what is missing is a boundary that is authoritative ACROSS processes.
-- There is no Redis and no gateway in this stack (go.mod is pgx/kin-openapi/ulid), so the shared
-- store is Postgres. This table is that store.
--
-- SCOPE — IDENTITY-KEYED SURFACES ONLY, AND THAT IS A SECURITY DECISION, NOT A DEFERRAL.
-- The audit clause is "per-telco/credential limits in aggregate". `channel` is keyed by the
-- VALIDATED telco: bounded cardinality, an FK to telcos, no attacker-controlled row creation, and
-- a partner can only exhaust its own bucket — which is the intended semantics.
-- The other two surfaces (`login`, `channel_ip`) are keyed by CLIENT IP. Aggregating those needs a
-- fixed-width keyspace (IPs are unbounded and attacker-chosen), i.e. hash slots — and that would
-- introduce a vulnerability this system does not have today:
--   hashtext() is unsalted, unkeyed and computable OFFLINE on any local PostgreSQL of the deployed
--   major version. An attacker computes which slot a chosen VICTIM's IP falls in, finds a source IP
--   colliding into the same slot (offline, from a single IPv6 /64), drains the burst, and then
--   sends one request every two seconds. Denied requests consume no tokens, so the slot stays at
--   zero and the VICTIM is 429'd on /v1/portal/login from EVERY replica, indefinitely, for 0.5
--   req/s. Today that is impossible: the limiter keys the literal IP, so a flood can only exhaust
--   its own bucket, on only the replica that served it.
-- Slot aggregation would convert a per-source, per-replica limiter into a victim-selectable,
-- fleet-wide denial primitive. So it is NOT done here. Nothing forecloses it: the config SHAPE
-- accepts an `aggregate` block on any surface, and a salted-HMAC slot scheme (keyed, so the victim's
-- slot is not computable) is the upgrade path if the auditor wants it — noting it removes TARGETING
-- but not collateral collision-denial, which is inherent to sharing a bucket between strangers.
--
-- WHY NO RLS ON THIS TABLE. The honest answer, rather than a policy that looks like a control:
-- a WITH CHECK (telco_id = current_setting('app.telco_id')) would be a TAUTOLOGY here. repo/tx.go
-- sets that GUC from the very same telcoID variable the caller passes, and this table's telco_id
-- would be written from that same variable in the same call frame — the predicate evaluates x = x
-- and cannot fail, including for the bug it claims to prevent (a caller-influenced telco reaching
-- the limiter, which would flow into BOTH sides). The pattern works elsewhere in this tree only
-- because the GUC and the written value have DIFFERENT provenance. Here the structural bound is the
-- FK below, plus a source fence test asserting the telco argument is only ever the value returned by
-- platform.TenantFrom. These rows are operational counters, not customer data.

CREATE TABLE rate_limit_buckets (
  scope                TEXT        NOT NULL,
  telco_id             TEXT        NOT NULL REFERENCES telcos(telco_id),

  -- Tokens are MILLI-tokens (1 request = 1000) so refill is exact integer arithmetic and a
  -- fractional refill is never silently rounded away.
  tokens_milli         BIGINT      NOT NULL CHECK (tokens_milli >= 0),

  -- The quota lives ON THE ROW and is AUTHORITATIVE. Passing it per-process into every call is what
  -- lets a rolling deploy redefine a shared ceiling: two replicas booted on different config
  -- versions would each assert their own burst against one row, and the stale (larger) one wins.
  -- Here the row holds the quota and the newest-effective config adopts it (see quota_effective_from).
  -- STRUCTURAL CHECKs, mirroring 0054's arm_freshness_max_seconds: no writer — migration, seed, bug,
  -- or a poisoned adopt — can store an unbounded ceiling on a value this authoritative.
  burst_milli          BIGINT      NOT NULL CHECK (burst_milli BETWEEN 1000 AND 1000000000),
  refill_milli_per_sec BIGINT      NOT NULL CHECK (refill_milli_per_sec BETWEEN 1 AND 10000000),

  -- Which governed config the row's quota came from. ORDERED BY effective_from, NOT version_no:
  -- version_no is allocated MAX+1 per (domain,scope), so a per-telco override and the global scope
  -- draw from INDEPENDENT counters and version_no is not a total order across rows that can govern
  -- the same bucket. effective_from is globally comparable and is what the config resolver already
  -- orders by.
  quota_effective_from TIMESTAMPTZ NOT NULL,

  last_refill_at       TIMESTAMPTZ NOT NULL,

  -- Whether the most recent spend was granted. This is RETURNED by the upsert. It exists because
  -- exhaustion must be a first-class DENY, never an error: writing the deny as a conditional
  -- DO UPDATE ... WHERE tokens >= 1000 makes an over-limit request return zero rows (pgx:
  -- ErrNoRows), which is indistinguishable from a store failure — so ordinary, expected 429 traffic
  -- would be counted as the store breaking and would trip the unavailability fallback.
  last_grant           BOOLEAN     NOT NULL,

  PRIMARY KEY (scope, telco_id),
  -- Tokens can never exceed the ceiling they are counted against.
  CHECK (tokens_milli <= burst_milli)
);

-- tcp_app spends tokens: it must INSERT (first touch for a telco), UPDATE (the spend) and SELECT.
-- No DELETE: rows are bounded by the telcos table and there is nothing to prune.
GRANT SELECT, INSERT, UPDATE ON rate_limit_buckets TO tcp_app;

-- Operator visibility. A cross-replica control with no way to see the current token level is
-- unfalsifiable in production — the fallback path in particular would otherwise be visible only to
-- whoever is reading logs. Read-only: operators observe the boundary, they do not spend it.
GRANT SELECT ON rate_limit_buckets TO tcp_operator;

-- The governed config gains an `aggregate` block on `channel` only (see the scope decision above).
-- Landed as a MIGRATION, not a portal write, and that ordering is load-bearing: the config WRITE
-- path decodes with DisallowUnknownFields, so an old replica still serving the portal would REJECT
-- a hand-authored document carrying this new key. The BOOT path uses a plain json.Unmarshal, so old
-- binaries simply ignore the block. Migrations run before services start (cmd/api/PLANES.md), so
-- migrate-then-deploy lands cleanly in both directions; authoring further ratelimit config changes
-- through the portal must wait until the rollout completes.
UPDATE config_versions
   SET state = 'SUPERSEDED', effective_to = now()
 WHERE config_version_id = 'cfg_seed_ratelimit_v2';

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ratelimit_v3', 'platform.ratelimit', 'global', 3, 'ACTIVE',
   '{"trusted_proxy_count":1,"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120,"aggregate":{"requests_per_minute":600,"burst":120}},"channel_ip":{"requests_per_minute":1200,"burst":240}}}',
   encode(sha256('{"trusted_proxy_count":1,"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120,"aggregate":{"requests_per_minute":600,"burst":120}},"channel_ip":{"requests_per_minute":1200,"burst":240}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'BX-MED-006: aggregate cross-replica quota for the per-telco channel surface (600/min burst 120), shared through rate_limit_buckets. login/channel_ip stay replica-local by design — slot-aggregating IP-keyed surfaces would create a targeted fleet-wide lockout (see 0083 header).');
