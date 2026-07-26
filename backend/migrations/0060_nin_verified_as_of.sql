-- 0060_nin_verified_as_of.sql — Build 2 follow-on: NIN revocation-safety (monotonicity).
-- The Build 2 nin_verified upsert was an unconditional overwrite. An out-of-order or
-- reprocessed MTN cut carrying nin_verified=true could silently clobber a NEWER
-- revocation (false) — un-revoking a subscriber's identity. This adds the date basis
-- to make the flag write MONOTONIC: only a same-or-newer feed cut may change it.
--
-- ONE more in-place column, no history rows (owner's no-accumulation intent). Sourced
-- from the feature file's as_of (the authoritative cut date). NULL = never yet dated
-- (any cut applies — the bootstrap/backfilled rows).

ALTER TABLE subscriber_accounts ADD COLUMN nin_verified_as_of TIMESTAMPTZ;
GRANT UPDATE (nin_verified_as_of) ON subscriber_accounts TO tcp_app;
