"use client";

// Configuration governance workspace (M4b). The full maker-checker lifecycle
// — draft → submit → approve → activate — clickable end-to-end against the
// real backend. Server errors (validation refusals, maker-checker 409s) are
// surfaced VERBATIM: the validator's message is the operator's guidance.
// Action buttons render for everyone the server map allows on the page;
// authorization is enforced server-side (deny-by-default RBAC), never here.

import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  ConfigSummary,
  ConfigVersion,
  configDraft,
  configLifecycle,
  configOverview,
  configVersions,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function ConfigPage() {
  const [domains, setDomains] = useState<ConfigSummary[] | null>(null);
  const [selected, setSelected] = useState<ConfigSummary | null>(null);
  const [versions, setVersions] = useState<ConfigVersion[] | null>(null);
  const [viewing, setViewing] = useState<ConfigVersion | null>(null);
  const [drafting, setDrafting] = useState(false);
  const [draftContent, setDraftContent] = useState("");
  const [draftReason, setDraftReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const loadOverview = useCallback(async () => {
    try {
      const r = await configOverview();
      setDomains(r.domains);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  const loadVersions = useCallback(async (s: ConfigSummary) => {
    setSelected(s);
    setViewing(null);
    setDrafting(false);
    setError(null);
    setNotice(null);
    try {
      const r = await configVersions(s.domain, s.scope);
      setVersions(r.versions);
    } catch (err) {
      setVersions(null);
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    loadOverview();
  }, [loadOverview]);

  async function refresh() {
    await loadOverview();
    if (selected) await loadVersions(selected);
  }

  function startDraft() {
    const active = versions?.find((v) => v.state === "ACTIVE");
    setDraftContent(JSON.stringify(active?.content ?? {}, null, 2));
    setDraftReason("");
    setDrafting(true);
    setViewing(null);
    setError(null);
    setNotice(null);
  }

  async function submitDraft() {
    if (!selected) return;
    let content: unknown;
    try {
      content = JSON.parse(draftContent);
    } catch {
      setError("Draft content is not valid JSON.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const cv = await configDraft(selected.domain, selected.scope, draftReason, content);
      setNotice(`Draft v${cv.version_no} created (${cv.config_version_id}).`);
      setDrafting(false);
      await refresh();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  async function lifecycle(v: ConfigVersion, step: "submit" | "approve" | "activate") {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await configLifecycle(v.config_version_id, step);
      setNotice(`v${v.version_no} ${step} — done.`);
      await refresh();
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Configuration</h1>

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

      <div className="card" style={{ marginBottom: 16 }}>
        <h2 style={{ marginTop: 0, fontSize: 16 }}>Domains</h2>
        {domains === null ? (
          <p className="muted">Loading…</p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Domain</th>
                <th>Scope</th>
                <th>Active</th>
                <th>Since</th>
                <th>Pending</th>
              </tr>
            </thead>
            <tbody>
              {domains.map((d) => (
                <tr
                  key={`${d.domain}/${d.scope}`}
                  onClick={() => loadVersions(d)}
                  style={{
                    cursor: "pointer",
                    background:
                      selected?.domain === d.domain && selected?.scope === d.scope
                        ? "var(--panel-2)"
                        : undefined,
                  }}
                >
                  <td className="mono">{d.domain}</td>
                  <td className="mono">{d.scope}</td>
                  <td>{d.active_version_no > 0 ? `v${d.active_version_no}` : "—"}</td>
                  <td className="muted">
                    {d.active_since ? new Date(d.active_since).toLocaleString() : "—"}
                  </td>
                  <td>{d.pending_count > 0 ? d.pending_count : ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {selected && (
        <div className="card" style={{ marginBottom: 16 }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
            <h2 style={{ margin: 0, fontSize: 16 }}>
              <span className="mono">{selected.domain}</span>{" "}
              <span className="muted mono">/ {selected.scope}</span>
            </h2>
            <button onClick={startDraft} disabled={busy}>
              New draft
            </button>
          </div>
          {versions === null ? (
            <p className="muted">Loading…</p>
          ) : (
            <table className="data" style={{ marginTop: 12 }}>
              <thead>
                <tr>
                  <th>Version</th>
                  <th>State</th>
                  <th>Created by</th>
                  <th>Approved by</th>
                  <th>Reason</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {versions.map((v) => (
                  <tr key={v.config_version_id}>
                    <td>v{v.version_no}</td>
                    <td>
                      <span className={`state state-${v.state}`}>{v.state}</span>
                    </td>
                    <td className="mono">{v.created_by}</td>
                    <td className="mono">{v.approved_by || "—"}</td>
                    <td className="muted">{v.reason}</td>
                    <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                      <button className="small" onClick={() => { setViewing(v); setDrafting(false); }}>
                        View
                      </button>{" "}
                      {v.state === "DRAFT" && (
                        <button className="small" disabled={busy} onClick={() => lifecycle(v, "submit")}>
                          Submit
                        </button>
                      )}
                      {v.state === "SUBMITTED" && (
                        <button className="small" disabled={busy} onClick={() => lifecycle(v, "approve")}>
                          Approve
                        </button>
                      )}
                      {v.state === "APPROVED" && (
                        <button className="small" disabled={busy} onClick={() => lifecycle(v, "activate")}>
                          Activate
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {drafting && selected && (
        <div className="card" style={{ marginBottom: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            New draft for <span className="mono">{selected.domain}</span>
          </h2>
          <p className="muted" style={{ marginTop: 0 }}>
            Prefilled from the active version. The domain validator runs at
            approval — refusals appear here verbatim.
          </p>
          <label>
            <span className="muted">Content (JSON)</span>
            <textarea
              className="mono"
              rows={12}
              value={draftContent}
              onChange={(e) => setDraftContent(e.target.value)}
              spellCheck={false}
            />
          </label>
          <label style={{ display: "block", margin: "12px 0" }}>
            <span className="muted">Reason (required)</span>
            <input value={draftReason} onChange={(e) => setDraftReason(e.target.value)} required />
          </label>
          <button onClick={submitDraft} disabled={busy || draftReason.trim() === ""}>
            {busy ? "Creating…" : "Create draft"}
          </button>{" "}
          <button onClick={() => setDrafting(false)} disabled={busy}>
            Cancel
          </button>
        </div>
      )}

      {viewing && (
        <div className="card">
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            v{viewing.version_no}{" "}
            <span className={`state state-${viewing.state}`}>{viewing.state}</span>
          </h2>
          <p className="muted" style={{ margin: "4px 0 12px" }}>
            {viewing.config_version_id} · hash {viewing.content_hash.slice(0, 16)}…
          </p>
          <pre className="mono" style={{ margin: 0, overflowX: "auto" }}>
            {JSON.stringify(viewing.content, null, 2)}
          </pre>
        </div>
      )}
    </>
  );
}
