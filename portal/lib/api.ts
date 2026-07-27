// Portal API client. The session lives in an httpOnly cookie the JS cannot read;
// the CSRF token lives in a separate readable `bx_csrf` cookie (see below). NOTE:
// authorization lives on the SERVER (RBAC map in backend/internal/handler/portal.go).
// Everything here is convenience.

// The CSRF token lives in a readable (non-httpOnly) `bx_csrf` cookie the server sets
// at login. A cookie is shared across same-origin tabs, so every tab reads the same
// token. We echo it in the X-CSRF-Token header; the server verifies THAT header
// against the session's stored hash. The cookie is transport only — the server never
// reads it (the double-submit is the header, not a cookie==header comparison).
const CSRF_COOKIE = "bx_csrf";
function csrfToken(): string {
  if (typeof document === "undefined") return "";
  const m = document.cookie.match(new RegExp("(?:^|;\\s*)" + CSRF_COOKIE + "=([^;]*)"));
  return m ? decodeURIComponent(m[1]) : "";
}

// Calls that own their own 401 handling and must NOT trip the global
// session-expired redirect: /login (a 401 is a bad key → inline error), /me (the
// shell redirects itself; a first-load 401 means "not signed in yet", not
// "expired"), and /logout (the shell navigates to /login after).
const NO_REAUTH_REDIRECT = new Set(["/v1/portal/login", "/v1/portal/me", "/v1/portal/logout"]);

export type Session = {
  actor: string;
  role: "ADMIN" | "RISK" | "FINANCE" | "OPS" | "SUPPORT";
  scope: string; // '*' = all scopes (platform admin), else a single config scope
  expires_at: string;
};

export class ApiError extends Error {
  constructor(
    public status: number,
    public errorCode: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = csrfToken();
  }
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    if (resp.status === 401 && typeof window !== "undefined" && !NO_REAUTH_REDIRECT.has(path.split("?")[0])) {
      // Session died mid-use — bounce to login with a one-time notice.
      window.location.assign("/login?expired=1");
    }
    throw new ApiError(resp.status, data.error_code ?? "UNKNOWN", data.message ?? "request failed");
  }
  return data as T;
}

export async function login(apiKey: string): Promise<Session> {
  // The server sets the httpOnly session cookie AND the readable bx_csrf cookie via
  // Set-Cookie; nothing to persist client-side (the csrf_token is still in the body
  // for backward compat but the cookie is the source of truth).
  const r = await request<Session & { csrf_token: string }>("POST", "/v1/portal/login", {
    api_key: apiKey,
  });
  return { actor: r.actor, role: r.role, scope: r.scope, expires_at: r.expires_at };
}

export async function logout(): Promise<void> {
  // The server clears both the session and bx_csrf cookies via Set-Cookie.
  await request("POST", "/v1/portal/logout");
}

export function me(): Promise<Session> {
  return request<Session>("GET", "/v1/portal/me");
}

export type ConfigVersion = {
  config_version_id: string;
  domain: string;
  scope: string;
  version_no: number;
  state: "DRAFT" | "SUBMITTED" | "APPROVED" | "ACTIVE" | "SUPERSEDED" | "ROLLED_BACK" | "REJECTED";
  content: unknown;
  content_hash: string;
  effective_from?: string;
  created_by: string;
  approved_by?: string;
  reason: string;
};

export type ConfigSummary = {
  domain: string;
  scope: string;
  active_version_no: number;
  active_since?: string;
  pending_count: number;
};

export function configActive(domain: string, scope: string): Promise<ConfigVersion> {
  const q = new URLSearchParams({ domain, scope });
  return request("GET", `/v1/portal/config/active?${q}`);
}

export function configOverview(): Promise<{ domains: ConfigSummary[] }> {
  return request("GET", "/v1/portal/config/overview");
}

export function configVersions(domain: string, scope: string): Promise<{ versions: ConfigVersion[] }> {
  const q = new URLSearchParams({ domain, scope });
  return request("GET", `/v1/portal/config/versions?${q}`);
}

export function configDraft(
  domain: string,
  scope: string,
  reason: string,
  content: unknown,
): Promise<ConfigVersion> {
  return request("POST", "/v1/portal/config/drafts", { domain, scope, reason, content });
}

