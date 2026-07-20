-- 0031_rp08_ratelimit_config.sql — R-P0-8. Governed inbound rate-limit
-- thresholds (validator: validators_ratelimit.go). Seeded ACTIVE so the two
-- inbound edges — portal /login and the telco channel API — have a backstop
-- from first boot. The API refuses to start if this config is absent, so
-- there is never a running service without a rate limiter.
--
--   login:   30 req/min per client IP, burst 10  (anti credential-stuffing /
--            brute force; a real operator logs in a handful of times an hour)
--   channel: 600 req/min per telco credential, burst 120  (generous for a
--            legitimate telco feed; a hammering or looping partner is throttled)

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ratelimit_v1', 'platform.ratelimit', 'global', 1, 'ACTIVE',
   '{"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120}}}',
   encode(sha256('{"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'R-P0-8: seeded inbound rate limits — login 30/min burst 10, channel 600/min burst 120. Admin-tunable; the API fails closed (refuses to boot) if absent.');
