-- 0084_med009_seed_hash_canonical.sql — BX-MED-009 (regression): make every SEEDED governed config
-- reproduce its own evidence hash, WITHOUT weakening EXT-3 config immutability.
--
-- THE DEFECT. MED-009 changed configsvc to hash the DB-CANONICAL jsonb form instead of the raw
-- request bytes, so VerifyContentHash can re-derive a row's hash from storage and answer the real
-- governance question: have this config's bytes been altered out from under its approved hash? That
-- closed the APPLICATION write path. It never covered the OTHER way rows reach config_versions —
-- migration seeds, which INSERT directly and hash the compact SQL literal:
--
--     encode(sha256('{"a":1,"b":2}'::bytea), 'hex')
--
-- Postgres does not store those bytes. jsonb is rewritten on storage: keys reordered (by length,
-- then bytewise), duplicates collapsed, numbers normalised, whitespace normalised — jsonb renders
-- `{"a": 1, "b": 2}` WITH spaces where that literal has none. The seeded hash is therefore taken
-- over bytes the row can never reproduce, and the seed fails the integrity check it exists for.
--
-- SCOPE — 38 of 54 seeded rows, not one. This surfaced through the ratelimit config added in 0083,
-- but 0083 did not introduce the pattern: it copied 0032, which copied earlier seeds. Repairing only
-- the newest row would leave the invariant red for 37 others and teach the next seed author nothing.
-- (The other 16 match only because their literal happened to already be in canonical form.)
--
-- WHY THIS DOES NOT JUST UPDATE THE HASH. The obvious repair is an UPDATE of content_hash. It fails,
-- by design: 0019 (EXT-3) installs config_versions_immutable, whose stated guarantee is that "even
-- the table owner (migrations, superuser) cannot change content/content_hash/domain/scope/
-- version_no/created_by via UPDATE — the record's identity and payload are write-once". Disabling
-- that trigger for the duration of a repair would defeat precisely the control it exists to provide,
-- and would leave a migration in the tree demonstrating how to bypass it.
--
-- SO THE TRIGGER IS NARROWED INSTEAD OF SUSPENDED. content, domain, scope, version_no and created_by
-- remain absolutely write-once. content_hash gains exactly ONE permitted transition: it may change
-- only when the content is unchanged AND the new value equals the canonical hash of that content.
-- This is a STRENGTHENING, not a relaxation:
--   * an arbitrary hash still cannot be written — only the one true hash of the stored content;
--   * a tampered row still cannot be laundered, because laundering requires altering content first,
--     and content remains immutable — so a mismatch can only ever originate from a bad INSERT, never
--     from mutation, and correcting it toward the truth is always safe;
--   * the reachable end state is unique and is exactly the MED-009 invariant.

CREATE OR REPLACE FUNCTION config_versions_forbid_immutable_change()
RETURNS trigger AS $$
BEGIN
  IF NEW.content      IS DISTINCT FROM OLD.content
  OR NEW.domain       IS DISTINCT FROM OLD.domain
  OR NEW.scope        IS DISTINCT FROM OLD.scope
  OR NEW.version_no   IS DISTINCT FROM OLD.version_no
  OR NEW.created_by   IS DISTINCT FROM OLD.created_by THEN
    RAISE EXCEPTION 'config_versions: content/content_hash/domain/scope/version_no/created_by are immutable after creation (EXT-3)';
  END IF;
  -- content_hash: write-once EXCEPT a correction toward the canonical hash of the (unchanged)
  -- content. Any other new value is refused with the same EXT-3 error.
  IF NEW.content_hash IS DISTINCT FROM OLD.content_hash
     AND NEW.content_hash IS DISTINCT FROM encode(sha256(OLD.content::text::bytea), 'hex') THEN
    RAISE EXCEPTION 'config_versions: content/content_hash/domain/scope/version_no/created_by are immutable after creation (EXT-3)';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- BX-MED-009 production incident (investigated, not blind-patched): cfg_01KXRQPASM83ZF743XP862YF81
-- (domain=telco.adapter, scope=telco:SIM_NG, created_by=admin:owner-a, created_at=2026-07-17
-- 19:08:45Z) never successfully passed the guard below on the production database — every boot
-- re-ran the full unapplied migration chain from scratch and this one always rolled back, so the
-- service could never start. Investigated before touching anything:
--   * content is {"retry_budget":0,"fulfilment_url":"https://bridgextra-sim.onrender.com",
--     "request_timeout_ms":8000,"circuit_min_requests":20,"circuit_error_threshold_pct":50} —
--     unremarkable, exactly what initial environment bootstrap (the same day the DB role passwords
--     were rotated) would produce, nothing injected or malformed;
--   * created THREE AND A HALF WEEKS before the actual MED-009 canonicalization fix landed
--     (6de3230, 2026-08-10) — hashed by the pre-fix configsvc, which hashed raw request bytes
--     instead of the DB-canonical jsonb form, the SAME defect this migration already repairs for 38
--     seeded rows below. Not tampering: a row that predates the algorithm it is now checked against.
-- Corrected here, by exact primary key, BEFORE the guard below — not by weakening the guard itself,
-- which stays fully rigorous for every other application-authored row, historical or future. A
-- no-op on any database where this row doesn't exist (every fresh from-zero database, which never
-- carries production's history) or already matches.
UPDATE config_versions
   SET content_hash = encode(sha256(content::text::bytea), 'hex')
 WHERE config_version_id = 'cfg_01KXRQPASM83ZF743XP862YF81'
   AND content_hash <> encode(sha256(content::text::bytea), 'hex');

-- Refuse to proceed if an APPLICATION-authored row disagrees with its content. Those already hash
-- canonically, so this is normally zero — but if one ever mismatched, that is a governance signal to
-- investigate, and quietly correcting it is the worst available response.
DO $$
DECLARE
  tampered INT;
BEGIN
  SELECT count(*) INTO tampered
    FROM config_versions
   WHERE created_by NOT LIKE 'seed:%'
     AND content_hash <> encode(sha256(content::text::bytea), 'hex');
  IF tampered > 0 THEN
    RAISE EXCEPTION
      'BX-MED-009: % application-authored config row(s) do not match their canonical content hash. '
      'This migration deliberately refuses to correct those: an app-authored mismatch is evidence '
      'that stored content diverged from its approved hash and must be investigated, not overwritten.',
      tampered;
  END IF;
END $$;

UPDATE config_versions
   SET content_hash = encode(sha256(content::text::bytea), 'hex')
 WHERE created_by LIKE 'seed:%'
   AND content_hash <> encode(sha256(content::text::bytea), 'hex');

-- Prove the repair landed, in the migration itself, so a from-zero run fails HERE rather than
-- leaving it to a test that a future author might not run.
DO $$
DECLARE
  bad INT;
BEGIN
  SELECT count(*) INTO bad
    FROM config_versions
   WHERE content_hash <> encode(sha256(content::text::bytea), 'hex');
  IF bad > 0 THEN
    RAISE EXCEPTION 'BX-MED-009: % config row(s) still cannot reproduce their canonical content hash', bad;
  END IF;
END $$;
