"use client";

// Ops workspace (M4e-1) — the ambiguity queues. Two tabs:
//   Fulfilments: attempts whose telco outcome is unresolved (UNKNOWN, or SENT
//     past the governed staleness threshold). One action: enquire-now, which
//     reschedules the resolver — the portal never resolves attempt state.
//   Reversals: PARKED reversals with their current blocker (M3B-F1). One
//     action: retry, which re-runs the money core's own guarded apply.
// Authorization is server-side; empty states are honest.

import { useCallback, useEffect, useState } from "react";
import {
  AmbiguousAttempt,
  ApiError,
  ParkedReversal,
  opsEnquireNow,
  opsFulfilments,
  opsReversalRetry,
  opsReversals,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

function age(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `${mins}m`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 48) return `${hrs}h ${mins % 60}m`;
  return `${Math.floor(hrs / 24)}d`;
}

export default function OpsPage() {
  const [tab, setTab] = useState<"fulfilments" | "reversals">("fulfilments");
  const [attempts, setAttempts] = useState<AmbiguousAttempt[] | null>(null);
  const [staleAfter, setStaleAfter] = useState<number | null>(null);
  const [reversals, setReversals] = useState<ParkedReversal[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [f, r] = await Promise.all([opsFulfilments(), opsReversals()]);
      setAttempts(f.attempts);
      setStaleAfter(f.stale_sent_after_seconds);
      setReversals(r.reversals);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function enquire(id: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await opsEnquireNow(id);
      setNotice(`${id}: enquiry rescheduled — the resolver will chase the telco now.`);
      await load();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  async function retry(id: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const r = await opsReversalRetry(id);
      setNotice(
        r.applied
          ? `${id}: reversal applied — the queue drains.`
          : `${id}: still blocked — ${r.park_reason}.`,
      );
      await load();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Ops — ambiguity queues</h1>

      <div style={{ marginBottom: 16 }}>
        <button
          className={tab === "fulfilments" ? "" : "small"}
          onClick={() => setTab("fulfilments")}
        >
          Fulfilments{attempts ? ` (${attempts.length})` : ""}
        </button>{" "}
        <button
          className={tab === "reversals" ? "" : "small"}
          onClick={() => setTab("reversals")}
        >
          Parked reversals{reversals ? ` (${reversals.length})` : ""}
        </button>
      </div>

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

      {tab === "fulfilments" && (
        <div className="card">
          {attempts === null ? (
            <p className="muted">Loading…</p>
          ) : attempts.length === 0 ? (
            <p className="muted" style={{ margin: 0 }}>
              No ambiguous fulfilments in your scope. An attempt lands here when
              the telco outcome is UNKNOWN, or a SENT request has had no answer
              for {staleAfter != null ? `${Math.round(staleAfter / 60)} minutes` : "the governed threshold"}.
              The resolver keeps chasing on its own cadence — enquire-now just
              pulls one to the front.
            </p>
          ) : (
            <table className="data">
              <thead>
                <tr>
                  <th>Age</th>
                  <th>Attempt</th>
                  <th>Advance</th>
                  <th>Face value</th>
                  <th>State</th>
                  <th>Enquiries</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {attempts.map((a) => (
                  <tr key={a.attempt_id}>
                    <td className="muted">{age(a.submitted_at)}</td>
                    <td className="mono">{a.attempt_id}</td>
                    <td className="mono">
                      {a.advance_id}
                      <span className="muted"> · {a.advance_state}</span>
                    </td>
                    <td>{a.face_value.display}</td>
                    <td>
                      <span className={`state state-${a.state === "UNKNOWN" ? "REJECTED" : "SUBMITTED"}`}>
                        {a.state}
                      </span>
                    </td>
                    <td className="muted">{a.enquiry_count}</td>
                    <td style={{ textAlign: "right" }}>
                      <button className="small" disabled={busy} onClick={() => enquire(a.attempt_id)}>
                        Enquire now
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {tab === "reversals" && (
        <div className="card">
          {reversals === null ? (
            <p className="muted">Loading…</p>
          ) : reversals.length === 0 ? (
            <p className="muted" style={{ margin: 0 }}>
              No parked reversals in your scope. A reversal parks when its
              original event has not arrived, was never allocated, or applying
              it would break an invariant — each shows its blocker here until
              it applies or is worked through the breaks process. (Telco-level
              view: programme-scoped operators see an empty queue.)
            </p>
          ) : (
            <table className="data">
              <thead>
                <tr>
                  <th>Age</th>
                  <th>Reversal</th>
                  <th>Original event</th>
                  <th>Amount</th>
                  <th>Blocker</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {reversals.map((r) => (
                  <tr key={r.pending_reversal_id}>
                    <td className="muted">{age(r.received_at)}</td>
                    <td className="mono">{r.pending_reversal_id}</td>
                    <td className="mono">{r.original_source_event_id}</td>
                    <td>{r.amount.display}</td>
                    <td>
                      <span className="state state-REJECTED">{r.park_reason}</span>
                    </td>
                    <td style={{ textAlign: "right" }}>
                      <button className="small" disabled={busy} onClick={() => retry(r.pending_reversal_id)}>
                        Retry apply
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </>
  );
}
