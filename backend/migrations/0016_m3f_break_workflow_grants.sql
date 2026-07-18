-- 0016_m3f_break_workflow_grants.sql — the M3f breaks workflow writes the
-- lifecycle columns 0011 added; the app role gets exactly those columns and
-- nothing else (status stays recon-engine territory, detail stays immutable).
GRANT UPDATE (assigned_to, resolved_at, resolution) ON recon_items TO tcp_app;
