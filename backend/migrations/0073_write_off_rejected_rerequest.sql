-- 0073 — Collections write-off: allow re-request after a REJECTED write-off (owner-approved
-- money-model change). The blanket UNIQUE(advance_id) on write_offs conflated two guarantees:
--   * "no two LIVE write-offs on one advance"  — correct, must keep; and
--   * "no re-request EVER"                      — wrong for a rejection.
-- A rejection is a "not now", not a permanent bar: an advance can deteriorate further months
-- later and warrant a write-off then. Replace the blanket UNIQUE with a PARTIAL unique index that
-- excludes REJECTED, so:
--   * still at most one LIVE write-off per advance (REQUESTED/APPROVED/POSTED all counted);
--   * a POSTED write-off STILL permanently bars re-request (POSTED is non-REJECTED, so a new
--     REQUESTED collides) — and the FSM already blocks it too (the advance is WRITTEN_OFF);
--   * a REJECTED write-off no longer blocks a later re-request (it is excluded from the index).
--
-- Watch-outs cleared:
--   * The 0021 immutability trigger is BEFORE UPDATE FOR EACH ROW — it does NOT fire on this DDL,
--     so the constraint swap applies cleanly, and it keeps the rejected row itself frozen.
--   * The repo Insert's arbiter moves in lockstep: it no longer uses ON CONFLICT (which cannot
--     infer a partial index without repeating the predicate); it catches the partial index's 23505
--     by name (write_offs_one_live_per_advance) → ErrWriteOffExists.
--   * From zero: 0011 creates the single-column UNIQUE as write_offs_advance_id_key (PG default),
--     which this drops by that deterministic name.

ALTER TABLE write_offs DROP CONSTRAINT write_offs_advance_id_key;

CREATE UNIQUE INDEX write_offs_one_live_per_advance
  ON write_offs (advance_id)
  WHERE state <> 'REJECTED';
