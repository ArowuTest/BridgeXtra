"use client";

// Support workspace (M4f) — the masked subscriber timeline (V2-SUB-008,
// UI-004) and the complaints workflow. Tokens are masked SERVER-side; this
// page never sees a full token except the one the operator typed to search.
// SUPPORT is read-only on financial truth (V3-ORG-005): its only actions
// here are opening and progressing complaints.

import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  ComplaintItem,
  SubscriberTimeline,
  supportComplaintOpen,
  supportComplaintProgress,
  supportComplaints,
  supportTimeline,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

const CATEGORIES = ["DISPUTED_ADVANCE", "DISPUTED_RECOVERY", "DISCLOSURE", "SERVICE", "OTHER"];

export default function SupportPage() {
  const [tab, setTab] = useState<"timeline" | "complaints">("timeline");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Timeline.
  const [searchToken, setSearchToken] = useState("");
  const [timeline, setTimeline] = useState<SubscriberTimeline | null>(null);

  // Complaints.
  const [complaints, setComplaints] = useState<ComplaintItem[] | null>(null);
  const [resolveFor, setResolveFor] = useState<string | null>(null);
  const [resolution, setResolution] = useState("");
  const [form, setForm] = useState({ telco_id: "", msisdn_token: "", advance_id: "", channel: "CALL_CENTRE", category: "DISPUTED_ADVANCE", narrative: "" });

  const loadComplaints = useCallback(async () => {
    setError(null);
    try {
      const r = await supportComplaints();
      setComplaints(r.complaints);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    loadComplaints();
  }, [loadComplaints]);

  async function search() {
    setBusy(true);
    setError(null);
    setNotice(null);
    setTimeline(null);
    try {
      setTimeline(await supportTimeline(searchToken.trim()));
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  async function openComplaint() {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const r = await supportComplaintOpen({
        telco_id: form.telco_id || undefined,
        msisdn_token: form.msisdn_token || undefined,
        advance_id: form.advance_id || undefined,
        channel: form.channel,
        category: form.category,
        narrative: form.narrative,
      });
      setNotice(`${r.complaint_id}: complaint opened.`);
      setForm({ ...form, msisdn_token: "", advance_id: "", narrative: "" });
      await loadComplaints();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  async function progress(id: string, to: "IN_REVIEW" | "RESOLVED" | "REJECTED", why?: string) {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const r = await supportComplaintProgress(id, to, why);
      setNotice(`${id}: ${r.state.toLowerCase().replace("_", " ")}.`);
      setResolveFor(null);
      setResolution("");
      await loadComplaints();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Support — subscriber care</h1>

      <div style={{ marginBottom: 16 }}>
        <button className={tab === "timeline" ? "" : "small"} onClick={() => setTab("timeline")}>
          Subscriber timeline
        </button>{" "}
        <button className={tab === "complaints" ? "" : "small"} onClick={() => setTab("complaints")}>
          Complaints{complaints ? ` (${complaints.filter((c) => c.state === "OPEN" || c.state === "IN_REVIEW").length} open)` : ""}
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

      {tab === "timeline" && (
        <>
          <div className="card" style={{ marginBottom: 16 }}>
            <p className="muted" style={{ marginTop: 0 }}>
              Look up a subscriber by their FULL tokenised MSISDN (from the
              channel system). Responses show tokens masked — the platform is
              PII-lean and never echoes a full token back.
            </p>
            <div style={{ display: "flex", gap: 8 }}>
              <input
                placeholder="tok_…"
                value={searchToken}
                onChange={(e) => setSearchToken(e.target.value)}
                style={{ flex: 1 }}
                onKeyDown={(e) => e.key === "Enter" && searchToken && search()}
              />
              <button disabled={busy || !searchToken} onClick={search}>
                Search
              </button>
            </div>
          </div>

          {timeline && (
            <>
              <div className="card" style={{ marginBottom: 16 }}>
                <h2 style={{ marginTop: 0, fontSize: 16 }}>
                  <span className="mono">{timeline.subscriber.msisdn_token_masked}</span>{" "}
                  <span className={`state state-${timeline.subscriber.status === "ACTIVE" ? "ACTIVE" : "REJECTED"}`}>
                    {timeline.subscriber.status}
                  </span>
                </h2>
                <p className="muted" style={{ margin: 0 }}>
                  {timeline.subscriber.telco_id} · identity since{" "}
                  {new Date(timeline.subscriber.effective_from).toLocaleDateString()}
                </p>
              </div>

              <div className="card" style={{ marginBottom: 16 }}>
                <h3 style={{ marginTop: 0, fontSize: 14 }}>Advances</h3>
                {timeline.advances.length === 0 ? (
                  <p className="muted" style={{ margin: 0 }}>No advances on record.</p>
                ) : (
                  <table className="data">
                    <tbody>
                      {timeline.advances.map((a) => (
                        <tr key={a.advance_id}>
                          <td className="muted">{new Date(a.accepted_at).toLocaleDateString()}</td>
                          <td className="mono">{a.advance_id}</td>
                          <td>{a.face_value.display}</td>
                          <td className="muted">outstanding {a.outstanding.display}</td>
                          <td>
                            <span className={`state state-${a.state === "ACTIVE" ? "ACTIVE" : a.state === "CLOSED" ? "SUBMITTED" : "REJECTED"}`}>
                              {a.state}
                            </span>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>

              <div className="card" style={{ marginBottom: 16 }}>
                <h3 style={{ marginTop: 0, fontSize: 14 }}>Complaints</h3>
                {timeline.complaints.length === 0 ? (
                  <p className="muted" style={{ margin: 0 }}>No complaints on record.</p>
                ) : (
                  <table className="data">
                    <tbody>
                      {timeline.complaints.map((c) => (
                        <tr key={c.complaint_id}>
                          <td className="muted">{new Date(c.opened_at).toLocaleDateString()}</td>
                          <td>{c.category}</td>
                          <td>{c.narrative}</td>
                          <td>
                            <span className={`state state-${c.state === "RESOLVED" ? "ACTIVE" : "SUBMITTED"}`}>{c.state}</span>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>

              <div className="card">
                <h3 style={{ marginTop: 0, fontSize: 14 }}>Messages & status history</h3>
                {timeline.notifications.length === 0 && timeline.status_actions.length === 0 ? (
                  <p className="muted" style={{ margin: 0 }}>No messages or status actions on record.</p>
                ) : (
                  <>
                    <ul style={{ marginTop: 0 }}>
                      {timeline.notifications.map((n, i) => (
                        <li key={i} className="mono" style={{ fontSize: 13 }}>
                          {n.kind} · {n.state} · {new Date(n.created_at).toLocaleString()}
                        </li>
                      ))}
                      {timeline.status_actions.map((a) => (
                        <li key={a.action_id} className="mono" style={{ fontSize: 13 }}>
                          {a.from_status} → {a.to_status} ({a.state}) — {a.reason}
                        </li>
                      ))}
                    </ul>
                  </>
                )}
              </div>
            </>
          )}
        </>
      )}

      {tab === "complaints" && (
        <>
          <div className="card" style={{ marginBottom: 16 }}>
            <h2 style={{ marginTop: 0, fontSize: 16 }}>Open a complaint</h2>
            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              <input placeholder="msisdn token (optional)" value={form.msisdn_token}
                onChange={(e) => setForm({ ...form, msisdn_token: e.target.value })} style={{ minWidth: 200 }} />
              <input placeholder="advance id (optional)" value={form.advance_id}
                onChange={(e) => setForm({ ...form, advance_id: e.target.value })} style={{ minWidth: 200 }} />
              <input placeholder="telco id (all-scope only)" value={form.telco_id}
                onChange={(e) => setForm({ ...form, telco_id: e.target.value })} style={{ minWidth: 180 }} />
              <input placeholder="channel (e.g. CALL_CENTRE)" value={form.channel}
                onChange={(e) => setForm({ ...form, channel: e.target.value })} style={{ minWidth: 180 }} />
              <select value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value })}>
                {CATEGORIES.map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </select>
              <input placeholder="narrative (required)" value={form.narrative}
                onChange={(e) => setForm({ ...form, narrative: e.target.value })} style={{ flex: 1, minWidth: 260 }} />
              <button disabled={busy || !form.narrative || !form.channel} onClick={openComplaint}>
                Open
              </button>
            </div>
          </div>

          <div className="card">
            {complaints === null ? (
              <p className="muted">Loading…</p>
            ) : complaints.length === 0 ? (
              <p className="muted" style={{ margin: 0 }}>
                No complaints in your scope. A complaint is worked to a
                resolution, never edited away — closed cases stay on record.
              </p>
            ) : (
              <table className="data">
                <thead>
                  <tr>
                    <th>Opened</th>
                    <th>Subscriber</th>
                    <th>Category</th>
                    <th>Narrative</th>
                    <th>State</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {complaints.map((c) => (
                    <tr key={c.complaint_id}>
                      <td className="muted">{new Date(c.opened_at).toLocaleDateString()}</td>
                      <td className="mono">{c.msisdn_token_masked || "—"}</td>
                      <td>{c.category}</td>
                      <td>
                        {c.narrative}
                        {c.resolution && <span className="muted"> — {c.resolution}</span>}
                      </td>
                      <td>
                        <span className={`state state-${c.state === "RESOLVED" ? "ACTIVE" : c.state === "REJECTED" ? "REJECTED" : "SUBMITTED"}`}>
                          {c.state}
                        </span>
                      </td>
                      <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                        {c.state === "OPEN" && (
                          <button className="small" disabled={busy} onClick={() => progress(c.complaint_id, "IN_REVIEW")}>
                            Review
                          </button>
                        )}{" "}
                        {(c.state === "OPEN" || c.state === "IN_REVIEW") && (
                          <button className="small" disabled={busy} onClick={() => { setResolveFor(c.complaint_id); setResolution(""); }}>
                            Close
                          </button>
                        )}
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
                Close complaint — <span className="mono">{resolveFor}</span>
              </h2>
              <p className="muted" style={{ marginTop: 0 }}>
                A resolution is required; it becomes part of the permanent case
                record.
              </p>
              <div style={{ display: "flex", gap: 8 }}>
                <input placeholder="resolution (required)" value={resolution}
                  onChange={(e) => setResolution(e.target.value)} style={{ flex: 1 }} />
                <button disabled={busy || !resolution} onClick={() => progress(resolveFor, "RESOLVED", resolution)}>
                  Resolve
                </button>
                <button className="small" disabled={busy || !resolution} onClick={() => progress(resolveFor, "REJECTED", resolution)}>
                  Reject
                </button>
                <button className="small" onClick={() => setResolveFor(null)}>
                  Cancel
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </>
  );
}
