"use client";

// Finance workspace (M4d part 2) — reconciliation breaks queue. Telco-scoped:
// a telco/ADMIN operator works their breaks (assign, resolve with a reason;
// breaks are never edited away). Authorization is server-side.

import { useCallback, useEffect, useState } from "react";
import { ApiError, BreakAction, ReconBreak, financeBreakAction, financeBreaks } from "@/lib/api";
import { fmtDateTime } from "@/lib/datetime";

const ACTION_LABEL: Record<BreakAction, string> = {
  ASSIGN: "assigned to self",
  PROPOSE_RESOLVE: "resolution proposed",
  APPROVE_RESOLVE: "resolution approved (break closed)",
  ESCALATE: "escalated",
  NOTE: "note added",
};

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function BreaksPage() {
  const [breaks, setBreaks] = useState<ReconBreak[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [resolveFor, setResolveFor] = useState<string | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await financeBreaks();
      setBreaks(r.breaks);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function act(id: string, action: BreakAction, why: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await financeBreakAction(id, action, why);
      setNotice(`${id}: ${ACTION_LABEL[action]}.`);
      setResolveFor(null);
      setReason("");
      await load();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Finance — reconciliation breaks</h1>

      {error && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--danger)" }}>
          <p className="error mono" style={{ margin: 0 }}>{error}</p>
        </div>
      )}
      {notice && (
        <div className="card" style={{ marginBottom: 16 }}>
          <p style={{ margin: 0, color: "var(--accent)" }}>{notice}</p>
        </div>
      )}

      <div className="card">
        {breaks === null ? (
          <p className="muted">Loading…</p>
        ) : breaks.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>
            No open breaks in your scope. A break is a platform-vs-telco
            discrepancy the reconciler flagged; it is worked to a reason, never
            edited away.
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Detected</th>
                <th>Type</th>
                <th>Status</th>
                <th>Platform ref</th>
                <th>Assigned</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {breaks.map((b) => (
                <tr key={b.recon_item_id}>
                  <td className="muted">{fmtDateTime(b.created_at)}</td>
                  <td>{b.item_type}</td>
                  <td>
                    <span className="state state-SUBMITTED">{b.status.replace("BREAK_", "")}</span>
                  </td>
                  <td className="mono">{b.platform_ref || "—"}</td>
                  <td className="mono">{b.assigned_to || "—"}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button className="small" disabled={busy} onClick={() => act(b.recon_item_id, "ASSIGN", "assigning to self")}>
                      Assign me
                    </button>{" "}
                    <button className="small" disabled={busy} onClick={() => { setResolveFor(b.recon_item_id); setReason(""); }}>
                      Work…
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {resolveFor && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            Work break — <span className="mono">{resolveFor}</span>
          </h2>
          <p className="muted" style={{ marginTop: 0 }}>
            A money break is closed under two-actor control: one operator{" "}
            <strong>proposes</strong> a resolution, then a <strong>different</strong>{" "}
            operator <strong>approves</strong> it. Escalate or add a note at any time.
            Everything is recorded permanently; a break is never edited away.
          </p>
          <label>
            <span className="muted">Reason (required)</span>
            <input value={reason} onChange={(e) => setReason(e.target.value)} required />
          </label>
          <div style={{ marginTop: 12, display: "flex", gap: 8, flexWrap: "wrap" }}>
            <button disabled={busy || reason.trim() === ""} onClick={() => act(resolveFor, "PROPOSE_RESOLVE", reason)}>
              {busy ? "Working…" : "Propose resolution"}
            </button>
            <button className="small" disabled={busy || reason.trim() === ""} onClick={() => act(resolveFor, "APPROVE_RESOLVE", reason)}>
              Approve resolution
            </button>
            <button className="small" disabled={busy || reason.trim() === ""} onClick={() => act(resolveFor, "ESCALATE", reason)}>
              Escalate
            </button>
            <button className="small" disabled={busy || reason.trim() === ""} onClick={() => act(resolveFor, "NOTE", reason)}>
              Add note
            </button>
            <button className="small" disabled={busy} onClick={() => setResolveFor(null)}>Cancel</button>
          </div>
        </div>
      )}
    </>
  );
}
