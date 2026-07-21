-- 0045_operator_resolver_programme_grant.sql — Gate B #1 Slice 2 (chokepoint).
-- The OperatorReader resolves a programme's owning telco to pin app.telco_id for
-- a programme-scoped operator read (so the telco boundary is DB-enforced while
-- programme stays an intra-tenant app filter). That lookup runs on the trusted
-- resolver pool (worker locally / DB owner on Render, BYPASSRLS), which needs
-- SELECT on programmes. This is a metadata lookup — which telco owns a programme —
-- never tenant money data. Separate migration (not an edit of 0044) because 0044
-- is already applied; the runner keys on version int, so a new grant needs a new
-- migration to reach a DB that already ran 0044.
GRANT SELECT ON programmes TO tcp_worker;
