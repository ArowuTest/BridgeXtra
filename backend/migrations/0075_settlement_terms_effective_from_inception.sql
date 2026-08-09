-- 0075_settlement_terms_effective_from_inception.sql — supports BX-HIGH-011.
--
-- settlement.Generate now resolves settlement.terms at the settlement PERIOD's start (the
-- contractual instant the period's obligations began), not at generation time — so a
-- statement generated or backfilled AFTER a terms renegotiation no longer applies the NEW
-- terms to an OLD period.
--
-- The seeded terms used effective_from = now() (the migration instant), so a settlement for
-- any period BEFORE the migration ran would find NO terms in force during it. A real
-- programme's terms are in force from its inception; backdate the seed so period-anchored
-- resolution finds them. Only effective_from moves — content, hash and identity are
-- untouched, so the EXT-3 config immutability trigger is satisfied.
UPDATE config_versions
   SET effective_from = TIMESTAMPTZ '2020-01-01 00:00:00+00'
 WHERE config_version_id = 'cfg_seed_settlement_terms_v1'
   AND effective_from > TIMESTAMPTZ '2020-01-01 00:00:00+00';
