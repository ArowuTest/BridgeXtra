// Portal API client. The session lives in an httpOnly cookie the JS cannot
// read; the CSRF token is the only piece held client-side (sessionStorage —
// per-tab, gone on close). NOTE: authorization lives on the SERVER (RBAC map
// in backend/internal/handler/portal.go). Everything here is convenience.

const CSRF_KEY = "bx_csrf";

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
    headers["X-CSRF-Token"] = sessionStorage.getItem(CSRF_KEY) ?? "";
  }
  const resp = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new ApiError(resp.status, data.error_code ?? "UNKNOWN", data.message ?? "request failed");
  }
  return data as T;
}

export async function login(apiKey: string): Promise<Session> {
  const r = await request<Session & { csrf_token: string }>("POST", "/v1/portal/login", {
    api_key: apiKey,
  });
  sessionStorage.setItem(CSRF_KEY, r.csrf_token);
  return { actor: r.actor, role: r.role, scope: r.scope, expires_at: r.expires_at };
}

export async function logout(): Promise<void> {
  try {
    await request("POST", "/v1/portal/logout");
  } finally {
    sessionStorage.removeItem(CSRF_KEY);
  }
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

export function financeBreakAction(
  id: string,
  action: "ASSIGN" | "RESOLVE" | "ESCALATE" | "NOTE",
  reason: string,
): Promise<unknown> {
  return request("POST", `/v1/portal/finance/breaks/${id}/action`, { action, reason });
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
