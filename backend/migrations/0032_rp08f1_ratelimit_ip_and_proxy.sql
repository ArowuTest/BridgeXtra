-- 0032_rp08f1_ratelimit_ip_and_proxy.sql — R-P0-8a-F1 (reviewer) + R-P2-7.
-- The v1 channel limiter keyed on the api-key, so a rotating-invalid-key flood
-- got a fresh bucket per request and was never throttled. v2 adds:
--   channel_ip  — the always-on PRE-auth IP throttle (a rotating flood shares
--                 one IP bucket); the per-telco `channel` bucket now applies
--                 POST-auth on the validated telco.
--   trusted_proxy_count — how many proxies in front to trust for X-Forwarded-
--                 For (R-P2-7). Behind Render's LB, RemoteAddr is the proxy for
--                 every client, so IP-keying without this collapses to one
--                 global bucket. Seeded 1 for the Render topology; a directly-
--                 exposed deploy must set it to 0.
--
-- Supersedes v1 (config immutability + ACTIVE-overlap exclusion; now() is the
-- tx timestamp so v1.effective_to == v2.effective_from).

UPDATE config_versions
   SET state = 'SUPERSEDED', effective_to = now()
 WHERE config_version_id = 'cfg_seed_ratelimit_v1';

INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_ratelimit_v2', 'platform.ratelimit', 'global', 2, 'ACTIVE',
   '{"trusted_proxy_count":1,"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120},"channel_ip":{"requests_per_minute":1200,"burst":240}}}',
   encode(sha256('{"trusted_proxy_count":1,"surfaces":{"login":{"requests_per_minute":30,"burst":10},"channel":{"requests_per_minute":600,"burst":120},"channel_ip":{"requests_per_minute":1200,"burst":240}}}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'R-P0-8a-F1: add channel_ip pre-auth IP throttle (1200/min burst 240) + trusted_proxy_count=1 (Render). Rotating-key flood shares one IP bucket; per-telco fairness moves post-auth.');
