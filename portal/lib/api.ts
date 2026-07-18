// Portal API client. The session lives in an httpOnly cookie the JS cannot
// read; the CSRF token is the only piece held client-side (sessionStorage —
// per-tab, gone on close). NOTE: authorization lives on the SERVER (RBAC map
// in backend/internal/handler/portal.go). Everything here is convenience.

const CSRF_KEY = "bx_csrf";

export type Session = {
  actor: string;
  role: "ADMIN" | "RISK" | "FINANCE" | "OPS" | "SUPPORT";
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
  return { actor: r.actor, role: r.role, expires_at: r.expires_at };
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

export function configActive(domain: string, scope: string): Promise<unknown> {
  const q = new URLSearchParams({ domain, scope });
  return request("GET", `/v1/portal/config/active?${q}`);
}
