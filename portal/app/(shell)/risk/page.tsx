"use client";

// Risk workspace (M4c): open guardrail trips and the two-person re-arm.
// Money is displayed exactly as the server formatted it — no client-side
// arithmetic. Authorization is server-side (scope + role); this UI only shows
// what the operator is permitted to see.

import { useCallback, useEffect, useState } from "react";
import { ApiError, GuardrailTrip, riskApproveRearm, riskRequestRearm, riskTrips } from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function RiskPage() {
  const [trips, setTrips] = useState<GuardrailTrip[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [reasonFor, setReasonFor] = useState<string | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await riskTrips();
      setTrips(r.trips);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function submitRequest(tripId: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await riskRequestRearm(tripId, reason);
      setNotice(`Re-arm requested for ${tripId}. A different operator must approve.`);
      setReasonFor(null);
      setReason("");
      await load();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  async function approve(tripId: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await riskApproveRearm(tripId);
      setNotice(`${tripId} re-armed. The programme resumes once no guardrail holds it.`);
      await load();
    } catch (err) {
      // The 409 (approver == requester) is the two-person rule doing its job.
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Risk — guardrail trips</h1>

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
        {trips === null ? (
          <p className="muted">Loading…</p>
        ) : trips.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>
            No open guardrail trips in your scope. A tripped guardrail suspends its
            programme and appears here for two-person re-arm.
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Programme</th>
                <th>Guardrail</th>
                <th>Measured</th>
                <th>Limit</th>
                <th>State</th>
                <th>Requested by</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {trips.map((t) => (
                <tr key={t.trip_id}>
                  <td className="mono">{t.programme_id}</td>
                  <td>{t.guardrail}</td>
                  <td className="mono">{t.measured.display}</td>
                  <td className="mono">{t.limit.display}</td>
                  <td>
                    <span className={`state state-${t.state === "TRIPPED" ? "SUBMITTED" : "APPROVED"}`}>
                      {t.state}
                    </span>
                  </td>
                  <td className="mono">{t.rearm_requested_by || "—"}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    {t.state === "TRIPPED" && (
                      <button className="small" disabled={busy} onClick={() => { setReasonFor(t.trip_id); setReason(""); }}>
                        Request re-arm
                      </button>
                    )}
                    {t.state === "REARM_REQUESTED" && (
                      <button className="small" disabled={busy} onClick={() => approve(t.trip_id)}>
                        Approve re-arm
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {reasonFor && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            Request re-arm — <span className="mono">{reasonFor}</span>
          </h2>
          <p className="muted" style={{ marginTop: 0 }}>
            State why the breach is resolved. A different operator must approve
            before lending resumes.
          </p>
          <label>
            <span className="muted">Reason (required)</span>
            <input value={reason} onChange={(e) => setReason(e.target.value)} required />
          </label>
          <div style={{ marginTop: 12 }}>
            <button disabled={busy || reason.trim() === ""} onClick={() => submitRequest(reasonFor)}>
              {busy ? "Submitting…" : "Submit request"}
            </button>{" "}
            <button disabled={busy} onClick={() => setReasonFor(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </>
  );
}
