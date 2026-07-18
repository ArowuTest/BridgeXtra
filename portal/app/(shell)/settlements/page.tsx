"use client";

// Finance workspace (M4d part 3) — settlement statements + verification. A
// FINAL statement can be re-derived from the ledger and checked against its
// pinned hash: "verify" reports whether it reproduces. No client money
// arithmetic — line amounts are server-formatted.

import { useCallback, useEffect, useState } from "react";
import {
  ApiError,
  SettlementLine,
  SettlementStatement,
  financeSettlement,
  financeSettlementVerify,
  financeSettlements,
} from "@/lib/api";

function fmtErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.errorCode}: ${err.message}`;
  return "Request failed. Try again shortly.";
}

export default function SettlementsPage() {
  const [statements, setStatements] = useState<SettlementStatement[] | null>(null);
  const [selected, setSelected] = useState<{ statement: SettlementStatement; lines: SettlementLine[] } | null>(null);
  const [verify, setVerify] = useState<Record<string, boolean>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const r = await financeSettlements();
      setStatements(r.statements);
    } catch (err) {
      setError(fmtErr(err));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function open(id: string) {
    setError(null);
    try {
      setSelected(await financeSettlement(id));
    } catch (err) {
      setError(fmtErr(err));
    }
  }

  async function runVerify(id: string) {
    setBusy(true);
    setError(null);
    try {
      const r = await financeSettlementVerify(id);
      setVerify((v) => ({ ...v, [id]: r.verified }));
    } catch (err) {
      setError(fmtErr(err));
    } finally {
      setBusy(false);
    }
  }

  function fmtDate(s: string) {
    return new Date(s).toLocaleDateString();
  }

  return (
    <>
      <h1>Finance — settlements</h1>

      {error && (
        <div className="card" style={{ marginBottom: 16, borderColor: "var(--danger)" }}>
          <p className="error mono" style={{ margin: 0 }}>{error}</p>
        </div>
      )}

      <div className="card">
        {statements === null ? (
          <p className="muted">Loading…</p>
        ) : statements.length === 0 ? (
          <p className="muted" style={{ margin: 0 }}>No settlement statements in your scope.</p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Period</th>
                <th>Programme</th>
                <th>State</th>
                <th>Verification</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {statements.map((s) => (
                <tr key={s.statement_id}>
                  <td className="muted">{fmtDate(s.period_start)} – {fmtDate(s.period_end)}</td>
                  <td className="mono">{s.programme_id}</td>
                  <td>
                    <span className={`state state-${s.state === "FINAL" ? "ACTIVE" : "DRAFT"}`}>{s.state}</span>
                  </td>
                  <td>
                    {verify[s.statement_id] === undefined ? (
                      <span className="muted">—</span>
                    ) : verify[s.statement_id] ? (
                      <span style={{ color: "var(--accent)" }}>✓ reproduces</span>
                    ) : (
                      <span className="error">✗ does not reproduce</span>
                    )}
                  </td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <button className="small" onClick={() => open(s.statement_id)}>Lines</button>{" "}
                    {s.state === "FINAL" && (
                      <button className="small" disabled={busy} onClick={() => runVerify(s.statement_id)}>
                        Verify
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {selected && (
        <div className="card" style={{ marginTop: 16 }}>
          <h2 style={{ marginTop: 0, fontSize: 16 }}>
            Statement lines{" "}
            <span className="muted mono" style={{ fontSize: 12 }}>{selected.statement.statement_id}</span>
          </h2>
          {selected.lines.length === 0 ? (
            <p className="muted" style={{ margin: 0 }}>No lines (zero-activity period).</p>
          ) : (
            <table className="data">
              <thead>
                <tr>
                  <th>Line</th>
                  <th style={{ textAlign: "right" }}>Amount</th>
                </tr>
              </thead>
              <tbody>
                {selected.lines.map((l) => (
                  <tr key={l.line_code}>
                    <td className="mono">{l.line_code}</td>
                    <td className="mono" style={{ textAlign: "right" }}>{l.amount.display}</td>
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
