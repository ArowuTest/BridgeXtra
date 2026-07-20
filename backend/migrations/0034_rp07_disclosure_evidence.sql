-- 0034_rp07_disclosure_evidence.sql — R-P0-7 (AUD-P0-007, REG-002, PRD-002/003,
-- EDG-028). Confirm proved only WHICH offer existed, not that THOSE EXACT terms
-- were rendered and accepted through a real channel: it reconstructed the terms
-- server-side and hardcoded Channel='USSD'. This closes the conduct-evidence
-- gap by minting a disclosure SNAPSHOT at offer generation (the exact rendered
-- disclosure, 1:1 with the offer, append-only + content-hashed) and requiring,
-- at confirm, that the customer echo back that snapshot's reference plus the
-- channel/session/acceptance evidence — captured into the consent record.
--
-- Design (see build/TELCO_INTERFACE_CONTRACT_DD06.md): the snapshot is the
-- authoritative, tamper-evident, retained record on OUR side — its integrity
-- comes from append-only storage + the server-computed content_hash, and the
-- client cannot fabricate a reference that resolves to another offer. The
-- separate cryptographic telco-evidence SIGNATURE (offline-verifiable by third
-- parties) is a telco-interface-contract concern because it originates
-- telco-side; the nullable consents.telco_evidence column is that DD-06 slot.

-- ---------------------------------------------------------------------------
-- disclosure_snapshots: what WE presented for one offer, minted at menu time.
-- Mirrors decision_snapshots (append-only evidence). One per offer (UNIQUE).
-- ---------------------------------------------------------------------------
CREATE TABLE disclosure_snapshots (
  disclosure_snapshot_id TEXT PRIMARY KEY,
  telco_id            TEXT NOT NULL,
  programme_id        TEXT NOT NULL REFERENCES programmes(programme_id),
  offer_id            TEXT NOT NULL REFERENCES offers(offer_id),
  template_id         TEXT NOT NULL,             -- disclosure.policy template
  template_version    TEXT NOT NULL,             -- pinned template version
  locale              TEXT NOT NULL,             -- rendered locale (e.g. en-NG)
  disclosure_config_version_id TEXT NOT NULL,    -- pinned disclosure.policy version
  currency            CHAR(3) NOT NULL,
  face_value_minor    BIGINT NOT NULL CHECK (face_value_minor > 0),
  fee_minor           BIGINT NOT NULL CHECK (fee_minor >= 0),
  disbursed_minor     BIGINT NOT NULL CHECK (disbursed_minor > 0),
  repayment_minor     BIGINT NOT NULL CHECK (repayment_minor > 0),
  rendered_body       TEXT NOT NULL,             -- EXACT text presented
  total_cost_text     TEXT NOT NULL,             -- total-cost representation
  content_hash        TEXT NOT NULL,             -- sha256 of the canonical snapshot
  issued_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at          TIMESTAMPTZ NOT NULL,      -- = offer.expires_at (short-lived)
  UNIQUE (offer_id)
);
CREATE INDEX disclosure_snapshots_offer_ix ON disclosure_snapshots (offer_id);

ALTER TABLE disclosure_snapshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY t_disclosure_snapshots ON disclosure_snapshots
  USING (telco_id = current_setting('app.telco_id', true))
  WITH CHECK (telco_id = current_setting('app.telco_id', true));
-- Append-only: minted once at offer generation, never mutated. No UPDATE/DELETE.
GRANT SELECT, INSERT ON disclosure_snapshots TO tcp_app;
GRANT SELECT ON disclosure_snapshots TO tcp_worker;

-- ---------------------------------------------------------------------------
-- consents enrichment: bind the accepted terms to the snapshot the customer
-- was shown, and capture the channel/session/acceptance evidence. These become
-- REQUIRED (NOT NULL) — an advance cannot exist without proof of what was
-- disclosed and that it was accepted through a real channel session. telco_
-- evidence is the optional DD-06 slot for a telco-supplied acceptance
-- signature. Fresh-from-zero migration + pre-launch, so NOT NULL is safe.
-- consents keeps its SELECT,INSERT-only grant (append-only) — new columns are
-- covered by the existing table-level grant.
-- ---------------------------------------------------------------------------
ALTER TABLE consents
  ADD COLUMN disclosure_snapshot_id TEXT NOT NULL REFERENCES disclosure_snapshots(disclosure_snapshot_id),
  ADD COLUMN session_id    TEXT NOT NULL,        -- channel session (telco-supplied)
  ADD COLUMN accepted_at   TIMESTAMPTZ NOT NULL, -- when the customer accepted
  ADD COLUMN telco_evidence JSONB;               -- DD-06: telco acceptance signature (optional)

-- ---------------------------------------------------------------------------
-- Seed disclosure.policy for the sim programme. Programme-scoped like
-- product.airtime (terms are per-product). body_template / total_cost_template
-- MUST reference {{repayment}} — a disclosure that omits the total obligation
-- is not a disclosure (the validator enforces this; armed-but-dead floor).
-- allowed_channels de-hardcodes the previously literal 'USSD'.
-- ---------------------------------------------------------------------------
INSERT INTO config_versions
  (config_version_id, domain, scope, version_no, state, content, content_hash,
   effective_from, created_by, approved_by, reason)
VALUES
  ('cfg_seed_disclosure_v1', 'disclosure.policy', 'programme:prg_sim_airtime01', 1, 'ACTIVE',
   '{"template_id":"USSD_ADVANCE_DISCLOSURE","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD","APP"],"body_template":"You are borrowing {{face}} (fee {{fee}}). You will repay {{repayment}} from your recharges.","total_cost_template":"Total to repay: {{repayment}} = {{face}} advance + {{fee}} fee."}',
   encode(sha256('{"template_id":"USSD_ADVANCE_DISCLOSURE","template_version":"v1","default_locale":"en-NG","supported_locales":["en-NG"],"allowed_channels":["USSD","APP"],"body_template":"You are borrowing {{face}} (fee {{fee}}). You will repay {{repayment}} from your recharges.","total_cost_template":"Total to repay: {{repayment}} = {{face}} advance + {{fee}} fee."}'::bytea),'hex'),
   now(), 'seed:builder', 'seed:reviewer',
   'R-P0-7 seed disclosure policy (REG-002): pinned template+version+locale, allowed channels de-hardcode USSD, body must disclose the repayment total. Rendered snapshot minted per offer at menu time and echoed back at confirm.');