export function configLifecycle(id: string, step: "submit" | "approve" | "activate"): Promise<unknown> {
  return request("POST", `/v1/portal/config/${id}/${step}`);
}

// --- M4c risk workspace ---

export type MoneyView = { amount_minor: number; currency: string; display: string };

export type GuardrailTrip = {
  trip_id: string;
  telco_id: string;
  programme_id: string;
  guardrail: "DAILY_DISBURSED" | "OPEN_EXPOSURE";
  measured: MoneyView;
  limit: MoneyView;
  state: "TRIPPED" | "REARM_REQUESTED" | "REARMED";
  tripped_at: string;
  rearm_requested_by?: string;
  rearm_approved_by?: string;
};

export function riskTrips(): Promise<{ trips: GuardrailTrip[] }> {
  return request("GET", "/v1/portal/risk/trips");
}

export function riskRequestRearm(tripId: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/risk/trips/${tripId}/request-rearm`, { reason });
}

export function riskApproveRearm(tripId: string): Promise<unknown> {
  return request("POST", `/v1/portal/risk/trips/${tripId}/approve-rearm`);
}

// --- M4d finance workspace: ledger browser ---

export type JournalHeader = {
  journal_id: string;
  event_type: string;
  telco_id: string;
  programme_id: string;
  advance_id?: string;
  correlation_id: string;
  accounting_date: string;
  posted_at: string;
};

export type JournalEntry = {
  entry_id: string;
  account_code: string;
  debit: MoneyView;
  credit: MoneyView;
};

export function ledgerJournals(params: { advance_id?: string; correlation_id?: string } = {}): Promise<{
  journals: JournalHeader[];
}> {
  const q = new URLSearchParams();
  if (params.advance_id) q.set("advance_id", params.advance_id);
  if (params.correlation_id) q.set("correlation_id", params.correlation_id);
  const qs = q.toString();
  return request("GET", `/v1/portal/finance/ledger/journals${qs ? `?${qs}` : ""}`);
}

export function ledgerJournal(id: string): Promise<{ journal: JournalHeader; entries: JournalEntry[] }> {
  return request("GET", `/v1/portal/finance/ledger/journals/${id}`);
}

export type ReconBreak = {
  recon_item_id: string;
  run_id: string;
  telco_id: string;
  item_type: string;
  status: string;
  platform_ref?: string;
  telco_ref?: string;
  assigned_to?: string;
  created_at: string;
};

export function financeBreaks(): Promise<{ breaks: ReconBreak[] }> {
  return request("GET", "/v1/portal/finance/breaks");
}

// Break resolution is a two-actor decision (R-P0-6 Slice E1): PROPOSE_RESOLVE
// (maker) then APPROVE_RESOLVE (a DISTINCT checker). Single-actor RESOLVE was
// retired — the backend rejects it. ESCALATE/NOTE are single-actor.
export type BreakAction =
  | "ASSIGN"
  | "ESCALATE"
  | "NOTE"
  | "PROPOSE_RESOLVE"
  | "APPROVE_RESOLVE";

export function financeBreakAction(
  id: string,
  action: BreakAction,
  reason: string,
): Promise<unknown> {
  return request("POST", `/v1/portal/finance/breaks/${id}/action`, { action, reason });
}

// --- held-recharge release queue (four-eyes; Phase 1 S2.3b backend) ------------
export type HeldRecharge = {
  held_id: string;
  source_event_id: string;
  msisdn_token: string;
  amount: MoneyView; // server-formatted money (amount_minor + currency + display); the UI never computes money
  occurred_at: string;
  reason: string;
  requested_by: string; // the maker (for the Wave A safety-UX: disable Approve for the proposer)
};

export function heldRecharges(telco: string): Promise<{ held: HeldRecharge[] }> {
  return request("GET", `/v1/portal/finance/held-recharges?telco=${encodeURIComponent(telco)}`);
}
export function heldRechargeRequestRelease(id: string, telco: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/finance/held-recharges/${encodeURIComponent(id)}/request-release`, { telco, reason });
}
export function heldRechargeApproveRelease(id: string, telco: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/finance/held-recharges/${encodeURIComponent(id)}/approve-release`, { telco, reason });
}
export function heldRechargeReject(id: string, telco: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/finance/held-recharges/${encodeURIComponent(id)}/reject`, { telco, reason });
}

