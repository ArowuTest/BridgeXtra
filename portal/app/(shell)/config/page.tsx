"use client";

// Read-only active-config viewer (ADMIN/RISK/FINANCE per the server map).
// The full maker-checker authoring UI is the M4b slice; this proves the
// wire end-to-end without faking anything.

import { FormEvent, useState } from "react";
import { ApiError, configActive } from "@/lib/api";

export default function ConfigPage() {
  const [domain, setDomain] = useState("platform.outbox");
  const [scope, setScope] = useState("global");
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      const cv = await configActive(domain.trim(), scope.trim());
      setResult(JSON.stringify(cv, null, 2));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setError("No active configuration for that domain/scope.");
      } else if (err instanceof ApiError && err.status === 403) {
        setError("Your role is not permitted to view configuration.");
      } else {
        setError("Lookup failed. Try again shortly.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Configuration</h1>
      <div className="card" style={{ marginBottom: 16 }}>
        <form onSubmit={onSubmit} style={{ display: "grid", gap: 12, gridTemplateColumns: "1fr 1fr auto", alignItems: "end" }}>
          <label>
            <span className="muted">Domain</span>
            <input value={domain} onChange={(e) => setDomain(e.target.value)} required />
          </label>
          <label>
            <span className="muted">Scope</span>
            <input value={scope} onChange={(e) => setScope(e.target.value)} required />
          </label>
          <button type="submit" disabled={busy}>
            {busy ? "Loading…" : "View active"}
          </button>
        </form>
        {error && <p className="error" style={{ marginBottom: 0 }}>{error}</p>}
      </div>
      {result && (
        <div className="card">
          <pre className="mono" style={{ margin: 0, overflowX: "auto" }}>{result}</pre>
        </div>
      )}
    </>
  );
}