// --- operator provisioning (ADMIN-only, four-eyes create; portal.go:175-180) ----
export type Operator = { actor: string; role: string; scope: string; status: string };
export type OperatorRequest = {
  request_id: string;
  actor: string;
  role: string;
  scope: string;
  reason: string;
  requested_by: string; // the maker — Approve is disabled in-UI for this actor (server also 409s)
};

export function operatorsList(): Promise<{ operators: Operator[] }> {
  return request("GET", "/v1/portal/operators");
}
export function operatorRequests(): Promise<{ requests: OperatorRequest[] }> {
  return request("GET", "/v1/portal/operators/requests");
}
export function operatorPropose(body: { actor: string; role: string; scope: string; reason: string }): Promise<{ request_id: string }> {
  return request("POST", "/v1/portal/operators/requests", body);
}
// Approve returns the ONE-TIME plaintext access key — shown once, stored hash-only.
export function operatorApprove(id: string): Promise<{ request_id: string; access_key: string; note: string }> {
  return request("POST", `/v1/portal/operators/requests/${encodeURIComponent(id)}/approve`);
}
export function operatorReject(id: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/operators/requests/${encodeURIComponent(id)}/reject`, { reason });
}
export function operatorRevoke(actor: string, reason: string): Promise<unknown> {
  return request("POST", `/v1/portal/operators/${encodeURIComponent(actor)}/revoke`, { reason });
}

export type SettlementStatement = {
  statement_id: string;
  telco_id: string;
  programme_id: string;
  period_start: string;
  period_end: string;
  state: "DRAFT" | "FINAL";
  currency: string;
  terms_version_id: string;
  finalised_at?: string;
};

export type SettlementLine = { line_code: string; amount: MoneyView };

export function financeSettlements(): Promise<{ statements: SettlementStatement[] }> {
  return request("GET", "/v1/portal/finance/settlements");
}

export function financeSettlement(id: string): Promise<{ statement: SettlementStatement; lines: SettlementLine[] }> {
  return request("GET", `/v1/portal/finance/settlements/${id}`);
}

export function financeSettlementVerify(id: string): Promise<{ statement_id: string; verified: boolean }> {
  return request("POST", `/v1/portal/finance/settlements/${id}/verify`);
}

// --- M4e ops workspace ---

export type AmbiguousAttempt = {
  attempt_id: string;
  advance_id: string;
  telco_id: string;
  programme_id: string;
  advance_state: string;
  face_value: MoneyView;
  state: "UNKNOWN" | "SENT";
  attempt_no: number;
  enquiry_count: number;
  submitted_at: string;
  next_enquiry_at?: string;
};

export function opsFulfilments(): Promise<{
  attempts: AmbiguousAttempt[];
  stale_sent_after_seconds: number;
}> {
  return request("GET", "/v1/portal/ops/fulfilments");
}

export function opsEnquireNow(id: string): Promise<{ attempt_id: string; rescheduled: boolean }> {
  return request("POST", `/v1/portal/ops/fulfilments/${id}/enquire-now`);
}

export type ParkedReversal = {
  pending_reversal_id: string;
  telco_id: string;
  original_source_event_id: string;
  reversal_source_event_id: string;
  amount: MoneyView;
  park_reason: string;
  received_at: string;
};

export function opsReversals(): Promise<{ reversals: ParkedReversal[] }> {
  return request("GET", "/v1/portal/ops/reversals");
}

export function opsReversalRetry(
  id: string,
): Promise<{ pending_reversal_id: string; applied: boolean; park_reason?: string }> {
  return request("POST", `/v1/portal/ops/reversals/${id}/retry`);
}

export type StatusAction = {
  action_id: string;
  telco_id: string;
  subscriber_account_id: string;
  msisdn_token: string;
  current_status: string;
  from_status: "ACTIVE" | "BARRED" | "CLOSED";
  to_status: "ACTIVE" | "BARRED" | "CLOSED";
  reason: string;
  requested_by: string;
  approved_by?: string;
  state: "REQUESTED" | "REJECTED" | "APPLIED";
  requested_at: string;
  decided_at?: string;
};

export function opsStatusActions(): Promise<{ actions: StatusAction[] }> {
  return request("GET", "/v1/portal/ops/status-actions");
}

export function opsStatusActionRequest(req: {
  telco_id?: string;
  msisdn_token: string;
  to_status: "ACTIVE" | "BARRED" | "CLOSED";
  reason: string;
}): Promise<{ action_id: string; state: string; from_status: string; to_status: string }> {
  return request("POST", "/v1/portal/ops/status-actions", req);
}

export function opsStatusActionDecide(
  id: string,
  decision: "approve" | "reject",
): Promise<{ action_id: string; state: string }> {
  return request("POST", `/v1/portal/ops/status-actions/${id}/${decision}`);
}

export type DemoScenario = { name: string; description: string; pool_size: number };

export function opsDemoScenarios(): Promise<{ telco_id: string; scenarios: DemoScenario[] }> {
  return request("GET", "/v1/portal/ops/demo/scenarios");
}

export type DemoRun = {
  run_id: string;
  telco_id: string;
  scenario: string;
  msisdn_token: string;
  advance_id: string;
  correlation_id: string;
  requested_by: string;
  created_at: string;
};

export function opsDemoRun(req: {
  telco_id?: string;
  scenario: string;
}): Promise<{ run_id: string; scenario: string; msisdn_token: string; advance_id: string }> {
  return request("POST", "/v1/portal/ops/demo/run", req);
}

export function opsDemoRuns(): Promise<{ runs: DemoRun[] }> {
  return request("GET", "/v1/portal/ops/demo/runs");
}

export type DemoChain = {
  run: DemoRun;
  advance: {
    advance_id: string;
    state: string;
    face_value: MoneyView;
    outstanding: MoneyView;
    activated_at?: string;
    closed_at?: string;
  };
  attempts: {
    attempt_id: string;
    attempt_no: number;
    state: string;
    telco_reference?: string;
    enquiry_count: number;
    submitted_at: string;
    resolved_at?: string;
    next_enquiry_at?: string;
  }[];
  notifications: { kind: string; state: string; created_at: string; sent_at?: string }[];
  journals: { journal_id: string; event_type: string; correlation_id: string }[];
};

export function opsDemoRunDetail(id: string): Promise<DemoChain> {
  return request("GET", `/v1/portal/ops/demo/runs/${id}`);
}

// --- M4f support workspace ---

export type TimelineAdvance = {
  advance_id: string;
  programme_id: string;
  state: string;
  face_value: MoneyView;
  outstanding: MoneyView;
  accepted_at: string;
  closed_at?: string;
};

export type TimelineComplaint = {
  complaint_id: string;
  advance_id?: string;
  channel: string;
  category: string;
  narrative: string;
  state: string;
  resolution?: string;
  opened_at: string;
};

export type SubscriberTimeline = {
  subscriber: {
    subscriber_account_id: string;
    telco_id: string;
    msisdn_token_masked: string;
    status: string;
    effective_from: string;
  };
  advances: TimelineAdvance[];
  notifications: { kind: string; state: string; created_at: string; sent_at?: string }[];
  complaints: TimelineComplaint[];
  status_actions: {
    action_id: string;
    from_status: string;
    to_status: string;
    reason: string;
    state: string;
    requested_at: string;
  }[];
};

export function supportTimeline(token: string): Promise<SubscriberTimeline> {
  return request("GET", `/v1/portal/support/subscriber?token=${encodeURIComponent(token)}`);
}

export type ComplaintItem = {
  complaint_id: string;
  telco_id: string;
  msisdn_token_masked?: string;
  advance_id?: string;
  channel: string;
  category: string;
  narrative: string;
  state: "OPEN" | "IN_REVIEW" | "RESOLVED" | "REJECTED";
  resolution?: string;
  opened_at: string;
};

export function supportComplaints(): Promise<{ complaints: ComplaintItem[] }> {
  return request("GET", "/v1/portal/support/complaints");
}

export function supportComplaintOpen(req: {
  telco_id?: string;
  msisdn_token?: string;
  advance_id?: string;
  channel: string;
  category: string;
  narrative: string;
}): Promise<{ complaint_id: string; state: string }> {
  return request("POST", "/v1/portal/support/complaints", req);
}

export function supportComplaintProgress(
  id: string,
  to: "IN_REVIEW" | "RESOLVED" | "REJECTED",
  resolution?: string,
): Promise<{ complaint_id: string; state: string }> {
  return request("POST", `/v1/portal/support/complaints/${id}/progress`, { to, resolution });
}
